// Package web serves the public status pages and the admin pages on two
// separate listeners.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dor1a/noroshi/internal/auth"
	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/metrics"
	"github.com/dor1a/noroshi/internal/notify"
	"github.com/dor1a/noroshi/internal/store"
	"github.com/dor1a/noroshi/internal/uptime"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Window is the number of days shown in the uptime bars.
const Window = 90

// Leader tells the admin view whether this instance runs the checks.
type Leader interface{ IsLeader() bool }

// Server holds what the handlers need.
type Server struct {
	store    *store.Store
	auth     *auth.Auth
	metrics  *metrics.Metrics
	leader   Leader
	env      config.Env
	log      *slog.Logger
	tpl      *template.Template
	cacheTTL time.Duration

	mu    sync.Mutex
	cache map[string]cachedPage

	targets    map[string]notify.Target
	pageNotify map[string][]string
	sender     *notify.Sender
	bg         sync.WaitGroup

	pages map[string]PageMeta
}

// PageMeta is what the config file says about a page beyond its content.
type PageMeta struct {
	Private bool
	Logo    *Logo
}

// Logo is a page logo loaded at startup.
type Logo struct {
	Data        []byte
	ContentType string
	Version     string
}

// SetPages records page visibility and logos from the config file.
func (s *Server) SetPages(meta map[string]PageMeta) { s.pages = meta }

func (s *Server) private(key string) bool { return s.pages[key].Private }

func (s *Server) logoURL(key string) string {
	if l := s.pages[key].Logo; l != nil {
		return "/p/" + key + "/logo?v=" + l.Version
	}
	return ""
}

type viewKey struct{}

// adminView marks a request as coming through the admin listener, where
// private pages are visible.
func adminView(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), viewKey{}, true)))
	})
}

func isAdminView(r *http.Request) bool {
	v, _ := r.Context().Value(viewKey{}).(bool)
	return v
}

// visible drops private pages unless the request came through the admin listener.
func (s *Server) visible(r *http.Request, pages []store.PageRow) []store.PageRow {
	if isAdminView(r) {
		return pages
	}
	out := pages[:0:0]
	for _, p := range pages {
		if !s.private(p.Key) {
			out = append(out, p)
		}
	}
	return out
}

type cachedPage struct {
	data *pageData
	at   time.Time
}

// New parses templates and wires the auth error page.
func New(st *store.Store, a *auth.Auth, m *metrics.Metrics, leader Leader, env config.Env, log *slog.Logger) (*Server, error) {
	s := &Server{
		store: st, auth: a, metrics: m, leader: leader, env: env, log: log,
		cacheTTL: 30 * time.Second,
		cache:    map[string]cachedPage{},
		sender:   notify.NewSender(),
	}
	tpl, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.tpl = tpl
	a.OnErr = func(w http.ResponseWriter, r *http.Request, status int, key string) {
		s.renderError(w, r, status, i18n.T(s.locale(w, r), key))
	}
	return s, nil
}

// SetNotifiers configures the announcement channels and which pages use them.
func (s *Server) SetNotifiers(targets []notify.Target, pageNotify map[string][]string) {
	s.targets = map[string]notify.Target{}
	for _, t := range targets {
		s.targets[t.Key] = t
	}
	s.pageNotify = pageNotify
}

