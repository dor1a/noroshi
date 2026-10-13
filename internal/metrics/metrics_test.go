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
