package web

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/auth"
	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/metrics"
	"github.com/dor1a/noroshi/internal/store"
	"github.com/dor1a/noroshi/internal/uptime"
)

type fakeLeader bool

func (f fakeLeader) IsLeader() bool { return bool(f) }

func newTestServer(t *testing.T) *Server {
	t.Helper()
	env := config.Env{DevNoAuth: true, DefaultLocale: "ko"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(nil, auth.New(env, log), nil, fakeLeader(true), env, log)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func samplePage() *pageData {
	loc := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, loc)
	ok := uptime.Compute([]uptime.Event{{At: now.Add(-48 * time.Hour), Status: judge.Operational}},
		[]uptime.Interval{{Start: now.Add(-48 * time.Hour), End: now}}, loc, now, Window)
	down := uptime.Compute([]uptime.Event{{At: now.Add(-time.Hour), Status: judge.Major}},
		[]uptime.Interval{{Start: now.Add(-2 * time.Hour), End: now}}, loc, now, Window)
	web := componentData{GroupKey: "web", Key: "site", Name: i18n.Text{"ko": "웹사이트", "en": "Website"},
		Description: i18n.Text{"*": "main site"}, Status: judge.Operational, Summary: ok}
	db := componentData{GroupKey: "infra", Key: "db", Name: i18n.Text{"*": "DB"}, Status: judge.Major, Summary: down}
	return &pageData{
		Page:    store.PageRow{Key: "home", Title: i18n.Text{"ko": "상태", "en": "Status"}, Timezone: "Asia/Seoul"},
		Loc:     loc,
		Overall: judge.Major,
		Groups: []groupData{
			{Key: "web", Name: i18n.Text{"*": "Web"}, Components: []componentData{web}},
			{Key: "infra", Name: i18n.Text{"*": "Infra"}, Components: []componentData{db}},
		},
		Components: []componentData{web, db},
		UpdatedAt:  now,
	}
}

func render(t *testing.T, s *Server, name string, data any) string {
	t.Helper()
	var b bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b.String()
}

func TestPublicTemplates(t *testing.T) {
	s := newTestServer(t)
	for _, locale := range []string{"ko", "en"} {
		v := s.pageView(samplePage(), locale, "tokyonight", true, false)
		out := render(t, s, "page", v)
		for _, want := range []string{`data-theme="tokyonight"`, `class="verdict-box vt-major_outage"`, `class="vb-body"`, `class="vb-icon"`, `<svg viewBox="0 0 20 20"`, `class="dot s-major_outage"`, `class="ticks"`, "/p/home/live?lang=" + locale} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s page missing %q", locale, want)
			}
		}
		if got := strings.Count(out, "<i class="); got != 2*Window {
			t.Fatalf("%s page has %d ticks, want %d", locale, got, 2*Window)
		}
		render(t, s, "live", v)
	}
	if v := s.pageView(samplePage(), "ko", "", false, false); v.Groups[0].OK != 1 || v.Groups[1].OK != 0 || v.Groups[1].Total != 1 {
		t.Fatalf("group counts wrong: %+v", v.Groups)
	}
	items := []pageItem{{"home", "Home", judge.Operational, true}}
	render(t, s, "pages", struct {
		Locale, Theme, DocTitle string
		Pages                   []pageItem
	}{"ko", "", "x", items})
	out := render(t, s, "admin_pages", struct {
		adminBase
		Pages []pageItem
	}{adminBase{Locale: "ko", DocTitle: "admin", Section: "pages", Brand: "Example", Logo: "/admin/logo?v=1", UserName: "dev", Dev: true}, items})
	if !strings.Contains(out, `href="/pages" aria-current="page"`) || !strings.Contains(out, `href="/p/home?lang=ko"`) || !strings.Contains(out, `class="colophon"`) {
		t.Fatal("admin page list lacks the pages tab or the page link")
	}
	out = render(t, s, "signed_out", struct {
		Locale, Theme, DocTitle, Brand, Logo, Favicon string
	}{"ko", "", "x", "Example", "/admin/logo?v=1", "/admin/logo?v=1"})
	if !strings.Contains(out, "로그아웃했습니다") || !strings.Contains(out, `href="/admin"`) {
		t.Fatal("signed-out page lacks its message or the sign-in link")
	}
	s.SetFooter(false)
	if out := render(t, s, "pages", struct {
		Locale, Theme, DocTitle string
		Pages                   []pageItem
	}{"ko", "", "x", items}); strings.Contains(out, `class="colophon"`) {
		t.Fatal("footer shown with powered_by false")
	}
	s.SetFooter(true)
	render(t, s, "error", errorView{Locale: "en", DocTitle: "x", Message: "Page not found"})
}

