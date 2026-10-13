package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

func dockerAPI() http.Handler {
	states := map[string]string{
		"plain":      `{"State":{"Status":"running","Running":true}}`,
		"healthy":    `{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`,
		"unhealthy":  `{"State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy"}}}`,
		"starting":   `{"State":{"Status":"running","Running":true,"Health":{"Status":"starting"}}}`,
		"exited":     `{"State":{"Status":"exited","Running":false,"ExitCode":137}}`,
		"restarting": `{"State":{"Status":"restarting","Running":true,"Restarting":true}}`,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		body, ok := states[name]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body)) //nolint:errcheck
	})
}

func TestDocker(t *testing.T) {
	srv := httptest.NewServer(dockerAPI())
	defer srv.Close()
	host := "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	r := NewRunner()
	run := func(container string) Outcome {
		return r.Run(context.Background(), config.Check{
			Type: config.TypeDocker, Container: container, DockerHost: host, Timeout: config.Duration{Duration: 2 * time.Second},
		})
	}
	for _, tc := range []struct {
		container  string
		ok, skip   bool
		detailPart string
	}{
		{"plain", true, false, "running"},
		{"healthy", true, false, "healthy"},
		{"unhealthy", false, false, "unhealthy"},
		{"starting", false, true, "starting"},
		{"exited", false, false, "exit code 137"},
		{"restarting", false, false, "restarting"},
		{"missing", false, false, "not found"},
	} {
		out := run(tc.container)
		if out.OK != tc.ok || out.Skip != tc.skip || !strings.Contains(out.Detail, tc.detailPart) {
			t.Errorf("%s: %+v", tc.container, out)
		}
	}
}

func TestDockerUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	srv := &http.Server{Handler: dockerAPI()}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()
	out := NewRunner().Run(context.Background(), config.Check{
		Type: config.TypeDocker, Container: "healthy", DockerHost: "unix://" + sock, Timeout: config.Duration{Duration: 2 * time.Second},
	})
	if !out.OK {
		t.Fatalf("unix socket: %+v", out)
	}
}
