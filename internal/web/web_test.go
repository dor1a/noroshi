package web

import (
	"bytes"
	"fmt"
	"html/template"
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
	webRow := s.adminRow("en", store.AdminCheck{Key: "new", Type: "http", PageKey: "home", ComponentKey: "web"})
	pingRow := webRow
	pingRow.Key, pingRow.ComponentKey, pingRow.StateKey = "ping", "db", "up"
	rows := []adminRow{webRow, pingRow, row, {StateKey: "slow", StateLabel: "Degraded", PageKey: "status", Page: "Status", ComponentKey: "x"}}
	secs := adminSections(rows, func(key string) bool { return key == "home" })
	if len(secs) != 2 || secs[0].Key != "home" || !secs[0].Private || secs[0].Down != 1 || secs[1].Key != "status" || secs[1].Private {
		t.Fatalf("sections: %+v", secs)
	}
	// The component with a down check comes first and keeps its checks in order.
	if c := secs[0].Components; len(c) != 2 || !c[0].Down || len(c[0].Rows) != 2 || c[0].Rows[0].Key != "ping" || c[0].Rows[1].StateKey != "down" || c[1].Rows[0].Key != "new" {
		t.Fatalf("components: %+v", c)
	}
	if webRow.TypeLabel != "HTTP request" || typeLabel("ko", "tcp") != "TCP 연결" || typeLabel("ko", "future") != "future" {
		t.Fatalf("type labels: %q", webRow.TypeLabel)
	}
	recent := []outageView{{Page: "Home", Component: "DB", Status: "주요 장애", StatusKey: judge.Major, Start: "2026-10-30 22:36:21", Ongoing: true, Duration: "11분", Cause: "port: timeout", CauseURL: "/admin/checks/7?from=x", RecordURL: "/admin/incidents/new?page=home"}}
	out := render(t, s, "admin", struct {
		adminBase
		Recent   []outageView
		Sections []adminSection
	}{base, recent, secs})
	if !strings.Contains(out, "최근 24시간 장애") || !strings.Contains(out, `href="/admin/incidents/new?page=home">인시던트로 기록`) || !strings.Contains(out, "port: timeout") {
		t.Fatal("recent outages not rendered")
	}
	if !strings.Contains(out, `href="/p/home" title="페이지 보기">/p/home ↗</a>`) || !strings.Contains(out, "비공개") || !strings.Contains(out, ">공개<") || !strings.Contains(out, `rowspan="2"`) {
		t.Fatal("page sections not rendered")
	}
	if !strings.Contains(out, `<span class="cs cs-slow">`) {
		t.Fatal("slow row not marked")
	}
	if !strings.Contains(out, `class="comp-cell cell-down"`) || !strings.Contains(out, `<path d="M6.8 6.8l6.4 6.4`) {
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
	start := time.Date(2026, 10, 30, 22, 34, 47, 0, time.Local)
	back := start.Add(13 * time.Minute)
	fails := failureViews("ko", []store.FailureRun{
		{Start: start.Add(2 * time.Hour), Count: 1, Detail: "timeout"},
		{Start: start, End: &back, Count: 13, Detail: "timeout"},
	}, 3, start.Add(2*time.Hour+90*time.Second))
	if !fails[0].Ongoing || fails[0].Judged || fails[0].Duration != "2분" || fails[1].Ongoing || !fails[1].Judged || fails[1].Duration != "13분" || fails[1].End != "2026-10-30 22:47:47" {
		t.Fatalf("failures: %+v", fails)
	}
	out = render(t, s, "admin_check", struct {
		adminBase
		Row                           adminRow
		Interval, Timeout, Thresholds string
		Results                       []resultView
		Failures                      []failureView
		Range                         string
		RangeBad                      bool
		Presets                       []presetView
		From, To, Min, Max            string
		FailedOnly                    bool
		Total                         int
		Chart                         template.HTML
	}{base, row, "1m0s", "10s", "3/2", []resultView{{At: "now", Class: "down", Label: "장애", Latency: "1 ms", Detail: "x"}}, fails,
		"최근 14일", false, []presetView{{Label: "14일", Href: "?range=14d", Active: true}}, "2026-10-17T00:00", "2026-10-31T00:00", "2026-10-17T00:00", "2026-10-31T00:00", true, 360, template.HTML(`<svg class="lat-chart"></svg>`)})
	for _, want := range []string{"응답 시간 (최근 14일)", `<svg class="lat-chart">`, "실패 이력 (최근 14일)", "실패 중", "장애 판정", "실행 결과 (최근 14일)", "360개 중 최신 1개", `class="chip active"`, `name="failed" value="1" checked`} {
		if !strings.Contains(out, want) {
			t.Fatalf("check page lacks %s", want)
		}
	}
}

func TestParseRange(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 10, 31, 0, 30, 0, 0, loc)
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }
	for _, c := range []struct {
		query, preset string
		from, to      time.Time
		bad           bool
	}{
		{"", "14d", now.Add(-14 * 24 * time.Hour), now, false},
		{"range=6h", "6h", now.Add(-6 * time.Hour), now, false},
		{"range=1y", "14d", now.Add(-14 * 24 * time.Hour), now, true},
		{"from=2026-10-30T22:00&to=2026-10-30T23:00", "", time.Date(2026, 10, 30, 22, 0, 0, 0, loc), time.Date(2026, 10, 30, 23, 0, 0, 0, loc), false},
		{"from=2026-10-30T22:00", "", time.Date(2026, 10, 30, 22, 0, 0, 0, loc), now, false},
		{"from=2026-09-01T00:00&to=2027-01-01T00:00", "", now.Add(-14 * 24 * time.Hour), now, false},
		{"from=2026-10-30T23:00&to=2026-10-30T22:00", "14d", now.Add(-14 * 24 * time.Hour), now, true},
		{"from=yesterday", "14d", now.Add(-14 * 24 * time.Hour), now, true},
	} {
		r := parseRange(q(c.query), now, loc, 14*24*time.Hour)
		if r.Preset != c.preset || !r.From.Equal(c.from) || !r.To.Equal(c.to) || r.Bad != c.bad {
			t.Errorf("%q: %+v", c.query, r)
		}
	}
	// A longer retention offers longer presets; one that is not a preset
	// gets an "all" preset of its own.
	keys := func(d time.Duration) (out []string) {
		for _, p := range rangePresets(d) {
			out = append(out, p.Key)
		}
		return out
	}
	if got := strings.Join(keys(14*24*time.Hour), ","); got != "1h,6h,24h,7d,14d" {
		t.Errorf("14d presets %s", got)
	}
	if got := strings.Join(keys(60*24*time.Hour), ","); got != "1h,6h,24h,7d,14d,30d,all" {
		t.Errorf("60d presets %s", got)
	}
	if r := parseRange(q(""), now, loc, 3*24*time.Hour); r.Preset != "all" || !r.From.Equal(now.Add(-3*24*time.Hour)) {
		t.Errorf("short retention default %+v", r)
	}
	if got := rangeLabel("ko", checkRange{Preset: "all", From: now.Add(-60 * 24 * time.Hour), To: now}, loc); got != "최근 전체 60일" {
		t.Errorf("all label %q", got)
	}
	if got := rangeLabel("ko", checkRange{Preset: "24h"}, loc); got != "최근 24시간" {
		t.Errorf("preset label %q", got)
	}
	same := checkRange{From: time.Date(2026, 10, 30, 22, 0, 0, 0, loc), To: time.Date(2026, 10, 30, 23, 0, 0, 0, loc)}
	if got := rangeLabel("en", same, loc); got != "2026-10-30 22:00–23:00" {
		t.Errorf("same day label %q", got)
	}
	same.To = time.Date(2026, 10, 31, 1, 0, 0, 0, loc)
	if got := rangeLabel("en", same, loc); got != "2026-10-30 22:00 – 2026-10-31 01:00" {
		t.Errorf("two day label %q", got)
	}
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

	edit := editView{Action: "/admin/incidents/3/edit", Title: "t", StartedAt: "2026-10-30T22:37", ResolvedAt: "2026-10-30T22:47", TimeZone: "Asia/Seoul",
		Components: []compChoice{{ID: 5, Name: "DB", Current: "major_outage"}}, Statuses: []statusChoice{{Key: "major_outage", Label: "주요 장애"}}, Errors: []string{"bad"}, Show: true}
	out = render(t, s, "admin_incident", struct {
		adminBase
		Inc       incidentView
		FormView  formView
		Edit      editView
		PublicURL string
	}{base, s.incidentView(in, "ko", time.UTC, true), fv, edit, ""})
	for _, want := range []string{`<details class="edit" open>`, `action="/admin/incidents/3/edit"`, `name="resolved_at" value="2026-10-30T22:47"`, `<option value="major_outage" selected>`, "Asia/Seoul"} {
		if !strings.Contains(out, want) {
			t.Fatalf("edit form missing %q", want)
		}
	}
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
	if got := tipHead("en", d) + " | " + tipDate("en", d); got != "99.90% uptime | Tue, Oct 13" {
		t.Fatalf("en: %q", got)
	}
	d.Down, d.Up = 83*time.Minute, 24*time.Hour-83*time.Minute
	if got := tipHead("ko", d) + " | " + tipDate("ko", d); got != "가용률 94.24% | 10월 13일 (화)" {
		t.Fatalf("ko: %q", got)
	}
	empty := uptime.Day{Date: d.Date, Worst: judge.NoData}
	if tipHead("ko", empty) != "데이터 없음" {
		t.Fatalf("no data: %q", tipHead("ko", empty))
	}

	// Maintenance is left out of the percentage but still shows.
	maint := uptime.Day{Date: d.Date, Up: 22 * time.Hour, Maint: 2*time.Hour + time.Minute, Worst: judge.Maintenance}
	if got := tipHead("en", maint); got != "100.00% uptime" {
		t.Fatalf("maintenance: %q", got)
	}
	if got := dayClass(maint); got != "d-ok has-maint" {
		t.Fatalf("maintenance day class %q", got)
	}
	if got := dayClass(uptime.Day{Maint: time.Hour, Worst: judge.Maintenance}); got != "d-maint" {
		t.Fatalf("maintenance-only day class %q", got)
	}
}