// Drain waits for announcements still being sent, until ctx ends.
func (s *Server) Drain(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		s.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// invalidate drops cached pages after an admin change.
func (s *Server) invalidate() {
	s.mu.Lock()
	s.cache = map[string]cachedPage{}
	s.mu.Unlock()
}

var funcs = template.FuncMap{
	"t": i18n.T,
	"tf": func(locale, key string, args ...any) string {
		return fmt.Sprintf(i18n.T(locale, key), args...)
	},
	"pct": pct,
	"fmtTime": func(t time.Time, loc *time.Location) string {
		return t.In(loc).Format("2006-01-02 15:04 MST")
	},
	"dayClass": dayClass,
	"incidentTable": func(locale string, rows []incidentRow) any {
		return struct {
			Locale string
			Rows   []incidentRow
		}{locale, rows}
	},
	"glyph":      glyph,
	"statusIcon": statusIcon,
	"favicon":    favicon,
	"asset":      asset,
	"fmtRange":   fmtRange,
	"stateGlyph": stateGlyph,
	"tipHead":    tipHead,
	"tipDate":    tipDate,
	"tipNote":    tipNote,
}

// pct prints a percentage with two decimals and never rounds a real outage
// up to 100.
func pct(p float64) string {
	s := strconv.FormatFloat(p, 'f', 2, 64)
	if p < 100 && strings.HasPrefix(s, "100") {
		return "99.99"
	}
	return s
}

// tipHead, tipDate, and tipNote fill the tooltip of one uptime tick:
// "99.95% uptime" in the tick's colour, "Mon, Oct 13", and "1m down".
func tipHead(locale string, d uptime.Day) string {
	switch {
	case d.HasData():
		return fmt.Sprintf(i18n.T(locale, "uptime.percent"), pct(d.Percent()))
	case d.Worst == judge.Maintenance:
		return i18n.T(locale, "status.under_maintenance")
	}
	return i18n.T(locale, "uptime.nodata")
}

func tipDate(locale string, d uptime.Day) string {
	if locale == "ko" {
		return fmt.Sprintf("%d월 %d일 (%s)", d.Date.Month(), d.Date.Day(), []string{"일", "월", "화", "수", "목", "금", "토"}[d.Date.Weekday()])
	}
	return d.Date.Format("Mon, Jan 2")
}

func tipNote(locale string, d uptime.Day) string {
	if d.Down <= 0 {
		return ""
	}
	return fmt.Sprintf(i18n.T(locale, "uptime.down"), fmtDuration(locale, d.Down))
}

// fmtDuration prints a downtime rounded up to the minute: "1h 23m" or "1시간 23분".
func fmtDuration(locale string, d time.Duration) string {
	mins := int((d + time.Minute - 1) / time.Minute)
	h, m := mins/60, mins%60
	if locale == "ko" {
		switch {
		case h == 0:
			return fmt.Sprintf("%d분", m)
		case m == 0:
			return fmt.Sprintf("%d시간", h)
		}
		return fmt.Sprintf("%d시간 %d분", h, m)
	}
	switch {
	case h == 0:
		return fmt.Sprintf("%dm", m)
	case m == 0:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// assetVersion changes whenever an embedded static file changes, so browsers
// do not keep an old stylesheet after an upgrade.
var assetVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error { //nolint:errcheck
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(path)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:10]
}()

func asset(name string) string { return "/static/" + name + "?v=" + assetVersion }

// favicon uses the view's Favicon field when it has one, and the bundled
// icon otherwise, so every template can share one head.
func favicon(v any) string {
	rv := reflect.Indirect(reflect.ValueOf(v))
	if rv.Kind() == reflect.Struct {
		if f := rv.FieldByName("Favicon"); f.IsValid() && f.Kind() == reflect.String && f.String() != "" {
			return f.String()
		}
	}
	return asset("favicon.svg")
}

func glyph(s judge.Status) string { return s.Glyph() }

// statusIcon is the filled badge for the overall verdict: a circle in the
// status colour with the symbol cut out in the page colour.
func statusIcon(s judge.Status) template.HTML {
	const open = `<svg viewBox="0 0 20 20" focusable="false">`
	disc := `<circle cx="10" cy="10" r="10" fill="currentColor"/>`
	mark := `stroke="var(--paper)" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" fill="none"`
	var symbol string
	switch s {
	case judge.Operational:
		symbol = `<path d="M5.6 10.4l3 3 5.8-6.4" ` + mark + `/>`
	case judge.Degraded, judge.Partial:
		symbol = `<path d="M10 5.4v5.4" ` + mark + `/><circle cx="10" cy="14.3" r="1.3" fill="var(--paper)"/>`
	case judge.Major:
		symbol = `<path d="M6.8 6.8l6.4 6.4M13.2 6.8l-6.4 6.4" ` + mark + `/>`
	case judge.Maintenance:
		symbol = `<path d="M6 10h8" ` + mark + `/>`
	default:
		disc = `<circle cx="10" cy="10" r="8.9" fill="none" stroke="currentColor" stroke-width="2.2"/>`
	}
	return template.HTML(open + disc + symbol + `</svg>`)
}

func stateGlyph(state string) string {
	switch state {
	case "up":
		return "○"
	case "down":
		return "×"
	case "slow":
		return "△"
	default:
		return "–"
	}
}

// fmtRange prints the uptime window, e.g. "Jul 16 – Oct 13" or "7월 16일 – 10월 13일".
func fmtRange(locale string, from, to time.Time) string {
	if locale == "ko" {
		return fmt.Sprintf("%d월 %d일 – %d월 %d일", from.Month(), from.Day(), to.Month(), to.Day())
	}
	return from.Format("Jan 2") + " – " + to.Format("Jan 2")
}

func dayClass(d uptime.Day) string {
	if !d.HasData() {
		if d.Worst == judge.Maintenance {
			return "d-maint"
		}
		return "d-none"
	}
	switch p := d.Percent(); {
	case p >= 99.9:
		return "d-ok"
	case p >= 99:
		return "d-minor"
	case p >= 95:
		return "d-warn"
	default:
		return "d-bad"
	}
}

// Themes are the colour sets in style.css.
var themes = map[string]bool{"tokyonight": true, "ink": true, "dancheong": true, "klein": true}

// theme picks the colour set: ?theme= (remembered in a cookie), then the
// cookie, then NOROSHI_THEME.
func (s *Server) theme(w http.ResponseWriter, r *http.Request) string {
	if v := r.URL.Query().Get("theme"); themes[v] {
		http.SetCookie(w, &http.Cookie{Name: "noroshi_theme", Value: v, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
		return v
	}
	if c, err := r.Cookie("noroshi_theme"); err == nil && themes[c.Value] {
		return c.Value
	}
	if themes[s.env.Theme] {
		return s.env.Theme
	}
	return "tokyonight"
}

// locale picks the UI language: ?lang=, then the cookie, then
// Accept-Language, then the configured default. ?lang= is remembered.
func (s *Server) locale(w http.ResponseWriter, r *http.Request) string {
	if l := i18n.Normalize(r.URL.Query().Get("lang")); l != "" {
		http.SetCookie(w, &http.Cookie{Name: "noroshi_lang", Value: l, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
		return l
	}
	if c, err := r.Cookie("noroshi_lang"); err == nil {
		if l := i18n.Normalize(c.Value); l != "" {
			return l
		}
	}
	if l := i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")); l != "" {
		return l
	}
	return s.env.DefaultLocale
}

func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		s.log.Error("render failed", "template", name, "err", err)
	}
}

type errorView struct {
	Locale, Theme, DocTitle, Message string
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, status, "error", errorView{Locale: s.locale(w, r), Theme: s.theme(w, r), DocTitle: msg, Message: msg})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "path", r.URL.Path, "err", err)
	s.renderError(w, r, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
}

func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	fsrv := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		http.StripPrefix("/static/", fsrv).ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("ok\n")) //nolint:errcheck
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		http.Error(w, "database unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n")) //nolint:errcheck
}

