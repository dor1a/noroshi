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
		v := s.pageView(samplePage(), locale, "tokyonight", true)
		out := render(t, s, "page", v)
		for _, want := range []string{`data-theme="tokyonight"`, `class="verdict-box vt-major_outage"`, "×", `class="ticks"`, "/p/home/live?lang=" + locale} {
			if !strings.Contains(out, want) {
				t.Fatalf("%s page missing %q", locale, want)
			}
		}
		if got := strings.Count(out, "<i class="); got != 2*Window {
			t.Fatalf("%s page has %d ticks, want %d", locale, got, 2*Window)
		}
		render(t, s, "live", v)
	}
	if v := s.pageView(samplePage(), "ko", "", false); v.Groups[0].OK != 1 || v.Groups[1].OK != 0 || v.Groups[1].Total != 1 {
		t.Fatalf("group counts wrong: %+v", v.Groups)
	}
	render(t, s, "pages", struct {
		Locale, Theme, DocTitle string
		Pages                   []struct {
			Key, Title string
			Status     judge.Status
		}
	}{Locale: "ko", DocTitle: "x", Pages: []struct {
		Key, Title string
		Status     judge.Status
	}{{"home", "Home", judge.Operational}}})
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
	base := adminBase{Locale: "ko", DocTitle: "admin", UserName: "dev", Dev: true, ProberLine: "prober"}
	out := render(t, s, "admin", struct {
		adminBase
		Rows []adminRow
	}{base, []adminRow{row, s.adminRow("en", store.AdminCheck{Key: "new", Type: "http"})}})
	if !strings.Contains(out, `class="cell-down"`) || !strings.Contains(out, "×") {
		t.Fatal("down row not marked")
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
	return []store.Incident{
		{
			ID: 3, PageKey: "home", Title: "DB unreachable", Stage: store.StageIdentified, StartedAt: now.Add(-time.Hour),
			Components: []store.IncidentComponent{{ComponentID: 2, Key: "db", Name: i18n.Text{"*": "DB"}, Status: judge.Major}},
			Updates: []store.IncidentUpdate{
				{ID: 6, Stage: store.StageIdentified, Body: "Disk full.\nCleaning up.", At: now.Add(-30 * time.Minute), Author: "kim",
					Notifications: []store.Notification{{Notifier: "teams", OK: false, Attempts: 3, Detail: "HTTP 400"}}},
				{ID: 5, Stage: store.StageInvestigating, Body: "Looking into it.", At: now.Add(-time.Hour), Author: "kim"},
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
	v := s.pageView(d, "en", "tokyonight", false)
	if len(v.Active) != 1 || len(v.Past) != 1 {
		t.Fatalf("active %d past %d", len(v.Active), len(v.Past))
	}
	out := render(t, s, "page", v)
	for _, want := range []string{`class="incident it-major_outage"`, "DB unreachable", "Identified", "/p/home/incidents/3", "Slow pages", "Disk full.\nCleaning up."} {
		if !strings.Contains(out, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	for _, leak := range []string{"HTTP 400", "kim", "teams"} {
		if strings.Contains(out, leak) {
			t.Fatalf("public page leaks %q", leak)
		}
	}

	in := d.Incidents[0]
	render(t, s, "incident", struct {
		Locale, Theme, DocTitle, PageTitle, Home string
		Inc                                      incidentView
	}{"ko", "", "x", "Status", "/", s.incidentView(in, "ko", d.Loc, false)})

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
