package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const sample = `
defaults:
  interval: 30s
pages:
  - key: home
    title:
      ko: 서비스 상태
      en: Service Status
    timezone: Asia/Seoul
    groups:
      - key: web
        name: Web
        components:
          - key: blog
            name:
              ko: 블로그
              en: Blog
            checks:
              - key: https
                type: http
                url: https://example.com/
                expect_status:
                  - 200-299
                  - "401"
                slow_after: 2s
              - key: domain
                type: domain_expiry
                domain: example.com
              - key: port
                type: tcp
                address: example.com:443
                interval: 2m
                fail_threshold: 5
`

func TestParseDefaults(t *testing.T) {
	f, err := ParseFile([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	checks := f.Pages[0].Groups[0].Components[0].Checks
	httpc, dom, tcp := checks[0], checks[1], checks[2]

	if httpc.Interval.Duration != 30*time.Second || httpc.Timeout.Duration != 10*time.Second {
		t.Fatalf("http interval/timeout %s/%s", httpc.Interval, httpc.Timeout)
	}
	if httpc.FailThreshold != 3 || httpc.RecoverThreshold != 2 || httpc.Method != "GET" || !httpc.Follow() {
		t.Fatalf("http defaults not applied: %+v", httpc)
	}
	if !httpc.Accepts(204) || !httpc.Accepts(401) || httpc.Accepts(302) || httpc.Accepts(500) {
		t.Fatalf("expect_status ranges wrong: %v", httpc.ExpectRanges())
	}
	if dom.Interval.Duration != 12*time.Hour {
		t.Fatalf("domain_expiry default interval %s", dom.Interval)
	}
	if dom.WarnBefore.Duration != 30*24*time.Hour {
		t.Fatalf("warn_before default %s", dom.WarnBefore)
	}
	if tcp.Interval.Duration != 2*time.Minute || tcp.FailThreshold != 5 {
		t.Fatalf("tcp overrides lost: %+v", tcp)
	}
	if got := f.Pages[0].Groups[0].Name.Get("ko"); got != "Web" {
		t.Fatalf("plain string name should apply to every locale, got %q", got)
	}
	if got := f.Pages[0].Title.Get("en"); got != "Service Status" {
		t.Fatalf("title en %q", got)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"unknown field", `
pages:
  - key: home
    title: x
    colour: red
`, "colour"},
		{"bad key", `
pages:
  - key: Home Page
    title: x
`, "key must match"},
		{"duplicate component", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
      - key: b
        name: B
        components:
          - key: c
            name: C
`, "duplicate component key"},
		{"missing url", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
            checks:
              - key: k
                type: http
`, "absolute http or https URL"},
		{"timeout not shorter than interval", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
            checks:
              - key: k
                type: tcp
                address: example.com:443
                interval: 10s
                timeout: 10s
`, "timeout must be positive"},
		{"slow_after not shorter than timeout", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
            checks:
              - key: k
                type: http
                url: https://example.com/
                slow_after: 10s
`, "slow_after"},
		{"bad status range", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
            checks:
              - key: k
                type: http
                url: https://example.com/
                expect_status:
                  - 299-200
`, "invalid expect_status"},
		{"unknown type", `
pages:
  - key: home
    title: x
    groups:
      - key: a
        name: A
        components:
          - key: c
            name: C
            checks:
              - key: k
                type: icmp
`, "unknown type"},
		{"bad timezone", `
pages:
  - key: home
    title: x
    timezone: Mars/Olympus
`, "unknown timezone"},
		{"unsupported locale", `
pages:
  - key: home
    title:
      fr: Statut
`, "unsupported locale"},
		{"unknown notifier type", `
notifiers:
  - key: ops
    type: slack
    url_env: NOROSHI_WEBHOOK_OPS
pages:
  - key: home
    title: x
`, "use discord or teams"},
		{"notifier url_env not a variable name", `
notifiers:
  - key: ops
    type: discord
    url_env: https://discord.com/api/webhooks/1/x
pages:
  - key: home
    title: x
`, "url_env must name an environment variable"},
		{"page notifies an unknown notifier", `
pages:
  - key: home
    title: x
    notify:
      - ops
`, "unknown notifier"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFile([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestAPITokens(t *testing.T) {
	base := "pages:\n  - key: home\n    title: x\n"
	f, err := ParseFile([]byte("api_tokens:\n  - key: ops\n    token_env: NOROSHI_TEST_API_TOKEN\n" + base))
	if err != nil || len(f.APITokens) != 1 || f.APITokens[0].Key != "ops" {
		t.Fatalf("got %+v %v", f.APITokens, err)
	}
	for want, yaml := range map[string]string{
		"duplicate token key": "api_tokens:\n  - key: ops\n    token_env: A\n  - key: ops\n    token_env: B\n",
		"token_env must name": "api_tokens:\n  - key: ops\n    token_env: secret-value\n",
	} {
		if _, err := ParseFile([]byte(yaml + base)); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}

	tok := f.APITokens[0]
	if _, err := tok.Token(); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Fatalf("unset: %v", err)
	}
	t.Setenv("NOROSHI_TEST_API_TOKEN", "short")
	if _, err := tok.Token(); err == nil || strings.Contains(err.Error(), "short\"") || !strings.Contains(err.Error(), "shorter than") {
		t.Fatalf("short token must fail without echoing it: %v", err)
	}
	long := strings.Repeat("x", MinTokenLength)
	t.Setenv("NOROSHI_TEST_API_TOKEN", long)
	if v, err := tok.Token(); err != nil || v != long {
		t.Fatalf("got %q %v", v, err)
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"30d": 30 * 24 * time.Hour,
		"90s": 90 * time.Second,
		"12h": 12 * time.Hour,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Fatalf("%s: got %s %v", in, got, err)
		}
	}
	if _, err := ParseDuration("-1d"); err == nil {
		t.Fatal("negative days should fail")
	}
}

func TestNotifierURL(t *testing.T) {
	n := Notifier{Key: "ops", Type: NotifierDiscord, URLEnv: "NOROSHI_TEST_WEBHOOK"}
	if _, err := n.URL(); err == nil || !strings.Contains(err.Error(), "is not set") {
		t.Fatalf("unset: %v", err)
	}
	t.Setenv("NOROSHI_TEST_WEBHOOK", "not a url")
	if _, err := n.URL(); err == nil || strings.Contains(err.Error(), "not a url") {
		t.Fatalf("bad URL must fail without echoing the value: %v", err)
	}
	t.Setenv("NOROSHI_TEST_WEBHOOK", "https://discord.com/api/webhooks/1/x")
	if u, err := n.URL(); err != nil || u != "https://discord.com/api/webhooks/1/x" {
		t.Fatalf("got %q %v", u, err)
	}
}

func TestAlertInheritanceAndDocker(t *testing.T) {
	f, err := ParseFile([]byte(`
defaults:
  docker_host: tcp://docker-socket-proxy:2375
pages:
  - key: public
    title: Public
    alert: false
    groups:
      - key: media
        name: Media
        components:
          - key: plex
            name: Plex
            checks:
              - key: container
                type: docker
                container: plex
                docker_host: tcp://atsui:2375
          - key: ombi
            name: Ombi
            alert: true
            checks:
              - key: container
                type: docker
                container: ombi
              - key: web
                type: http
                url: https://ombi.example.com/
                alert: false
`))
	if err != nil {
		t.Fatal(err)
	}
	comps := f.Pages[0].Groups[0].Components
	if comps[0].Checks[0].AlertEnabled() || !comps[1].Checks[0].AlertEnabled() || comps[1].Checks[1].AlertEnabled() {
		t.Fatal("alert: false must flow down from the page and be overridable at each level")
	}
	if comps[0].Checks[0].DockerHost != "tcp://atsui:2375" || comps[1].Checks[0].DockerHost != "tcp://docker-socket-proxy:2375" {
		t.Fatalf("docker hosts: %q %q", comps[0].Checks[0].DockerHost, comps[1].Checks[0].DockerHost)
	}
	if f.Pages[0].Private() {
		t.Fatal("pages are public by default")
	}

	for yaml, want := range map[string]string{
		"pages:\n  - key: a\n    title: A\n    visibility: secret\n":                       "visibility must be public or private",
		"pages:\n  - key: a\n    title: A\n    logo: logo.bmp\n":                           "logo must be",
		"admin:\n  logo: logo.bmp\npages:\n  - key: a\n    title: A\n":                     "admin: logo must be",
		"admin:\n  logo: https://cdn.example.com/logo\npages:\n  - key: a\n    title: A\n": "admin: logo must be",
		"admin:\n  logo: https:///logo.png\npages:\n  - key: a\n    title: A\n":            "admin: logo is not a valid URL",
		"pages:\n  - key: a\n    title: A\n    groups:\n      - key: g\n        name: G\n        components:\n          - key: c\n            name: C\n            checks:\n              - key: k\n                type: docker\n                container: x\n                docker_host: http://h:2375\n": "docker_host must start with",
		"pages:\n  - key: a\n    title: A\n    groups:\n      - key: g\n        name: G\n        components:\n          - key: c\n            name: C\n            checks:\n              - key: k\n                type: docker\n":                                                                           "container must be",
	} {
		if _, err := ParseFile([]byte(yaml)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestLoadFileResolvesLogo(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	if err := os.WriteFile(path, []byte("admin:\n  title: Home\n  logo: brand/admin.svg\npages:\n  - key: a\n    title: A\n    visibility: private\n    logo: brand/logo.png\n  - key: b\n    title: B\n    logo: https://cdn.example.com/img/logo.PNG?v=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Pages[0].Logo != dir+"/brand/logo.png" || !f.Pages[0].Private() {
		t.Fatalf("got %+v", f.Pages[0])
	}
	if l := f.Pages[1].Logo; l != "https://cdn.example.com/img/logo.PNG?v=2" || LogoExt(l) != ".png" {
		t.Fatalf("URL logo: %q", l)
	}
	if !f.Footer.ShowPoweredBy() {
		t.Fatal("footer credit is on by default")
	}
	if f.Admin.Logo != dir+"/brand/admin.svg" || f.Admin.Title.Get("en") != "Home" {
		t.Fatalf("admin: %+v", f.Admin)
	}
}
