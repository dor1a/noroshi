package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

// docker asks the Docker Engine API, usually through a read-only socket
// proxy, whether a container is running and, when it has a health check,
// healthy. A health check that is still starting decides nothing.
func (r *Runner) docker(ctx context.Context, c config.Check) Outcome {
	client, base, err := r.dockerClient(c.DockerHost)
	if err != nil {
		return Outcome{Detail: err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/containers/"+url.PathEscape(c.Container)+"/json", nil)
	if err != nil {
		return Outcome{Detail: err.Error()}
	}
	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return Outcome{Latency: latency, Detail: describe(err)}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Outcome{Latency: latency, Detail: "container not found"}
	default:
		return Outcome{Latency: latency, Detail: fmt.Sprintf("docker API returned %d", resp.StatusCode)}
	}
	var info struct {
		State struct {
			Status     string
			Running    bool
			Restarting bool
			ExitCode   int
			Health     *struct{ Status string }
		}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return Outcome{Latency: latency, Detail: "unreadable docker API response"}
	}
	st := info.State
	out := Outcome{Latency: latency}
	switch {
	case st.Restarting:
		out.Detail = "restarting"
	case !st.Running:
		out.Detail = fmt.Sprintf("%s (exit code %d)", st.Status, st.ExitCode)
	case st.Health == nil:
		out.OK, out.Detail = true, "running"
	case st.Health.Status == "healthy":
		out.OK, out.Detail = true, "running, healthy"
	case st.Health.Status == "starting":
		out.Skip, out.Detail = true, "running, health check starting"
	default:
		out.Detail = "running, " + st.Health.Status
	}
	return out
}

// dockerClient returns a client for a DOCKER_HOST style address, reusing
// one per host.
func (r *Runner) dockerClient(host string) (*http.Client, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.dockers[host]; ok {
		return c, dockerBase(host), nil
	}
	tr := &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second}
	switch {
	case strings.HasPrefix(host, "unix://"):
		path := strings.TrimPrefix(host, "unix://")
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
	case strings.HasPrefix(host, "tcp://"):
	default:
		return nil, "", fmt.Errorf("unsupported docker_host %q", host)
	}
	c := &http.Client{Transport: tr}
	if r.dockers == nil {
		r.dockers = map[string]*http.Client{}
	}
	r.dockers[host] = c
	return c, dockerBase(host), nil
}

func dockerBase(host string) string {
	if strings.HasPrefix(host, "tcp://") {
		return "http://" + strings.TrimPrefix(host, "tcp://")
	}
	return "http://docker"
}
