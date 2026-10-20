package metrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/judge"
)

func TestAlertLabel(t *testing.T) {
	m := New("test")
	exp := time.Now().Add(time.Hour)
	m.CheckRun(CheckLabels{Page: "p", Component: "plex", Check: "c", Type: "docker", Alert: false}, judge.Down, time.Now(), time.Second, time.Minute, &exp, nil)
	m.CheckRun(CheckLabels{Page: "p", Component: "wiki", Check: "c", Type: "http", Alert: true}, judge.Up, time.Now(), time.Second, time.Minute, nil, nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, want := range []string{
		`noroshi_check_up{alert="false",check="c",component="plex",page="p",type="docker"} 0`,
		`noroshi_check_up{alert="true",check="c",component="wiki",page="p",type="http"} 1`,
		`noroshi_cert_expiry_timestamp_seconds{alert="false",check="c",component="plex",page="p"}`,
		`noroshi_check_last_run_timestamp_seconds{check="c",component="plex",page="p"}`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestCheckInfoAndFailure(t *testing.T) {
	m := New("test")
	l := CheckLabels{Page: "p", Component: "wiki", Check: "https", Type: "http", Alert: true,
		Name: "Wiki", Target: "https://wiki.example/"}
	m.CheckRun(l, judge.Down, time.Now(), time.Second, time.Minute, nil, nil)
	m.CheckFailure(l, "timeout")
	m.CheckFailure(l, "HTTP 502")

	body := scrape(t, m)
	for _, want := range []string{
		`noroshi_check_info{check="https",component="wiki",name="Wiki",page="p",target="https://wiki.example/"} 1`,
		`noroshi_check_failure_info{check="https",component="wiki",page="p",reason="HTTP 502"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, `reason="timeout"`) {
		t.Error("an older failure reason was kept")
	}

	m.CheckFailure(l, "")
	if strings.Contains(scrape(t, m), "noroshi_check_failure_info{") {
		t.Error("failure reason was not cleared")
	}

	m.SetLeader(false)
	if strings.Contains(scrape(t, m), "noroshi_check_info{") {
		t.Error("check info survived losing the lead")
	}
}

func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}
