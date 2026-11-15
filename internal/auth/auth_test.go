package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

func newAuth() *Auth {
	return New(config.Env{
		SessionKey:     []byte(strings.Repeat("k", 32)),
		AdminURL:       "https://status-admin.example.com",
		OIDCAdminGroup: "noroshi-admins",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestSignVerify(t *testing.T) {
	a := newAuth()
	s, err := a.sign(forSession, User{Subject: "u1", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	var u User
	if err := a.verify(forSession, s, &u); err != nil || u.Subject != "u1" {
		t.Fatalf("round trip failed: %v %+v", err, u)
	}
	payload, sig, _ := strings.Cut(s, ".")
	if err := a.verify(forSession, payload+"x."+sig, &u); err == nil {
		t.Fatal("tampered payload accepted")
	}
	other := newAuth()
	other.env.SessionKey = []byte(strings.Repeat("z", 32))
	if err := other.verify(forSession, s, &u); err == nil {
		t.Fatal("cookie signed with another key accepted")
	}
}

func TestRequire(t *testing.T) {
	a := newAuth()
	ok := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, UserFrom(r.Context()).Subject)
	}))

	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/checks/1", nil))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=%2Fadmin%2Fchecks%2F1") {
		t.Fatalf("anonymous request should go to login, got %d %s", rec.Code, rec.Header().Get("Location"))
	}

	expired, _ := a.sign(forSession, User{Subject: "u1", Expires: time.Now().Add(-time.Minute)})
	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: expired})
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("expired session should go to login, got %d", rec.Code)
	}

	valid, _ := a.sign(forSession, User{Subject: "u1", Expires: time.Now().Add(time.Hour)})
	req = httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: valid})
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "u1" {
		t.Fatalf("valid session refused: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHasGroup(t *testing.T) {
	cases := []struct {
		claim any
		want  bool
	}{
		{[]any{"a", "noroshi-admins"}, true},
		{[]any{"/noroshi-admins"}, true}, // Keycloak full group path
		{"noroshi-admins", true},
		{[]any{"admins"}, false},
		{nil, false},
		{[]any{1, 2}, false},
	}
	for _, c := range cases {
		if got := hasGroup(c.claim, "noroshi-admins"); got != c.want {
			t.Fatalf("hasGroup(%v) = %v", c.claim, got)
		}
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/admin/checks/3":   "/admin/checks/3",
		"https://evil.test": "/admin",
		"//evil.test/admin": "/admin",
		"":                  "/admin",
		"/p/home":           "/admin",
	} {
		if got := safeNext(in); got != want {
			t.Fatalf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogout(t *testing.T) {
	var endSession string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"issuer":"`+srv.URL+`","authorization_endpoint":"`+srv.URL+`/authorize","token_endpoint":"`+srv.URL+`/token","jwks_uri":"`+srv.URL+`/jwks","end_session_endpoint":"`+endSession+`"}`)
	}))
	defer srv.Close()

	// logout ends a session that holds idToken, which may be empty when it
	// did not fit in the cookie.
	logout := func(a *Auth, idToken string) *httptest.ResponseRecorder {
		s, _ := a.sign(forSession, User{Subject: "u1", Expires: time.Now().Add(time.Hour), IDToken: idToken})
		req := httptest.NewRequest("GET", "/logout?t="+a.mac(forLogout, s), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s})
		rec := httptest.NewRecorder()
		a.Logout(rec, req)
		return rec
	}
	newProviderAuth := func() *Auth {
		a := newAuth()
		a.env.OIDCIssuer, a.env.OIDCClientID = srv.URL, "noroshi"
		return a
	}

	endSession = srv.URL + "/end-session/"
	a := newProviderAuth()
	rec := logout(a, "tok")
	loc := rec.Header().Get("Location")
	for _, want := range []string{srv.URL + "/end-session/?", "id_token_hint=tok", "client_id=noroshi", "post_logout_redirect_uri=https%3A%2F%2Fstatus-admin.example.com%2Fsigned-out"} {
		if !strings.Contains(loc, want) {
			t.Fatalf("logout redirect %q lacks %q", loc, want)
		}
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("session cookie not cleared: %+v", c)
	}
	if loc := logout(a, "").Header().Get("Location"); strings.Contains(loc, "post_logout_redirect_uri") || !strings.Contains(loc, "client_id=noroshi") {
		t.Fatalf("logout without an ID token: %q", loc)
	}
	// Without a session nothing goes to the provider.
	rec = httptest.NewRecorder()
	a.Logout(rec, httptest.NewRequest("GET", "/logout", nil))
	if loc := rec.Header().Get("Location"); loc != "/signed-out" {
		t.Fatalf("logout without a session: %q", loc)
	}

	endSession = ""
	if loc := logout(newProviderAuth(), "tok").Header().Get("Location"); loc != "/signed-out" {
		t.Fatalf("provider without end_session_endpoint: %q", loc)
	}
}

func TestLoginCookieIsNotASession(t *testing.T) {
	a := newAuth()
	login, _ := a.sign(forLogin, loginState{State: "s", Expires: time.Now().Add(time.Hour)})
	ok := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: login})
	rec := httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("a login cookie passed as a session: %d", rec.Code)
	}
	// A session-signed value without a user is refused as well.
	empty, _ := a.sign(forSession, User{Expires: time.Now().Add(time.Hour)})
	req = httptest.NewRequest("GET", "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: empty})
	rec = httptest.NewRecorder()
	ok.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("a session without a subject passed: %d", rec.Code)
	}
}

func TestLogoutNeedsTheSessionsToken(t *testing.T) {
	a := newAuth()
	s, _ := a.sign(forSession, User{Subject: "u1", Expires: time.Now().Add(time.Hour)})
	var link string
	a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		link = UserFrom(r.Context()).LogoutURL()
	})).ServeHTTP(httptest.NewRecorder(), withSession(httptest.NewRequest("GET", "/admin", nil), s))
	if link != "/logout?t="+a.mac(forLogout, s) {
		t.Fatalf("logout link %q", link)
	}
	other, _ := a.sign(forSession, User{Subject: "u2", Expires: time.Now().Add(time.Hour)})
	for _, target := range []string{"/logout", "/logout?t=", "/logout?t=forged", "/logout?t=" + a.mac(forLogout, other)} {
		rec := httptest.NewRecorder()
		a.Logout(rec, withSession(httptest.NewRequest("GET", target, nil), s))
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin" || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s ended the session: %d %q", target, rec.Code, rec.Header().Get("Location"))
		}
	}
	rec := httptest.NewRecorder()
	a.Logout(rec, withSession(httptest.NewRequest("GET", link, nil), s))
	if rec.Header().Get("Location") != "/signed-out" || len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout with the token: %q %+v", rec.Header().Get("Location"), rec.Result().Cookies())
	}
}

func withSession(r *http.Request, v string) *http.Request {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
	return r
}