// PublicHandler serves status pages. It needs no authentication.
func (s *Server) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /p/{key}", s.handlePage)
	mux.HandleFunc("GET /p/{key}/live", s.handleLive)
	mux.HandleFunc("GET /p/{key}/incidents/{id}", s.handlePublicIncident)
	mux.HandleFunc("GET /p/{key}/logo", s.handleLogo)
	mux.HandleFunc("GET /api/v1/pages/{key}", s.handleAPI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, r, http.StatusNotFound, i18n.T(s.locale(w, r), "page.notfound"))
	})
	return securityHeaders(mux)
}

// AdminHandler serves login and the admin pages. Everything except the
// login flow and static files requires a session, so the listener can sit
// behind a public hostname. Cross-origin form posts are refused.
func (s *Server) AdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", staticHandler())
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /login", s.auth.Login)
	mux.HandleFunc("GET /oauth/callback", s.auth.Callback)
	mux.HandleFunc("GET /logout", s.auth.Logout)
	mux.Handle("GET /admin", s.auth.Require(http.HandlerFunc(s.handleAdmin)))
	mux.Handle("GET /admin/checks/{id}", s.auth.Require(http.HandlerFunc(s.handleAdminCheck)))
	mux.Handle("GET /admin/incidents", s.auth.Require(http.HandlerFunc(s.handleIncidents)))
	mux.Handle("GET /admin/incidents/new", s.auth.Require(http.HandlerFunc(s.handleIncidentNew)))
	mux.Handle("POST /admin/incidents", s.auth.Require(http.HandlerFunc(s.handleIncidentCreate)))
	mux.Handle("GET /admin/incidents/{id}", s.auth.Require(http.HandlerFunc(s.handleIncident)))
	mux.Handle("POST /admin/incidents/{id}/updates", s.auth.Require(http.HandlerFunc(s.handleIncidentUpdate)))
	mux.Handle("POST /admin/incidents/{id}/delete", s.auth.Require(http.HandlerFunc(s.handleIncidentDelete)))
	// Every page, private ones included, for signed-in admins.
	viewer := func(h http.HandlerFunc) http.Handler { return s.auth.Require(adminView(h)) }
	mux.Handle("GET /pages", viewer(s.handleIndex))
	mux.Handle("GET /p/{key}", viewer(s.handlePage))
	mux.Handle("GET /p/{key}/live", viewer(s.handleLive))
	mux.Handle("GET /p/{key}/incidents/{id}", viewer(s.handlePublicIncident))
	mux.Handle("GET /p/{key}/logo", viewer(s.handleLogo))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.renderError(w, r, http.StatusNotFound, i18n.T(s.locale(w, r), "page.notfound"))
	})
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