func TestDayViews(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	at := func(day, h, m int) time.Time { return time.Date(2026, 10, day, h, m, 0, 0, loc) }
	span := func(st judge.Status, from, to time.Time) uptime.Span {
		return uptime.Span{Status: st, Start: from, End: to, Dur: to.Sub(from)}
	}
	day := uptime.Day{Date: at(30, 0, 0), Spans: []uptime.Span{
		span(judge.Maintenance, at(30, 1, 0), at(30, 3, 1)),
		span(judge.Major, at(30, 22, 36), at(30, 22, 47)),
		span(judge.Partial, at(30, 23, 0), at(30, 23, 5)),
	}}
	end := at(30, 23, 56)
	links := []store.IncidentLink{
		{IncidentID: 1, Title: "ended at midnight", Start: at(29, 20, 0), End: ptr(at(30, 0, 0))},
		{IncidentID: 2, Title: "Plex down", Start: at(30, 22, 37), End: &end},
		{IncidentID: 2, Title: "Plex down", Start: at(30, 22, 40), End: &end},
		{IncidentID: 3, Title: "next day", Start: at(31, 0, 0)},
	}
	v := dayViews("ko", []uptime.Day{day}, links, loc, "public")[0]
	var lines []string
	for _, l := range v.Lines {
		lines = append(lines, fmt.Sprintf("%s %s %v +%d", l.Status, l.Total, l.Windows, l.More))
	}
	want := "major_outage 11분 [22:36–22:47] +0 | partial_outage 5분 [23:00–23:05] +0 | under_maintenance 2시간 1분 [01:00–03:01] +0"
	if got := strings.Join(lines, " | "); got != want {
		t.Fatalf("lines\n got %s\nwant %s", got, want)
	}
	if len(v.Related) != 1 || v.Related[0].Title != "Plex down" || v.Related[0].URL != "/p/public/incidents/2" {
		t.Fatalf("related %+v", v.Related)
	}

	// More stretches than the tooltip lists are counted instead.
	busy := uptime.Day{Date: at(30, 0, 0)}
	for h := 1; h <= 5; h++ {
		busy.Spans = append(busy.Spans, span(judge.Major, at(30, h, 0), at(30, h, 1)))
	}
	if l := dayViews("en", []uptime.Day{busy}, nil, loc, "p")[0].Lines[0]; len(l.Windows) != maxWindows || l.More != 2 || l.Total != "5m" {
		t.Fatalf("busy %+v", l)
	}

	// A stretch to midnight ends at 24:00 and one still going says so.
	if got := fmtWindow("ko", span(judge.Major, at(30, 23, 50), at(31, 0, 0)), loc); got != "23:50–24:00" {
		t.Fatalf("midnight %q", got)
	}
	if got := fmtWindow("en", uptime.Span{Status: judge.Major, Start: at(30, 23, 0), End: at(30, 23, 30), Ongoing: true}, loc); got != "23:00–ongoing" {
		t.Fatalf("ongoing %q", got)
	}
}

