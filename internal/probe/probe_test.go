package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/config"
)

// check builds a validated check from YAML so tests use the real defaults.
func check(t *testing.T, body string) config.Check {
	t.Helper()
	y := "pages:\n  - key: p\n    title: P\n    groups:\n      - key: g\n        name: G\n        components:\n          - key: c\n            name: C\n            checks:\n" + indent(body, 14)
	f, err := config.ParseFile([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return f.Pages[0].Groups[0].Components[0].Checks[0]
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	var b strings.Builder
	for i, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if i == 0 {
			b.WriteString(pad + "- " + strings.TrimSpace(line) + "\n")
			continue
		}
		b.WriteString(pad + "  " + strings.TrimSpace(line) + "\n")
	}
	return b.String()
}

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, "hello noroshi")
		case "/500":
			w.WriteHeader(500)
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/slow":
			time.Sleep(300 * time.Millisecond)
			fmt.Fprint(w, "late")
		case "/hang":
			time.Sleep(2 * time.Second)
		}
	}))
	defer srv.Close()
	r := NewRunner()
	ctx := context.Background()

	cases := []struct {
		name     string
		yaml     string
		ok, slow bool
		detail   string
	}{
		{"200 is ok", "key: k\ntype: http\nurl: " + srv.URL + "/ok", true, false, "HTTP 200"},
		{"500 fails", "key: k\ntype: http\nurl: " + srv.URL + "/500", false, false, "HTTP 500"},
		{"500 accepted when expected", "key: k\ntype: http\nurl: " + srv.URL + "/500\nexpect_status: 500", true, false, "HTTP 500"},
		{"keyword found", "key: k\ntype: http\nurl: " + srv.URL + "/ok\nkeyword: noroshi", true, false, "HTTP 200"},
		{"keyword missing fails", "key: k\ntype: http\nurl: " + srv.URL + "/ok\nkeyword: missing", false, false, "keyword not found"},
		{"redirect followed", "key: k\ntype: http\nurl: " + srv.URL + "/redirect", true, false, "HTTP 200"},
		{"redirect not followed fails on 302", "key: k\ntype: http\nurl: " + srv.URL + "/redirect\nfollow_redirects: false", false, false, "HTTP 302"},
		{"slow response is ok but slow", "key: k\ntype: http\nurl: " + srv.URL + "/slow\nslow_after: 100ms\ntimeout: 5s", true, true, "HTTP 200"},
		{"timeout fails", "key: k\ntype: http\nurl: " + srv.URL + "/hang\ntimeout: 500ms\ninterval: 10s", false, false, "timeout"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := r.Run(ctx, check(t, c.yaml))
			if out.OK != c.ok || out.Slow != c.slow || !strings.Contains(out.Detail, c.detail) {
				t.Fatalf("got ok=%v slow=%v detail=%q", out.OK, out.Slow, out.Detail)
			}
		})
	}
}

func TestHTTPSCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	r := NewRunner()

	out := r.Run(context.Background(), check(t, "key: k\ntype: http\nurl: "+srv.URL))
	if out.OK {
		t.Fatal("self-signed certificate should fail verification")
	}

	out = r.Run(context.Background(), check(t, "key: k\ntype: http\nurl: "+srv.URL+"\ninsecure_skip_verify: true"))
	if !out.OK || out.CertExpiry == nil {
		t.Fatalf("insecure check should pass and report expiry: %+v", out)
	}

	// Pretend it is just after the certificate expired.
	exp := *out.CertExpiry
	r.Now = func() time.Time { return exp.Add(time.Minute) }
	out = r.Run(context.Background(), check(t, "key: k\ntype: http\nurl: "+srv.URL+"\ninsecure_skip_verify: true"))
	if out.OK || !strings.Contains(out.Detail, "certificate expired") {
		t.Fatalf("expired certificate should fail: %+v", out)
	}

	r.Now = func() time.Time { return exp.Add(-24 * time.Hour) }
	out = r.Run(context.Background(), check(t, "key: k\ntype: http\nurl: "+srv.URL+"\ninsecure_skip_verify: true\nwarn_before: 30d"))
	if !out.OK || !out.Warn {
		t.Fatalf("certificate a day from expiry should pass with a warning: %+v", out)
	}
}

func TestTCPAndTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	addr := ln.Addr().String()
	r := NewRunner()
	if out := r.Run(context.Background(), check(t, "key: k\ntype: tcp\naddress: "+addr)); !out.OK {
		t.Fatalf("tcp to listener should pass: %+v", out)
	}
	ln.Close()
	if out := r.Run(context.Background(), check(t, "key: k\ntype: tcp\naddress: "+addr)); out.OK {
		t.Fatal("tcp to closed port should fail")
	}

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "https://")
	out := r.Run(context.Background(), check(t, "key: k\ntype: tls\naddress: "+host+"\ninsecure_skip_verify: true"))
	if !out.OK || out.CertExpiry == nil {
		t.Fatalf("tls handshake should pass and report expiry: %+v", out)
	}
}

func TestDomainExpiry(t *testing.T) {
	var lookups int
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/dns.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"services":[[["com","net"],["%s/rdap/"]]]}`, base)
	})
	mux.HandleFunc("/rdap/domain/example.com", func(w http.ResponseWriter, r *http.Request) {
		lookups++
		fmt.Fprint(w, `{"events":[{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},{"eventAction":"expiration","eventDate":"2030-08-13T04:00:00Z"}]}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	r := NewRunner()
	r.RDAP.BootstrapURL = srv.URL + "/dns.json"
	r.Now = func() time.Time { return time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC) }

	out := r.Run(context.Background(), check(t, "key: k\ntype: domain_expiry\ndomain: example.com"))
	if !out.OK || out.Warn || out.DomainExpiry == nil || !strings.Contains(out.Detail, "2030-08-13") {
		t.Fatalf("got %+v", out)
	}

	r.Now = func() time.Time { return time.Date(2030, 8, 1, 0, 0, 0, 0, time.UTC) }
	out = r.Run(context.Background(), check(t, "key: k\ntype: domain_expiry\ndomain: example.com"))
	if !out.OK || !out.Warn {
		t.Fatalf("12 days before expiry should warn: %+v", out)
	}

	out = r.Run(context.Background(), check(t, "key: k\ntype: domain_expiry\ndomain: example.org"))
	if !out.Skip || out.OK {
		t.Fatalf("unknown tld should be skipped, not failed: %+v", out)
	}
	if lookups != 2 {
		t.Fatalf("expected 2 lookups, got %d", lookups)
	}
}
