// Package probe runs one check and reports what it saw. It does not decide
// whether a component is down; that is the judge package's job.
package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

// UserAgent is sent with HTTP checks.
var UserAgent = "noroshi"

// Outcome is the result of one probe run.
type Outcome struct {
	OK      bool
	Slow    bool
	Skip    bool // the probe could not decide, e.g. an RDAP lookup failed
	Warn    bool // a certificate or domain expires within warn_before
	Latency time.Duration
	Detail  string

	CertExpiry   *time.Time
	DomainExpiry *time.Time
}

// Runner runs checks. Its zero value is not usable; use NewRunner.
type Runner struct {
	RDAP *RDAP
	Now  func() time.Time
}

// NewRunner returns a runner with a default RDAP client.
func NewRunner() *Runner {
	return &Runner{RDAP: NewRDAP(), Now: time.Now}
}

// Run executes a check with its configured timeout.
func (r *Runner) Run(ctx context.Context, c config.Check) Outcome {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout.Duration)
	defer cancel()
	switch c.Type {
	case config.TypeHTTP:
		return r.http(ctx, c)
	case config.TypeTCP:
		return r.tcp(ctx, c)
	case config.TypeTLS:
		return r.tls(ctx, c)
	case config.TypeDomainExpiry:
		return r.domain(ctx, c)
	}
	return Outcome{Detail: "unknown check type " + c.Type}
}

func (r *Runner) http(ctx context.Context, c config.Check) Outcome {
	tr := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: c.InsecureSkipVerify}, //nolint:gosec // opt-in per check
		DisableKeepAlives: true,
		ForceAttemptHTTP2: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	if !c.Follow() {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	req, err := http.NewRequestWithContext(ctx, c.Method, c.URL, nil)
	if err != nil {
		return Outcome{Detail: err.Error()}
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}

	start := r.Now()
	resp, err := client.Do(req)
	latency := r.Now().Sub(start)
	if err != nil {
		return Outcome{Latency: latency, Detail: describe(err)}
	}
	defer resp.Body.Close()

	out := Outcome{Latency: latency, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode), OK: c.Accepts(resp.StatusCode)}
	if c.Keyword != "" && out.OK {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return Outcome{Latency: latency, Detail: "read body: " + describe(err)}
		}
		if !strings.Contains(string(body), c.Keyword) {
			out.OK = false
			out.Detail += ", keyword not found"
		}
	} else {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter
		out.CertExpiry = &exp
		r.applyExpiry(&out, exp, c.WarnBefore.Duration, "certificate")
	}
	if out.OK && c.SlowAfter.Duration > 0 && latency > c.SlowAfter.Duration {
		out.Slow = true
	}
	return out
}

func (r *Runner) tcp(ctx context.Context, c config.Check) Outcome {
	var d net.Dialer
	start := r.Now()
	conn, err := d.DialContext(ctx, "tcp", c.Address)
	latency := r.Now().Sub(start)
	if err != nil {
		return Outcome{Latency: latency, Detail: describe(err)}
	}
	conn.Close()
	return Outcome{OK: true, Latency: latency, Detail: "connected"}
}

func (r *Runner) tls(ctx context.Context, c config.Check) Outcome {
	host, _, _ := net.SplitHostPort(c.Address)
	sn := c.ServerName
	if sn == "" {
		sn = host
	}
	d := tls.Dialer{Config: &tls.Config{ServerName: sn, InsecureSkipVerify: c.InsecureSkipVerify}} //nolint:gosec // opt-in per check
	start := r.Now()
	conn, err := d.DialContext(ctx, "tcp", c.Address)
	latency := r.Now().Sub(start)
	if err != nil {
		return Outcome{Latency: latency, Detail: describe(err)}
	}
	defer conn.Close()
	certs := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Outcome{Latency: latency, Detail: "no certificate presented"}
	}
	exp := certs[0].NotAfter
	out := Outcome{OK: true, Latency: latency, Detail: "handshake ok", CertExpiry: &exp}
	r.applyExpiry(&out, exp, c.WarnBefore.Duration, "certificate")
	return out
}

func (r *Runner) domain(ctx context.Context, c config.Check) Outcome {
	start := r.Now()
	exp, err := r.RDAP.Expiry(ctx, c.Domain)
	latency := r.Now().Sub(start)
	if err != nil {
		// A failed lookup says nothing about the domain itself.
		return Outcome{Skip: true, Latency: latency, Detail: "rdap: " + describe(err)}
	}
	out := Outcome{OK: true, Latency: latency, Detail: "expires " + exp.UTC().Format("2006-01-02"), DomainExpiry: &exp}
	r.applyExpiry(&out, exp, c.WarnBefore.Duration, "domain")
	return out
}

// applyExpiry fails the outcome when something has expired and warns when
// it expires within warn.
func (r *Runner) applyExpiry(out *Outcome, exp time.Time, warn time.Duration, what string) {
	now := r.Now()
	switch {
	case !now.Before(exp):
		out.OK = false
		out.Detail += ", " + what + " expired"
	case exp.Sub(now) < warn:
		out.Warn = true
	}
}

// describe shortens common network errors for the admin view.
func describe(err error) string {
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &dnsErr):
		return "dns: " + dnsErr.Err
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
