// Package auth signs admins in through any standard OIDC provider and keeps
// them signed in with an HMAC-signed cookie. Admin rights come from a group
// in the ID token's groups claim.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/dor1a/noroshi/internal/config"
)

const (
	sessionCookie = "noroshi_session"
	loginCookie   = "noroshi_login"
	sessionTTL    = 8 * time.Hour
	loginTTL      = 10 * time.Minute

	// A browser drops a cookie over 4 KiB. The ID token is left out of a
	// session that would not fit, and logout then goes without a hint.
	maxSessionCookie = 3800
)

// User is the signed-in admin.
type User struct {
	Subject string    `json:"sub"`
	Name    string    `json:"name"`
	Email   string    `json:"email"`
	Expires time.Time `json:"exp"`

	// IDToken is sent back to the provider as id_token_hint on logout.
	IDToken string `json:"idt,omitempty"`
}

type ctxKey struct{}

// UserFrom returns the admin stored in the request context, if any.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(ctxKey{}).(*User)
	return u
}

// ErrorFunc renders an error page. The key is an i18n message key.
type ErrorFunc func(w http.ResponseWriter, r *http.Request, status int, key string)

// Auth handles login, callback, logout, and the admin guard.
type Auth struct {
	env    config.Env
	log    *slog.Logger
	secure bool
	OnErr  ErrorFunc

	mu         sync.Mutex
	verifier   *oidc.IDTokenVerifier
	oauth      *oauth2.Config
	endSession string
}

// New creates the handler set. The provider is discovered on first use so
// noroshi can start while the identity provider is down.
func New(env config.Env, log *slog.Logger) *Auth {
	return &Auth{
		env:    env,
		log:    log,
		secure: strings.HasPrefix(env.AdminURL, "https://"),
		OnErr: func(w http.ResponseWriter, _ *http.Request, status int, key string) {
			http.Error(w, key, status)
		},
	}
}

// DevMode reports whether authentication is switched off.
func (a *Auth) DevMode() bool { return a.env.DevNoAuth }

func (a *Auth) client() (*oidc.IDTokenVerifier, *oauth2.Config, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.verifier != nil {
		return a.verifier, a.oauth, nil
	}
	// The provider keeps this context for later key fetches, so it must not
	// be a request context. The client timeout bounds every call instead.
	ctx := oidc.ClientContext(context.Background(), &http.Client{Timeout: 15 * time.Second})
	p, err := oidc.NewProvider(ctx, a.env.OIDCIssuer)
	if err != nil {
		return nil, nil, err
	}
	a.verifier = p.Verifier(&oidc.Config{ClientID: a.env.OIDCClientID})
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := p.Claims(&meta); err == nil {
		a.endSession = meta.EndSession
	}
	a.oauth = &oauth2.Config{
		ClientID:     a.env.OIDCClientID,
		ClientSecret: a.env.OIDCClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.env.AdminURL + "/oauth/callback",
		Scopes:       a.env.OIDCScopes,
	}
	return a.verifier, a.oauth, nil
}

type loginState struct {
	State    string    `json:"state"`
	Nonce    string    `json:"nonce"`
	Verifier string    `json:"verifier"`
	Next     string    `json:"next"`
	Expires  time.Time `json:"exp"`
}

// Login starts the authorization code flow with PKCE.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	if a.env.DevNoAuth {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	_, oc, err := a.client()
	if err != nil {
		a.log.Error("oidc discovery failed", "err", err)
		a.OnErr(w, r, http.StatusBadGateway, "error.oidc_down")
		return
	}
	ls := loginState{
		State:    randomString(),
		Nonce:    randomString(),
		Verifier: oauth2.GenerateVerifier(),
		Next:     safeNext(r.URL.Query().Get("next")),
		Expires:  time.Now().Add(loginTTL),
	}
	v, err := a.sign(ls)
	if err != nil {
		a.OnErr(w, r, http.StatusInternalServerError, "error.login_failed")
		return
	}
	a.setCookie(w, loginCookie, v, loginTTL)
	http.Redirect(w, r, oc.AuthCodeURL(ls.State, oidc.Nonce(ls.Nonce), oauth2.S256ChallengeOption(ls.Verifier)), http.StatusFound)
}