// OpsHandler serves probes and metrics for the platform. Metrics name every
// check target, so keep this listener off public routes.
func (s *Server) OpsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("GET /metrics", s.metrics.Handler())
	return mux
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}

// ---- public pages ----

type pageData struct {
	Page       store.PageRow
	Loc        *time.Location
	Overall    judge.Status
	Groups     []groupData
	UpdatedAt  time.Time
	Components []componentData // flat, for the API
	Incidents  []store.Incident
}

type groupData struct {
	Key        string
	Name       i18n.Text
	Components []componentData
}

type componentData struct {
	ID          int64
	GroupKey    string
	Key         string
	Name        i18n.Text
	Description i18n.Text
	Status      judge.Status
	Summary     uptime.Summary
}

// loadPage builds (or reuses) the data behind one page.
func (s *Server) loadPage(ctx context.Context, p store.PageRow) (*pageData, error) {
	s.mu.Lock()
	if c, ok := s.cache[p.Key]; ok && time.Since(c.at) < s.cacheTTL {
		s.mu.Unlock()
		return c.data, nil
	}
	s.mu.Unlock()

	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	now := time.Now()
	local := now.In(loc)
	from := time.Date(local.Year(), local.Month(), local.Day()-(Window-1), 0, 0, 0, 0, loc)

	comps, err := s.store.PageComponents(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, len(comps))
	for i, c := range comps {
		ids[i] = c.ID
	}
	events, err := s.store.StatusEvents(ctx, ids, from)
	if err != nil {
		return nil, err
	}
	cov, err := s.store.Coverage(ctx, from)
	if err != nil {
		return nil, err
	}
	overrides, err := s.store.Overrides(ctx, ids, from)
	if err != nil {
		return nil, err
	}
	incidents, err := s.store.PageIncidents(ctx, p.ID, now.AddDate(0, 0, -PastDays))
	if err != nil {
		return nil, err
	}

	d := &pageData{Page: p, Loc: loc, UpdatedAt: now, Incidents: incidents}
	var statuses []judge.Status
	for _, c := range comps {
		evs := uptime.Apply(events[c.ID], overrides[c.ID])
		status := judge.NoData
		if len(evs) > 0 {
			status = evs[len(evs)-1].Status
		}
		cd := componentData{
			ID: c.ID, GroupKey: c.GroupKey, Key: c.Key, Name: c.Name, Description: c.Description,
			Status: status, Summary: uptime.Compute(evs, cov, loc, now, Window),
		}
		statuses = append(statuses, status)
		d.Components = append(d.Components, cd)
		if n := len(d.Groups); n == 0 || d.Groups[n-1].Key != c.GroupKey {
			d.Groups = append(d.Groups, groupData{Key: c.GroupKey, Name: c.GroupName})
		}
		g := &d.Groups[len(d.Groups)-1]
		g.Components = append(g.Components, cd)
	}
	d.Overall = overall(statuses)

	s.mu.Lock()
	s.cache[p.Key] = cachedPage{data: d, at: now}
	s.mu.Unlock()
	return d, nil
}

// overall is the page banner status. A page with some components still
// waiting for data shows the worst known status rather than "no data".
func overall(statuses []judge.Status) judge.Status {
	if len(statuses) == 0 {
		return judge.Operational
	}
	return judge.Worst(statuses)
}