func TestAdminTemplates(t *testing.T) {
	s := newTestServer(t)
	now := time.Now()
	ok, lat := false, 120
	exp := now.Add(10 * 24 * time.Hour)
	c := store.AdminCheck{
		ID: 7, PageKey: "home", PageTitle: i18n.Text{"*": "Home"}, ComponentKey: "db",
		ComponentName: i18n.Text{"*": "DB"}, Key: "port", Type: "tcp",
		Spec: config.Check{Type: "tcp", Address: "db:5432", WarnBefore: config.Duration{Duration: 30 * 24 * time.Hour}},
		State: &store.StateRow{
			State: judge.State{State: judge.Down, ConsecutiveFail: 3}, Since: now, LastAt: &now,
			LastOK: &ok, LastLatencyMS: &lat, LastDetail: "connection refused", CertExpiry: &exp,
		},
	}
	row := s.adminRow("ko", c)
	if row.StateKey != "down" || row.ResultClass != "t-bad" || row.ExpiryClass != "t-warn" {
		t.Fatalf("admin row: %+v", row)
	}
	base := adminBase{Locale: "ko", DocTitle: "admin", Section: "checks", Brand: "Example", Logo: "/admin/logo?v=1", UserName: "dev", Dev: true, ProberKey: "leader", ProberText: "prober", ProberLine: "prober"}
	out := render(t, s, "admin", struct {
		adminBase
		Rows []adminRow
	}{base, []adminRow{row, s.adminRow("en", store.AdminCheck{Key: "new", Type: "http"}), {StateKey: "slow", StateLabel: "Degraded"}}})
	if !strings.Contains(out, `<span class="cs cs-slow">`) {
		t.Fatal("slow row not marked")
	}
	if !strings.Contains(out, `class="cell-down"`) || !strings.Contains(out, `<path d="M6.8 6.8l6.4 6.4`) {
		t.Fatal("down row not marked")
	}
	spec := config.Check{Timeout: config.Duration{Duration: 10 * time.Second}}
	slow := spec
	slow.SlowAfter = config.Duration{Duration: 2 * time.Second}
	for _, c := range []struct {
		ms   int
		spec config.Check
		want string
	}{{300, spec, ""}, {5001, spec, "t-warn"}, {8000, spec, "t-bad"}, {2000, slow, ""}, {2001, slow, "t-warn"}, {9000, slow, "t-bad"}} {
		if got := latencyClass(c.ms, c.spec); got != c.want {
			t.Errorf("latencyClass(%d, slow_after %s) = %q, want %q", c.ms, c.spec.SlowAfter.Duration, got, c.want)
		}
	}
	if got, want := string(breakable("https://h.io/a?x=1&y=<2>")), "https://<wbr>h<wbr>.io/<wbr>a<wbr>?x=<wbr>1<wbr>&amp;y=<wbr>&lt;2&gt;"; got != want {
		t.Errorf("breakable = %s, want %s", got, want)
	}
	if row.Streak != "연속 실패 3회" {
		t.Errorf("streak = %q", row.Streak)
	}
	if a, b := gravatarURL(" Me@Example.com "), gravatarURL("me@example.com"); a != b || !strings.HasPrefix(a, "https://gravatar.com/avatar/") || len(a) != len("https://gravatar.com/avatar/?s=64&d=blank")+64 {
		t.Fatalf("gravatar: %s %s", a, b)
	}
	for _, want := range []string{`src="/admin/logo?v=1"`, `<span class="title">Example</span>`, `<span class="avatar" aria-hidden="true">D</span>`, `href="/admin" aria-current="page"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("admin masthead lacks %s", want)
		}
	}
	render(t, s, "admin_check", struct {
		adminBase
		Row                           adminRow
		Interval, Timeout, Thresholds string
		Results                       []resultView
	}{base, row, "1m0s", "10s", "3/2", []resultView{{At: "now", Class: "down", Label: "장애", Latency: "1 ms", Detail: "x"}}})
}

func TestListenerRoutes(t *testing.T) {
	env := config.Env{DevNoAuth: true, DefaultLocale: "en"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := New(nil, auth.New(env, log), metrics.New("test"), fakeLeader(true), env, log)
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string]http.Handler{"public": s.PublicHandler(), "admin": s.AdminHandler(), "ops": s.OpsHandler()}
	for _, tc := range []struct {
		listener, path string
		want           int
	}{
		{"public", "/metrics", http.StatusNotFound},
		{"admin", "/metrics", http.StatusNotFound},
		{"admin", "/readyz", http.StatusNotFound},
		{"admin", "/healthz", http.StatusOK},
		{"ops", "/metrics", http.StatusOK},
		{"ops", "/healthz", http.StatusOK},
		{"ops", "/admin", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		handlers[tc.listener].ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.listener, tc.path, rec.Code, tc.want)
		}
	}
}

func sampleIncidents(now time.Time) []store.Incident {
	resolvedAt := now.Add(-24 * time.Hour)
	edited := now.Add(-50 * time.Minute)
	return []store.Incident{
		{
			ID: 3, PageKey: "home", Title: "DB unreachable", Stage: store.StageIdentified, StartedAt: now.Add(-time.Hour),
			Components: []store.IncidentComponent{{ComponentID: 2, Key: "db", Name: i18n.Text{"*": "DB"}, Status: judge.Major}},
			Updates: []store.IncidentUpdate{
				{ID: 6, Stage: store.StageIdentified, Body: "Disk full.\nCleaning up.", At: now.Add(-30 * time.Minute), Author: "kim",
					Notifications: []store.Notification{{Notifier: "teams", OK: false, Attempts: 3, Detail: "HTTP 400"}}},
				{ID: 5, Stage: store.StageInvestigating, Body: "Looking into it.", At: now.Add(-time.Hour), Author: "kim",
					EditedAt: &edited, EditedBy: "park"},
			},
		},
		{
			ID: 2, PageKey: "home", Title: "Slow pages", Stage: store.StageResolved, StartedAt: now.Add(-26 * time.Hour), ResolvedAt: &resolvedAt,
			Updates: []store.IncidentUpdate{{ID: 4, Stage: store.StageResolved, Body: "Fixed.", At: resolvedAt, Author: "lee"}},
		},
	}
}

func TestIncidentTemplates(t *testing.T) {
	s := newTestServer(t)
	d := samplePage()
	d.Incidents = sampleIncidents(d.UpdatedAt)
	v := s.pageView(d, "en", "tokyonight", false, false)
	if len(v.Active) != 1 || len(v.Past) != 1 {
		t.Fatalf("active %d past %d", len(v.Active), len(v.Past))
	}
	out := render(t, s, "page", v)
	for _, want := range []string{`class="incident it-major_outage"`, "DB unreachable", "Identified", "/p/home/incidents/3", "Slow pages", "Disk full.\nCleaning up."} {
		if !strings.Contains(out, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	if !strings.Contains(out, `<span class="edited" title="Edited 2026-10-13 11:10 KST">Edited</span>`) {
		t.Fatal("public page does not mark the edited update")
	}
	if strings.Contains(out, `class="vb-body"`) {
		t.Fatal("verdict text repeats what the open incident says")
	}
	for _, leak := range []string{"HTTP 400", "kim", "teams", "park", "/updates/"} {
		if strings.Contains(out, leak) {
			t.Fatalf("public page leaks %q", leak)
		}
	}

	in := d.Incidents[0]
	render(t, s, "incident", incidentPage{Locale: "ko", DocTitle: "x", PageTitle: "Status", Home: "/", Inc: s.incidentView(in, "ko", d.Loc, false)})

	base := adminBase{Locale: "ko", DocTitle: "admin", Section: "incidents", UserName: "dev", Dev: true}
	render(t, s, "admin_incidents", struct {
		adminBase
		Open, Recent []incidentRow
		New          []newLink
		RecentDays   int
	}{base, []incidentRow{{ID: 3, Title: "DB unreachable", Tone: "major_outage", StageLabel: "원인 확인"}}, nil, []newLink{{"/admin/incidents/new?page=home", "새 인시던트"}}, 30})

	comps := []store.ComponentRow{{ID: 2, Key: "db", Name: i18n.Text{"*": "DB"}, GroupName: i18n.Text{"*": "Infra"}}}
	fv := s.formView("ko", "home", comps, map[int64]string{2: "major_outage"})
	fv.Form = incidentForm{Stage: store.StageInvestigating, Notify: true}
	fv.Create, fv.Action = true, "/admin/incidents"
	out = render(t, s, "admin_incident_new", struct {
		adminBase
		PageTitle string
		FormView  formView
	}{base, "Status", fv})
	if !strings.Contains(out, `<option value="major_outage" selected>`) || !strings.Contains(out, `name="page" value="home"`) {
		t.Fatal("new form does not keep the preselected status or page")
	}

	out = render(t, s, "admin_incident", struct {
		adminBase
		Inc       incidentView
		FormView  formView
		PublicURL string
	}{base, s.incidentView(in, "ko", time.UTC, true), fv, ""})
	if !strings.Contains(out, "teams 전송 실패(3회 시도): HTTP 400") || !strings.Contains(out, "kim") {
		t.Fatal("admin view must show delivery results and authors")
	}
	for _, want := range []string{`action="/admin/incidents/3/updates/6"`, `action="/admin/incidents/3/updates/5"`,
		`action="/admin/incidents/3/updates/5/delete"`, "에 수정 · park"} {
		if !strings.Contains(out, want) {
			t.Fatalf("admin view missing %q", want)
		}
	}
	if strings.Contains(out, "updates/6/delete") {
		t.Fatal("the latest update must not offer delete")
	}
}

func TestReadIncidentForm(t *testing.T) {
	comps := []store.ComponentRow{{ID: 2}, {ID: 9}}
	post := func(v url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/admin/incidents", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.ParseForm() //nolint:errcheck
		return r
	}
	f, st, _ := readIncidentForm(post(url.Values{
		"title": {" DB down "}, "stage": {"investigating"}, "body": {"line one\r\nline two"},
		"c2": {"major_outage"}, "c9": {""}, "c77": {"major_outage"}, "notify": {"1"},
	}), "en", comps, true, false)
	if len(f.Errors) > 0 || f.Title != "DB down" || f.Body != "line one\nline two" || !f.Notify {
		t.Fatalf("form: %+v", f)
	}
	if len(st) != 1 || st[2] != judge.Major {
		t.Fatalf("statuses must only include this page's components: %v", st)
	}

	f, _, _ = readIncidentForm(post(url.Values{"stage": {"panicking"}, "c2": {"on_fire"}}), "en", comps, true, false)
	if len(f.Errors) != 4 {
		t.Fatalf("want title, body, stage, and status errors, got %v", f.Errors)
	}

	f, st, _ = readIncidentForm(post(url.Values{"body": {"postmortem"}, "c2": {"on_fire"}}), "en", comps, false, true)
	if len(f.Errors) != 0 || len(st) != 0 {
		t.Fatalf("a note on a resolved incident ignores stage and components: %v %v", f.Errors, st)
	}
}

func TestAdminRefusesCrossOriginPosts(t *testing.T) {
	s := newTestServer(t)
	r := httptest.NewRequest(http.MethodPost, "/admin/incidents/1/delete", strings.NewReader("confirm=1"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	s.AdminHandler().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site post got %d", rec.Code)
	}
}

func TestPrivatePagesAndLogo(t *testing.T) {
	s := newTestServer(t)
	s.SetPages(map[string]PageMeta{
		"home":  {Logo: &Logo{Data: []byte("<svg/>"), ContentType: "image/svg+xml", Version: "abc"}},
		"inner": {Private: true, Logo: &Logo{Data: []byte("png"), ContentType: "image/png", Version: "def"}},
	})
	pages := []store.PageRow{{Key: "home"}, {Key: "inner"}}
	public := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := s.visible(public, pages); len(got) != 1 || got[0].Key != "home" {
		t.Fatalf("public listener must hide private pages: %+v", got)
	}
	var admin *http.Request
	adminView(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { admin = r })).ServeHTTP(httptest.NewRecorder(), public)
	if got := s.visible(admin, pages); len(got) != 2 {
		t.Fatalf("admin listener shows every page: %+v", got)
	}

	logo := func(listener http.Handler, key string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		listener.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/"+key+"/logo?v=x", nil))
		return rec
	}
	if rec := logo(s.PublicHandler(), "home"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("public logo: %d %v", rec.Code, rec.Header())
	}
	if rec := logo(s.PublicHandler(), "inner"); rec.Code != http.StatusNotFound {
		t.Fatalf("private logo on the public listener: %d", rec.Code)
	}
	if rec := logo(s.AdminHandler(), "inner"); rec.Code != http.StatusOK {
		t.Fatalf("private logo for a signed-in admin: %d", rec.Code)
	}

	v := s.pageView(samplePage(), "en", "", true, true)
	if v.Logo != "/p/home/logo?v=abc" || v.Favicon != v.Logo || v.Home != "/pages" {
		t.Fatalf("view: %+v", v)
	}
	if out := render(t, s, "page", v); !strings.Contains(out, `<link rel="icon" href="/p/home/logo?v=abc">`) || !strings.Contains(out, `class="logo"`) {
		t.Fatal("page must use the logo as favicon and in the masthead")
	}
}

func TestTickTooltip(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	d := uptime.Day{Date: time.Date(2026, 10, 13, 0, 0, 0, 0, loc), Up: 24*time.Hour - 90*time.Second, Down: 90 * time.Second}
	if got := tipHead("en", d) + " | " + tipDate("en", d) + " | " + tipNote("en", d); got != "99.90% uptime | Tue, Oct 13 | 2m down" {
		t.Fatalf("en: %q", got)
	}
	d.Down, d.Up = 83*time.Minute, 24*time.Hour-83*time.Minute
	if got := tipHead("ko", d) + " | " + tipDate("ko", d) + " | " + tipNote("ko", d); got != "가용률 94.24% | 10월 13일 (화) | 장애 1시간 23분" {
		t.Fatalf("ko: %q", got)
	}
	empty := uptime.Day{Date: d.Date, Worst: judge.NoData}
	if tipHead("ko", empty) != "데이터 없음" || tipNote("ko", empty) != "" {
		t.Fatalf("no data: %q %q", tipHead("ko", empty), tipNote("ko", empty))
	}

	// Maintenance is left out of the percentage but still shows.
	maint := uptime.Day{Date: d.Date, Up: 22 * time.Hour, Maint: 2*time.Hour + time.Minute, Worst: judge.Maintenance}
	if got := tipHead("en", maint) + " | " + tipNote("en", maint); got != "100.00% uptime | 2h 1m maintenance" {
		t.Fatalf("maintenance: %q", got)
	}
	if got := dayClass(maint); got != "d-ok has-maint" {
		t.Fatalf("maintenance day class %q", got)
	}
	maint.Down = 30 * time.Minute
	if got := tipNote("ko", maint); got != "장애 30분, 점검 2시간 1분" {
		t.Fatalf("down and maintenance: %q", got)
	}
	if got := dayClass(uptime.Day{Maint: time.Hour, Worst: judge.Maintenance}); got != "d-maint" {
		t.Fatalf("maintenance-only day class %q", got)
	}
}

func TestDefaultLogo(t *testing.T) {
	l := DefaultLogo()
	if l.ContentType != "image/svg+xml" || len(l.Data) == 0 || len(l.Version) != 10 {
		t.Fatalf("default logo: %s %d %q", l.ContentType, len(l.Data), l.Version)
	}
}

func TestAdminAPIAuth(t *testing.T) {
	s := newTestServer(t)
	s.SetAPITokens(map[string]string{"ops": strings.Repeat("a", 40), "ci": strings.Repeat("b", 40)})
	var seen string
	h := s.apiAuth(func(w http.ResponseWriter, r *http.Request) { seen = apiAuthor(r) })
	for _, tc := range []struct {
		header, want string
	}{
		{"", ""},
		{"Basic " + strings.Repeat("a", 40), ""},
		{"Bearer " + strings.Repeat("c", 40), ""},
		{"Bearer " + strings.Repeat("a", 40), "api:ops"},
		{"bearer " + strings.Repeat("b", 40), "api:ci"},
	} {
		seen = ""
		r := httptest.NewRequest(http.MethodGet, "/api/v1/admin/incidents", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if seen != tc.want {
			t.Fatalf("%q: author %q, want %q", tc.header, seen, tc.want)
		}
		if tc.want == "" && (rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "") {
			t.Fatalf("%q: got %d without a challenge", tc.header, rec.Code)
		}
	}

	admin := s.AdminHandler()
	for _, tc := range []struct {
		method, path, site string
		want               int
	}{
		{http.MethodGet, "/api/v1/admin/incidents", "", http.StatusUnauthorized},
		{http.MethodDelete, "/api/v1/admin/incidents/1", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/admin/nothing", "", http.StatusNotFound},
		{http.MethodPost, "/api/v1/admin/incidents", "cross-site", http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("a", 40))
		}
		rec := httptest.NewRecorder()
		admin.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: got %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
		if tc.want != http.StatusForbidden && !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s %s: not JSON", tc.method, tc.path)
		}
	}
}

func TestAPIStatusesAndJSON(t *testing.T) {
	comps := []store.ComponentRow{{ID: 2, Key: "db"}, {ID: 9, Key: "web"}}
	st, errs := apiStatuses(comps, map[string]judge.Status{"db": judge.Major, "web": judge.Maintenance})
	if len(errs) != 0 || st[2] != judge.Major || st[9] != judge.Maintenance {
		t.Fatalf("got %v %v", st, errs)
	}
	_, errs = apiStatuses(comps, map[string]judge.Status{"cache": judge.Major, "db": "on_fire", "web": judge.NoData})
	if len(errs) != 3 || !strings.Contains(errs[0], `unknown component "cache"`) {
		t.Fatalf("want three errors, got %v", errs)
	}

	decode := func(body string) error {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var in updateIn
		return decodeJSON(httptest.NewRecorder(), r, &in)
	}
	if err := decode(`{"body":"ok","notify":true}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"body":"x","title":"y"}`, `{"body":"x"} {}`, `not json`} {
		if decode(bad) == nil {
			t.Fatalf("%s should fail", bad)
		}
	}
}