// Callback finishes the flow and sets the session cookie.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	verifier, oc, err := a.client()
	if err != nil {
		a.OnErr(w, r, http.StatusBadGateway, "error.oidc_down")
		return
	}
	var ls loginState
	c, err := r.Cookie(loginCookie)
	if err != nil || a.verify(c.Value, &ls) != nil || time.Now().After(ls.Expires) {
		a.OnErr(w, r, http.StatusBadRequest, "error.login_failed")
		return
	}
	a.clearCookie(w, loginCookie)
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		a.log.Warn("oidc provider returned an error", "error", e, "description", q.Get("error_description"))
		a.OnErr(w, r, http.StatusUnauthorized, "error.login_failed")
		return
	}
	if q.Get("state") != ls.State {
		a.OnErr(w, r, http.StatusBadRequest, "error.login_failed")
		return
	}
	tok, err := oc.Exchange(r.Context(), q.Get("code"), oauth2.VerifierOption(ls.Verifier))
	if err != nil {
		a.log.Warn("oidc code exchange failed", "err", err)
		a.OnErr(w, r, http.StatusUnauthorized, "error.login_failed")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := verifier.Verify(r.Context(), raw)
	if err != nil || idt.Nonce != ls.Nonce {
		a.log.Warn("id token rejected", "err", err)
		a.OnErr(w, r, http.StatusUnauthorized, "error.login_failed")
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		a.OnErr(w, r, http.StatusUnauthorized, "error.login_failed")
		return
	}
	if !hasGroup(claims[a.env.OIDCGroupsClaim], a.env.OIDCAdminGroup) {
		a.log.Info("sign-in refused: not in admin group", "sub", idt.Subject, "group", a.env.OIDCAdminGroup)
		a.OnErr(w, r, http.StatusForbidden, "error.forbidden")
		return
	}
	u := User{
		Subject: idt.Subject,
		Name:    firstString(claims, "name", "preferred_username", "email"),
		Email:   firstString(claims, "email"),
		Expires: time.Now().Add(sessionTTL),
		IDToken: raw,
	}
	v, err := a.sign(u)
	if err == nil && len(v) > maxSessionCookie {
		a.log.Warn("id token too large for the session cookie; logout will not end the provider session cleanly", "bytes", len(raw))
		u.IDToken = ""
		v, err = a.sign(u)
	}
	if err != nil {
		a.OnErr(w, r, http.StatusInternalServerError, "error.login_failed")
		return
	}
	a.setCookie(w, sessionCookie, v, sessionTTL)
	a.log.Info("admin signed in", "sub", u.Subject, "name", u.Name)
	http.Redirect(w, r, ls.Next, http.StatusFound)
}

// Logout clears the session and, when the provider offers RP-initiated
// logout, ends the provider session as well. Otherwise the next visit to a
// page that needs a session would sign the browser straight back in. The
// provider sends the browser back to /signed-out, which needs no session.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	var u User
	if c, err := r.Cookie(sessionCookie); err == nil {
		if a.verify(c.Value, &u) != nil {
			u = User{}
		}
	}
	a.clearCookie(w, sessionCookie)
	if to := a.providerLogoutURL(u.IDToken); to != "" {
		http.Redirect(w, r, to, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/signed-out", http.StatusFound)
}

// providerLogoutURL is the provider's end_session_endpoint for this client,
// or "" when there is none. The return address needs id_token_hint, so it is
// left out when the session holds no ID token.
func (a *Auth) providerLogoutURL(idToken string) string {
	if a.env.DevNoAuth {
		return ""
	}
	if _, _, err := a.client(); err != nil {
		return ""
	}
	a.mu.Lock()
	end := a.endSession
	a.mu.Unlock()
	u, err := url.Parse(end)
	if end == "" || err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", a.env.OIDCClientID)
	if idToken != "" {
		q.Set("id_token_hint", idToken)
		q.Set("post_logout_redirect_uri", a.env.AdminURL+"/signed-out")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Require lets signed-in admins through and sends everyone else to login.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.env.DevNoAuth {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, &User{Name: "dev", Email: a.env.DevEmail})))
			return
		}
		var u User
		c, err := r.Cookie(sessionCookie)
		if err != nil || a.verify(c.Value, &u) != nil || time.Now().After(u.Expires) {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, &u)))
	})
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
}

// sign encodes v as base64(json) + "." + base64(hmac).
func (a *Auth) sign(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + a.mac(payload), nil
}

func (a *Auth) verify(s string, v any) error {
	payload, sig, ok := strings.Cut(s, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(a.mac(payload))) {
		return errors.New("bad signature")
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (a *Auth) mac(payload string) string {
	h := hmac.New(sha256.New, a.env.SessionKey)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func randomString() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// safeNext only allows redirects back into the admin area.
func safeNext(next string) string {
	if strings.HasPrefix(next, "/admin") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/admin"
}

func hasGroup(claim any, group string) bool {
	switch v := claim.(type) {
	case string:
		return v == group
	case []any:
		for _, g := range v {
			if s, ok := g.(string); ok && (s == group || strings.TrimPrefix(s, "/") == group) {
				return true
			}
		}
	}
	return false
}

func firstString(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := claims[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