func (s *Server) findPage(r *http.Request, key string) (*store.PageRow, []store.PageRow, error) {
	pages, err := s.store.Pages(r.Context())
	if err != nil {
		return nil, nil, err
	}
	pages = s.visible(r, pages)
	for i := range pages {
		if pages[i].Key == key {
			return &pages[i], pages, nil
		}
	}
	return nil, pages, nil
}

type pageView struct {
	Locale    string
	Theme     string
	DocTitle  string
	Title     string
	Logo      string
	Favicon   string
	Home      string
	Private   bool
	Multi     bool
	LivePath  string
	Window    int
	Overall   judge.Status
	Groups    []groupView
	UpdatedAt time.Time
	Loc       *time.Location
	From, To  time.Time
	Active    []incidentView
	Past      []incidentView
	PastDays  int
}

type groupView struct {
	Name       string
	OK         int
	Total      int
	Components []componentView
}

type componentView struct {
	Name        string
	Description string
	Status      judge.Status
	Days        []uptime.Day
	HasData     bool
	Percent     float64
}

func (s *Server) pageView(d *pageData, locale, theme string, multi, admin bool) pageView {
	title := d.Page.Title.Get(locale)
	v := pageView{
		Locale: locale, Theme: theme, DocTitle: title, Title: title, Multi: multi,
		Logo: s.logoURL(d.Page.Key), Home: "/", Private: s.private(d.Page.Key),
		LivePath: "/p/" + d.Page.Key + "/live?lang=" + locale,
		Window:   Window, Overall: d.Overall, UpdatedAt: d.UpdatedAt, Loc: d.Loc,
	}
	today := d.UpdatedAt.In(d.Loc)
	v.To = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, d.Loc)
	v.From = v.To.AddDate(0, 0, -(Window - 1))
	v.Favicon = v.Logo
	if admin {
		v.Home = "/pages"
	}
	v.PastDays = PastDays
	for _, in := range d.Incidents {
		iv := s.incidentView(in, locale, d.Loc, false)
		if in.Open() {
			v.Active = append(v.Active, iv)
		} else {
			v.Past = append(v.Past, iv)
		}
	}
	for _, g := range d.Groups {
		gv := groupView{Name: g.Name.Get(locale), Total: len(g.Components)}
		for _, c := range g.Components {
			if c.Status == judge.Operational {
				gv.OK++
			}
			gv.Components = append(gv.Components, componentView{
				Name: c.Name.Get(locale), Description: c.Description.Get(locale), Status: c.Status,
				Days: c.Summary.Days, HasData: c.Summary.HasData(), Percent: c.Summary.Percent(),
			})
		}
		v.Groups = append(v.Groups, gv)
	}
	return v
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	locale := s.locale(w, r)
	pages, err := s.store.Pages(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pages = s.visible(r, pages)
	admin := isAdminView(r)
	if len(pages) == 1 && !admin {
		d, err := s.loadPage(r.Context(), pages[0])
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.render(w, http.StatusOK, "page", s.pageView(d, locale, s.theme(w, r), false, false))
		return
	}
	type item struct {
		Key, Title string
		Status     judge.Status
		Private    bool
	}
	view := struct {
		Locale, Theme, DocTitle string
		Admin                   bool
		Pages                   []item
	}{Locale: locale, Theme: s.theme(w, r), DocTitle: i18n.T(locale, "page.list"), Admin: admin}
	for _, p := range pages {
		d, err := s.loadPage(r.Context(), p)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		view.Pages = append(view.Pages, item{Key: p.Key, Title: p.Title.Get(locale), Status: d.Overall, Private: s.private(p.Key)})
	}
	s.render(w, http.StatusOK, "pages", view)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	locale := s.locale(w, r)
	p, all, err := s.findPage(r, r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		s.renderError(w, r, http.StatusNotFound, i18n.T(locale, "page.notfound"))
		return
	}
	d, err := s.loadPage(r.Context(), *p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "page", s.pageView(d, locale, s.theme(w, r), len(all) > 1 || isAdminView(r), isAdminView(r)))
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	locale := s.locale(w, r)
	p, all, err := s.findPage(r, r.PathValue("key"))
	if err != nil || p == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	d, err := s.loadPage(r.Context(), *p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, http.StatusOK, "live", s.pageView(d, locale, s.theme(w, r), len(all) > 1 || isAdminView(r), isAdminView(r)))
}