func ptr[T any](v T) *T { return &v }

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
		{http.MethodGet, "/api/v1/admin/pages", "", http.StatusUnauthorized},
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

func TestBackfill(t *testing.T) {
	now := time.Date(2026, 10, 30, 23, 56, 0, 0, time.UTC)
	start, end := now.Add(-80*time.Minute), now.Add(-69*time.Minute)
	later := now.Add(time.Hour)
	for _, c := range []struct {
		name            string
		stage           string
		started, solved *time.Time
		want            string
	}{
		{"nothing", store.StageInvestigating, nil, nil, ""},
		{"past outage", store.StageResolved, &start, &end, ""},
		{"started earlier, still open", store.StageIdentified, &start, nil, ""},
		{"resolved only", store.StageResolved, nil, &end, ""},
		{"start in the future", store.StageInvestigating, &later, nil, "admin.err_started_future"},
		{"resolve time on an open stage", store.StageMonitoring, &start, &end, "admin.err_resolved_stage"},
		{"resolve time in the future", store.StageResolved, &start, &later, "admin.err_resolved_future"},
		{"resolved before it started", store.StageResolved, &end, &start, "admin.err_resolved_order"},
	} {
		if got := strings.Join(backfillErrors(c.stage, c.started, c.solved, now), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	in := newIncident(store.StageResolved, "b", "dev", nil, &start, &end, now)
	if !in.At.Equal(end) || !in.Since.Equal(start) {
		t.Fatalf("past outage input %+v", in)
	}
	if in := newIncident(store.StageInvestigating, "b", "dev", nil, nil, nil, now); !in.At.Equal(now) || !in.Since.IsZero() {
		t.Fatalf("plain input %+v", in)
	}

	loc := time.FixedZone("KST", 9*3600)
	if got, err := parseLocal("2026-10-30T22:37", loc); err != nil || !got.Equal(time.Date(2026, 10, 30, 13, 37, 0, 0, time.UTC)) {
		t.Fatalf("parseLocal: %v %v", got, err)
	}
	if got, err := parseLocal("", loc); got != nil || err != nil {
		t.Fatalf("empty: %v %v", got, err)
	}
	if _, err := parseLocal("yesterday", loc); err == nil {
		t.Fatal("bad time accepted")
	}
}

func TestRecordLink(t *testing.T) {
	loc := time.FixedZone("KST", 9*3600)
	start := time.Date(2026, 10, 30, 13, 36, 21, 0, time.UTC)
	end := time.Date(2026, 10, 30, 13, 47, 22, 0, time.UTC)
	got := recordLink("public", "plex", judge.Major, start, &end, loc)
	want := "/admin/incidents/new?component=plex&page=public&resolved_at=2026-10-30T22%3A48&started_at=2026-10-30T22%3A36&status=major_outage"
	if got != want {
		t.Fatalf("record link\n got %s\nwant %s", got, want)
	}
	// An end on the minute stays, and an ongoing outage has no end.
	exact := time.Date(2026, 10, 30, 13, 47, 0, 0, time.UTC)
	if got := recordLink("p", "c", judge.Partial, start, &exact, loc); !strings.Contains(got, "resolved_at=2026-10-30T22%3A47") {
		t.Fatalf("exact end %s", got)
	}
	if got := recordLink("p", "c", judge.Partial, start, nil, loc); strings.Contains(got, "resolved_at") {
		t.Fatalf("ongoing %s", got)
	}
	if got := checkPeriodURL(5, start, end); !strings.HasPrefix(got, "/admin/checks/5?from=") || !strings.Contains(got, "&to=") {
		t.Fatalf("check period %s", got)
	}
}

func TestLatencyChart(t *testing.T) {
	from := time.Date(2026, 10, 30, 22, 0, 0, 0, time.UTC)
	to := from.Add(240 * time.Minute)
	f := func(v float64) *float64 { return &v }
	i := func(v int) *int { return &v }
	if latencyChart("en", nil, 240, from, to, 0, 0) != "" {
		t.Fatal("empty chart drawn")
	}
	buckets := []store.LatencyBucket{
		{Index: 0, AvgMS: f(2), MaxMS: i(4), Count: 6},
		{Index: 1, AvgMS: f(3), MaxMS: i(9), Count: 6},
		{Index: 2, Failures: 6, Count: 6},
		{Index: 3, AvgMS: f(2), MaxMS: i(3), Count: 6},
	}
	out := string(latencyChart("ko", buckets, 240, from, to, 2500*time.Millisecond, 4*time.Second))
	for _, want := range []string{`<svg class="lat-chart"`, `class="lat-avg" points=`, `class="lat-fail"`, `class="lat-warn"`, `실패 6/6`, `평균 3 ms · 최대 9 ms`, `<circle class="lat-avg"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("chart lacks %s\n%s", want, out)
		}
	}
	if strings.Count(out, `class="lat-hit"`) != 4 {
		t.Fatal("one tooltip per slice")
	}
	if chartSlices(time.Hour, 10*time.Second) != 240 || chartSlices(time.Hour, time.Minute) != 60 || chartSlices(time.Minute, time.Hour) != 1 {
		t.Fatal("slices follow the check interval")
	}
}

func TestIncidentEdit(t *testing.T) {
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 30, 13, 37, 0, 0, time.UTC)
	end := time.Date(2026, 10, 30, 13, 47, 0, 0, time.UTC)
	open := &store.Incident{ID: 1, Title: "DB down", StartedAt: start}
	done := &store.Incident{ID: 2, Title: "DB down", StartedAt: start, ResolvedAt: &end}
	later := now.Add(time.Hour)
	before := start.Add(-time.Minute)
	empty := ""
	for _, c := range []struct {
		name string
		in   *store.Incident
		e    store.IncidentEdit
		want string
	}{
		{"title only", done, store.IncidentEdit{Title: ptr("Plex down")}, ""},
		{"empty title", done, store.IncidentEdit{Title: &empty}, "admin.err_title"},
		{"future start", open, store.IncidentEdit{StartedAt: &later}, "admin.err_started_future"},
		{"resolve time on open", open, store.IncidentEdit{ResolvedAt: &end}, "admin.err_edit_open"},
		{"components on open", open, store.IncidentEdit{Components: map[int64]judge.Status{}}, "admin.err_edit_open"},
		{"end before start", done, store.IncidentEdit{ResolvedAt: &before}, "admin.err_resolved_order"},
		{"earlier start", done, store.IncidentEdit{StartedAt: &before}, ""},
	} {
		if got := strings.Join(editErrors(c.in, c.e, now), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	// Saving the title alone leaves the times and components as they were.
	loc := time.FixedZone("KST", 9*3600)
	ev := editView{StartedAt: "2026-10-30T22:37", ResolvedAt: "2026-10-30T22:47", Components: []compChoice{{ID: 5, Current: "major_outage"}, {ID: 6}}}
	form := func(vals string) *http.Request {
		r := httptest.NewRequest("POST", "/", strings.NewReader(vals))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	e, bad := readEditForm(form("title=Plex+down&started_at=2026-10-30T22%3A37&resolved_at=2026-10-30T22%3A47&c5=major_outage&c6="), done, ev, loc)
	if bad || e.Title == nil || *e.Title != "Plex down" || e.StartedAt != nil || e.ResolvedAt != nil || e.Components != nil {
		t.Fatalf("title only: %+v %v", e, bad)
	}
	e, bad = readEditForm(form("title=DB+down&started_at=2026-10-30T22%3A30&resolved_at=2026-10-30T22%3A47&c5=major_outage&c6=partial_outage"), done, ev, loc)
	if bad || e.Title != nil || e.StartedAt == nil || !e.StartedAt.Equal(time.Date(2026, 10, 30, 13, 30, 0, 0, time.UTC)) || len(e.Components) != 2 {
		t.Fatalf("start and components: %+v %v", e, bad)
	}
	if _, bad := readEditForm(form("title=DB+down&started_at=soon"), done, ev, loc); !bad {
		t.Fatal("bad time accepted")
	}
}

func TestMaintenance(t *testing.T) {
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	start, end := now.Add(time.Hour), now.Add(3*time.Hour)
	past := now.Add(-time.Minute)
	for _, c := range []struct {
		name       string
		start, end *time.Time
		want       string
	}{
		{"window ahead", &start, &end, ""},
		{"already started", &past, &end, ""},
		{"no end", &start, nil, "admin.err_maint_times"},
		{"end before start", &end, &start, "admin.err_resolved_order"},
		{"end passed", &past, &past, "admin.err_resolved_order"},
		{"over", ptr(now.Add(-time.Hour)), &past, "admin.err_maint_over"},
	} {
		if got := strings.Join(maintenanceErrors(c.start, c.end, now), ","); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	got := maintenanceStatuses(map[int64]judge.Status{1: judge.Major, 2: judge.Partial})
	if len(got) != 2 || got[1] != judge.Maintenance || got[2] != judge.Maintenance {
		t.Fatalf("statuses %v", got)
	}
	in := newMaintenance("b", "dev", map[int64]judge.Status{1: judge.Major}, start, end, now)
	if in.Kind != store.KindMaintenance || in.Stage != store.StageScheduled || !in.At.Equal(now) || !in.Since.Equal(start) || !in.Until.Equal(end) || in.Components[1] != judge.Maintenance {
		t.Fatalf("input %+v", in)
	}

	loc := time.FixedZone("KST", 9*3600)
	if got := fmtWindowRange(time.Date(2026, 10, 30, 17, 0, 0, 0, time.UTC), time.Date(2026, 10, 30, 19, 0, 0, 0, time.UTC), loc); got != "2026-10-31 02:00 KST – 04:00" {
		t.Fatalf("window %q", got)
	}
	m := store.Incident{Kind: store.KindMaintenance, StartedAt: start, ResolvedAt: &end}
	if maintStage(m, now) != "maint_upcoming" || maintStage(m, start) != "maint_active" || maintStage(m, end) != "maint_done" {
		t.Fatal("maintenance stages")
	}
	// A maintenance in a timeline: open ones may be edited into the future.
	if keys := editErrors(&m, store.IncidentEdit{ResolvedAt: ptr(end.Add(time.Hour))}, now); len(keys) != 0 {
		t.Fatalf("reschedule %v", keys)
	}
}