type apiIncident struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Stage     string    `json:"stage"`
	StartedAt time.Time `json:"started_at"`
	URL       string    `json:"url"`
}

type apiComponent struct {
	Group     string       `json:"group"`
	Key       string       `json:"key"`
	Name      string       `json:"name"`
	Status    judge.Status `json:"status"`
	Uptime90d *float64     `json:"uptime_90d"`
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	locale := s.locale(w, r)
	p, _, err := s.findPage(r, r.PathValue("key"))
	if err != nil || p == nil {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	d, err := s.loadPage(r.Context(), *p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := struct {
		Page       string         `json:"page"`
		Title      string         `json:"title"`
		Status     judge.Status   `json:"status"`
		UpdatedAt  time.Time      `json:"updated_at"`
		Components []apiComponent `json:"components"`
		Incidents  []apiIncident  `json:"incidents"`
	}{Page: p.Key, Title: p.Title.Get(locale), Status: d.Overall, UpdatedAt: d.UpdatedAt, Incidents: []apiIncident{}}
	for _, in := range d.Incidents {
		if in.Open() {
			out.Incidents = append(out.Incidents, apiIncident{
				ID: in.ID, Title: in.Title, Stage: in.Stage, StartedAt: in.StartedAt,
				URL: s.env.BaseURL + incidentPath(in.PageKey, in.ID),
			})
		}
	}
	for _, c := range d.Components {
		ac := apiComponent{Group: c.GroupKey, Key: c.Key, Name: c.Name.Get(locale), Status: c.Status}
		if c.Summary.HasData() {
			v := c.Summary.Percent()
			ac.Uptime90d = &v
		}
		out.Components = append(out.Components, ac)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// ---- admin ----

type adminRow struct {
	ID          int64
	Page        string
	PageKey     string
	Component   string
	Key         string
	Type        string
	Target      string
	StateKey    string
	StateLabel  string
	Slow        bool
	SlowLabel   string
	Streak      string
	Since       string
	LastRun     string
	Latency     string
	Result      string
	ResultClass string
	Expiry      string
	ExpiryClass string
}

type adminBase struct {
	Locale     string
	Theme      string
	DocTitle   string
	Section    string
	UserName   string
	Dev        bool
	ProberLine string
}

func (s *Server) adminBase(w http.ResponseWriter, r *http.Request, section string) (adminBase, error) {
	locale := s.locale(w, r)
	b := adminBase{Locale: locale, Theme: s.theme(w, r), DocTitle: i18n.T(locale, "admin.title"), Section: section, Dev: s.auth.DevMode()}
	if u := auth.UserFrom(r.Context()); u != nil {
		b.UserName = u.Name
	}
	sess, err := s.store.LastSession(r.Context())
	if err != nil {
		return b, err
	}
	prober := i18n.T(locale, "admin.prober") + ": "
	switch {
	case s.leader.IsLeader():
		prober += i18n.T(locale, "admin.this_leader")
	case sess != nil && time.Since(sess.LastSeenAt) < 2*time.Minute:
		prober += i18n.T(locale, "admin.other_leader")
	case sess == nil:
		prober += i18n.T(locale, "admin.prober_none")
	default:
		prober += i18n.T(locale, "admin.prober_stopped")
	}
	if sess != nil {
		prober += fmt.Sprintf(". %s %s (%s)", i18n.T(locale, "admin.prober_seen"),
			sess.LastSeenAt.Local().Format("2006-01-02 15:04:05"), sess.Instance)
	}
	b.ProberLine = prober
	return b, nil
}

func (s *Server) adminRow(locale string, c store.AdminCheck) adminRow {
	row := adminRow{
		ID: c.ID, Page: c.PageTitle.Get(locale), PageKey: c.PageKey, Component: c.ComponentName.Get(locale),
		Key: c.Key, Type: c.Type, Target: c.Spec.Target(), StateKey: string(judge.Unknown),
		Since: "-", LastRun: "-", Latency: "-", Result: "-", Expiry: "-",
		StateLabel: i18n.T(locale, "state.unknown"), SlowLabel: i18n.T(locale, "state.slow"),
	}
	st := c.State
	if st == nil {
		return row
	}
	row.StateKey = string(st.State.State)
	row.StateLabel = i18n.T(locale, "state."+row.StateKey)
	row.Slow = st.Degraded
	row.Streak = fmt.Sprintf(i18n.T(locale, "admin.streak_fmt"), st.ConsecutiveOK, st.ConsecutiveFail)
	if !st.Since.IsZero() {
		row.Since = ago(st.Since)
	}
	if st.LastAt != nil {
		row.LastRun = ago(*st.LastAt)
	}
	if st.LastLatencyMS != nil {
		row.Latency = fmt.Sprintf("%d ms", *st.LastLatencyMS)
	}
	if st.LastDetail != "" {
		row.Result = st.LastDetail
	}
	if st.LastOK != nil {
		if *st.LastOK {
			row.ResultClass = "t-ok"
		} else {
			row.ResultClass = "t-bad"
		}
	}
	var parts []string
	worst := ""
	for _, e := range []struct {
		label string
		at    *time.Time
	}{{i18n.T(locale, "admin.cert"), st.CertExpiry}, {i18n.T(locale, "admin.domain"), st.DomainExpiry}} {
		if e.at == nil {
			continue
		}
		left := int(time.Until(*e.at).Hours() / 24)
		var txt string
		if left < 0 {
			txt = i18n.T(locale, "admin.expired")
			worst = "t-bad"
		} else {
			txt = fmt.Sprintf(i18n.T(locale, "admin.days_left"), left)
			if time.Until(*e.at) < c.Spec.WarnBefore.Duration && worst == "" {
				worst = "t-warn"
			}
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", e.label, e.at.Format("2006-01-02"), txt))
	}
	if len(parts) > 0 {
		row.Expiry = strings.Join(parts, ", ")
		row.ExpiryClass = worst
	}
	return row
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "checks")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	checks, err := s.store.AdminChecks(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view := struct {
		adminBase
		Rows []adminRow
	}{adminBase: base}
	for _, c := range checks {
		view.Rows = append(view.Rows, s.adminRow(base.Locale, c))
	}
	// Problems first, otherwise keep config order.
	sort.SliceStable(view.Rows, func(i, j int) bool {
		return rank(view.Rows[i].StateKey) < rank(view.Rows[j].StateKey)
	})
	s.render(w, http.StatusOK, "admin", view)
}

func rank(state string) int {
	switch state {
	case string(judge.Down):
		return 0
	case string(judge.Unknown):
		return 1
	default:
		return 2
	}
}

type resultView struct {
	At, Class, Label, Latency, Detail string
}

func (s *Server) handleAdminCheck(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "checks")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, i18n.T(base.Locale, "page.notfound"))
		return
	}
	c, err := s.store.AdminCheck(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if c == nil {
		s.renderError(w, r, http.StatusNotFound, i18n.T(base.Locale, "page.notfound"))
		return
	}
	results, err := s.store.RecentResults(r.Context(), id, 50)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view := struct {
		adminBase
		Row        adminRow
		Interval   string
		Timeout    string
		Thresholds string
		Results    []resultView
	}{
		adminBase:  base,
		Row:        s.adminRow(base.Locale, *c),
		Interval:   c.Spec.Interval.String(),
		Timeout:    c.Spec.Timeout.String(),
		Thresholds: fmt.Sprintf(i18n.T(base.Locale, "admin.thresh_fmt"), c.Spec.FailThreshold, c.Spec.RecoverThreshold),
	}
	for _, res := range results {
		rv := resultView{
			At:      res.At.Local().Format("2006-01-02 15:04:05"),
			Latency: fmt.Sprintf("%d ms", res.LatencyMS),
			Detail:  res.Detail,
		}
		switch {
		case res.Skipped:
			rv.Class, rv.Label = "unknown", i18n.T(base.Locale, "admin.skipped")
		case res.OK && res.Slow:
			rv.Class, rv.Label = "slow", i18n.T(base.Locale, "state.slow")
		case res.OK:
			rv.Class, rv.Label = "up", i18n.T(base.Locale, "state.up")
		default:
			rv.Class, rv.Label = "down", i18n.T(base.Locale, "state.down")
		}
		view.Results = append(view.Results, rv)
	}
	s.render(w, http.StatusOK, "admin_check", view)
}
