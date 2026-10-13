package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/dor1a/noroshi/internal/i18n"
)

// Check types.
const (
	TypeHTTP         = "http"
	TypeTCP          = "tcp"
	TypeTLS          = "tls"
	TypeDomainExpiry = "domain_expiry"
	TypeDocker       = "docker"
)

// Page visibility.
const (
	VisibilityPublic  = "public"
	VisibilityPrivate = "private"
)

// DefaultDockerHost is where docker checks look when nothing else is set.
const DefaultDockerHost = "unix:///var/run/docker.sock"

// Notifier types.
const (
	NotifierDiscord = "discord"
	NotifierTeams   = "teams"
)

// File is the YAML config: pages, their component groups, components, and
// checks, plus the channels that incident announcements go to.
type File struct {
	Defaults  Defaults   `yaml:"defaults" json:"-"`
	Notifiers []Notifier `yaml:"notifiers" json:"-"`
	Pages     []Page     `yaml:"pages" json:"pages"`
}

// Notifier is a chat channel that receives incident announcements. The
// webhook URL is a secret, so the file only names the environment variable
// that holds it (NAME or NAME_FILE).
type Notifier struct {
	Key    string `yaml:"key"`
	Type   string `yaml:"type"`
	URLEnv string `yaml:"url_env"`
	Locale string `yaml:"locale"`
}

// URL reads the webhook URL from the environment.
func (n Notifier) URL() (string, error) {
	v, err := lookup(n.URLEnv)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("notifier %s: %s is not set", n.Key, n.URLEnv)
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("notifier %s: %s is not an http or https URL", n.Key, n.URLEnv)
	}
	return v, nil
}

// Defaults apply to every check that does not set its own value.
type Defaults struct {
	Interval         Duration `yaml:"interval"`
	Timeout          Duration `yaml:"timeout"`
	FailThreshold    int      `yaml:"fail_threshold"`
	RecoverThreshold int      `yaml:"recover_threshold"`
	WarnBefore       Duration `yaml:"warn_before"`
	DockerHost       string   `yaml:"docker_host"`
}

// Page is one status page. A private page is served only on the admin
// listener, to signed-in admins.
type Page struct {
	Key        string    `yaml:"key" json:"key"`
	Title      i18n.Text `yaml:"title" json:"title"`
	Timezone   string    `yaml:"timezone" json:"timezone"`
	Visibility string    `yaml:"visibility" json:"-"`
	Logo       string    `yaml:"logo" json:"-"`
	Alert      *bool     `yaml:"alert" json:"-"`
	Notify     []string  `yaml:"notify" json:"-"`
	Groups     []Group   `yaml:"groups" json:"groups"`
}

// Private reports whether the page is hidden from the public listener.
func (p Page) Private() bool { return p.Visibility == VisibilityPrivate }

// Group is a heading on a page.
type Group struct {
	Key        string      `yaml:"key" json:"key"`
	Name       i18n.Text   `yaml:"name" json:"name"`
	Alert      *bool       `yaml:"alert" json:"-"`
	Components []Component `yaml:"components" json:"components"`
}

// Component is one row on a page.
type Component struct {
	Key         string    `yaml:"key" json:"key"`
	Name        i18n.Text `yaml:"name" json:"name"`
	Description i18n.Text `yaml:"description" json:"description,omitempty"`
	Alert       *bool     `yaml:"alert" json:"-"`
	Checks      []Check   `yaml:"checks" json:"checks"`
}

// Check is one probe. Which fields apply depends on Type.
type Check struct {
	Key              string   `yaml:"key" json:"key"`
	Type             string   `yaml:"type" json:"type"`
	Interval         Duration `yaml:"interval" json:"interval"`
	Timeout          Duration `yaml:"timeout" json:"timeout"`
	FailThreshold    int      `yaml:"fail_threshold" json:"fail_threshold"`
	RecoverThreshold int      `yaml:"recover_threshold" json:"recover_threshold"`
	WarnBefore       Duration `yaml:"warn_before" json:"warn_before"`

	// Alert is false when an outage of this check should not page anyone,
	// for example because another alert already covers the service. It is
	// inherited from the page, group, and component.
	Alert *bool `yaml:"alert" json:"alert,omitempty"`

	// http
	URL                string            `yaml:"url" json:"url,omitempty"`
	Method             string            `yaml:"method" json:"method,omitempty"`
	ExpectStatus       StatusList        `yaml:"expect_status" json:"expect_status,omitempty"`
	Keyword            string            `yaml:"keyword" json:"keyword,omitempty"`
	FollowRedirects    *bool             `yaml:"follow_redirects" json:"follow_redirects,omitempty"`
	SlowAfter          Duration          `yaml:"slow_after" json:"slow_after,omitempty"`
	InsecureSkipVerify bool              `yaml:"insecure_skip_verify" json:"insecure_skip_verify,omitempty"`
	Headers            map[string]string `yaml:"headers" json:"headers,omitempty"`

	// tcp, tls
	Address    string `yaml:"address" json:"address,omitempty"`
	ServerName string `yaml:"server_name" json:"server_name,omitempty"`

	// domain_expiry
	Domain string `yaml:"domain" json:"domain,omitempty"`

	// docker
	Container  string `yaml:"container" json:"container,omitempty"`
	DockerHost string `yaml:"docker_host" json:"docker_host,omitempty"`

	expect [][2]int
}

// Accepts reports whether an HTTP status code is expected.
func (c Check) Accepts(code int) bool {
	for _, r := range c.expect {
		if code >= r[0] && code <= r[1] {
			return true
		}
	}
	return false
}

// Follow reports whether redirects are followed.
func (c Check) Follow() bool { return c.FollowRedirects == nil || *c.FollowRedirects }

// Target is a short human description of what the check probes.
func (c Check) Target() string {
	switch c.Type {
	case TypeHTTP:
		return c.URL
	case TypeTCP, TypeTLS:
		return c.Address
	case TypeDomainExpiry:
		return c.Domain
	case TypeDocker:
		return c.Container + " @ " + c.DockerHost
	}
	return ""
}

// AlertEnabled reports whether an outage of this check should alert.
func (c Check) AlertEnabled() bool { return c.Alert == nil || *c.Alert }

// StatusList holds expected HTTP status codes or ranges. YAML may write each
// entry as a number (200) or a string ("200-299").
type StatusList []string

// UnmarshalYAML accepts a single scalar or a sequence of scalars.
func (s *StatusList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*s = StatusList{n.Value}
	case yaml.SequenceNode:
		out := make(StatusList, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: expect_status entries must be codes or ranges", c.Line)
			}
			out = append(out, c.Value)
		}
		*s = out
	default:
		return fmt.Errorf("line %d: expect_status must be a code, a range, or a list of them", n.Line)
	}
	return nil
}

// Duration is a time.Duration that also accepts a "d" suffix for days.
type Duration struct{ time.Duration }

// UnmarshalYAML parses values such as "90s", "12h", or "30d".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON stores the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

// UnmarshalJSON reads what MarshalJSON wrote.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	v, err := ParseDuration(s)
	d.Duration = v
	return err
}

// ParseDuration is time.ParseDuration plus whole days ("30d").
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return v, nil
}

// LoadFile reads, defaults, and validates a config file. Relative logo
// paths are resolved against the file's directory.
func LoadFile(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := ParseFile(b)
	if err != nil {
		return nil, err
	}
	for i := range f.Pages {
		if l := f.Pages[i].Logo; l != "" && !filepath.IsAbs(l) {
			f.Pages[i].Logo = filepath.Join(filepath.Dir(path), l)
		}
	}
	return f, nil
}

// ParseFile parses YAML bytes, applies defaults, and validates the result.
func ParseFile(b []byte) (*File, error) {
	var f File
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	f.applyDefaults()
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

var (
	keyRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	envNameRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	logoExts    = map[string]bool{".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".ico": true}
)

func inherit(parent bool, v *bool) bool {
	if v != nil {
		return *v
	}
	return parent
}

func (f *File) applyDefaults() {
	d := &f.Defaults
	if d.Interval.Duration == 0 {
		d.Interval.Duration = 60 * time.Second
	}
	if d.Timeout.Duration == 0 {
		d.Timeout.Duration = 10 * time.Second
	}
	if d.FailThreshold == 0 {
		d.FailThreshold = 3
	}
	if d.RecoverThreshold == 0 {
		d.RecoverThreshold = 2
	}
	if d.WarnBefore.Duration == 0 {
		d.WarnBefore.Duration = 30 * 24 * time.Hour
	}
	if d.DockerHost == "" {
		d.DockerHost = DefaultDockerHost
	}
	for pi := range f.Pages {
		p := &f.Pages[pi]
		if p.Timezone == "" {
			p.Timezone = "UTC"
		}
		if p.Visibility == "" {
			p.Visibility = VisibilityPublic
		}
		pageAlert := inherit(true, p.Alert)
		for gi := range p.Groups {
			g := &p.Groups[gi]
			groupAlert := inherit(pageAlert, g.Alert)
			for ci := range g.Components {
				comp := &g.Components[ci]
				compAlert := inherit(groupAlert, comp.Alert)
				for ki := range comp.Checks {
					c := &comp.Checks[ki]
					alert := inherit(compAlert, c.Alert)
					c.Alert = &alert
					if c.Type == TypeDocker && c.DockerHost == "" {
						c.DockerHost = d.DockerHost
					}
					if c.Interval.Duration == 0 {
						c.Interval = d.Interval
						if c.Type == TypeDomainExpiry {
							c.Interval.Duration = 12 * time.Hour
						}
					}
					if c.Timeout.Duration == 0 {
						c.Timeout = d.Timeout
					}
					if c.FailThreshold == 0 {
						c.FailThreshold = d.FailThreshold
					}
					if c.RecoverThreshold == 0 {
						c.RecoverThreshold = d.RecoverThreshold
					}
					if c.WarnBefore.Duration == 0 {
						c.WarnBefore = d.WarnBefore
					}
					if c.Type == TypeHTTP {
						if c.Method == "" {
							c.Method = "GET"
						}
						c.Method = strings.ToUpper(c.Method)
						if len(c.ExpectStatus) == 0 {
							c.ExpectStatus = StatusList{"200-299"}
						}
					}
				}
			}
		}
	}
}

func (f *File) validate() error {
	var errs []error
	bad := func(where, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", where, fmt.Sprintf(format, args...)))
	}
	if len(f.Pages) == 0 {
		bad("pages", "at least one page is required")
	}
	notifierKeys := map[string]bool{}
	for _, n := range f.Notifiers {
		nw := fmt.Sprintf("notifiers[%s]", n.Key)
		if !keyRe.MatchString(n.Key) {
			bad(nw, "key must match %s", keyRe)
		}
		if notifierKeys[n.Key] {
			bad(nw, "duplicate notifier key")
		}
		notifierKeys[n.Key] = true
		if n.Type != NotifierDiscord && n.Type != NotifierTeams {
			bad(nw, "unknown type %q (use discord or teams)", n.Type)
		}
		if !envNameRe.MatchString(n.URLEnv) {
			bad(nw, "url_env must name an environment variable such as NOROSHI_WEBHOOK_TEAMS")
		}
		if n.Locale != "" && i18n.Normalize(n.Locale) != n.Locale {
			bad(nw, "locale must be one of %s", strings.Join(i18n.Supported, ", "))
		}
	}
	pageKeys := map[string]bool{}
	for pi := range f.Pages {
		p := &f.Pages[pi]
		pw := fmt.Sprintf("pages[%s]", p.Key)
		if !keyRe.MatchString(p.Key) {
			bad(pw, "key must match %s", keyRe)
		}
		if pageKeys[p.Key] {
			bad(pw, "duplicate page key")
		}
		pageKeys[p.Key] = true
		if p.Title.Get("en") == "" {
			bad(pw, "title is required")
		}
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			bad(pw, "unknown timezone %q", p.Timezone)
		}
		if p.Visibility != VisibilityPublic && p.Visibility != VisibilityPrivate {
			bad(pw, "visibility must be public or private")
		}
		if p.Logo != "" && !logoExts[strings.ToLower(filepath.Ext(p.Logo))] {
			bad(pw, "logo must be an svg, png, jpg, webp, gif, or ico file")
		}
		for _, k := range p.Notify {
			if !notifierKeys[k] {
				bad(pw, "notify: unknown notifier %q", k)
			}
		}
		groupKeys, compKeys := map[string]bool{}, map[string]bool{}
		for gi := range p.Groups {
			g := &p.Groups[gi]
			gw := fmt.Sprintf("%s.groups[%s]", pw, g.Key)
			if !keyRe.MatchString(g.Key) {
				bad(gw, "key must match %s", keyRe)
			}
			if groupKeys[g.Key] {
				bad(gw, "duplicate group key in page")
			}
			groupKeys[g.Key] = true
			if g.Name.Get("en") == "" {
				bad(gw, "name is required")
			}
			for ci := range g.Components {
				c := &g.Components[ci]
				cw := fmt.Sprintf("%s.components[%s]", gw, c.Key)
				if !keyRe.MatchString(c.Key) {
					bad(cw, "key must match %s", keyRe)
				}
				if compKeys[c.Key] {
					bad(cw, "duplicate component key in page")
				}
				compKeys[c.Key] = true
				if c.Name.Get("en") == "" {
					bad(cw, "name is required")
				}
				checkKeys := map[string]bool{}
				for ki := range c.Checks {
					k := &c.Checks[ki]
					kw := fmt.Sprintf("%s.checks[%s]", cw, k.Key)
					if !keyRe.MatchString(k.Key) {
						bad(kw, "key must match %s", keyRe)
					}
					if checkKeys[k.Key] {
						bad(kw, "duplicate check key in component")
					}
					checkKeys[k.Key] = true
					for _, e := range k.validate() {
						bad(kw, "%s", e)
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Check) validate() []string {
	var out []string
	if c.Interval.Duration < 10*time.Second {
		out = append(out, "interval must be at least 10s")
	}
	if c.Timeout.Duration <= 0 || c.Timeout.Duration >= c.Interval.Duration {
		out = append(out, "timeout must be positive and shorter than interval")
	}
	if c.FailThreshold < 1 || c.RecoverThreshold < 1 {
		out = append(out, "fail_threshold and recover_threshold must be at least 1")
	}
	switch c.Type {
	case TypeHTTP:
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			out = append(out, "url must be an absolute http or https URL")
		}
		c.expect = c.expect[:0]
		for _, s := range c.ExpectStatus {
			r, err := parseStatusRange(s)
			if err != nil {
				out = append(out, err.Error())
				continue
			}
			c.expect = append(c.expect, r)
		}
		if c.SlowAfter.Duration < 0 || (c.SlowAfter.Duration > 0 && c.SlowAfter.Duration >= c.Timeout.Duration) {
			out = append(out, "slow_after must be shorter than timeout")
		}
	case TypeTCP, TypeTLS:
		if _, _, err := net.SplitHostPort(c.Address); err != nil {
			out = append(out, "address must be host:port")
		}
	case TypeDomainExpiry:
		if c.Domain == "" || !strings.Contains(c.Domain, ".") || strings.ContainsAny(c.Domain, "/: ") {
			out = append(out, "domain must be a registered domain name such as example.com")
		}
	case TypeDocker:
		if !containerRe.MatchString(c.Container) {
			out = append(out, "container must be a container name or ID")
		}
		if err := checkDockerHost(c.DockerHost); err != nil {
			out = append(out, err.Error())
		}
	default:
		out = append(out, fmt.Sprintf("unknown type %q (use http, tcp, tls, domain_expiry, or docker)", c.Type))
	}
	return out
}

func parseStatusRange(s string) ([2]int, error) {
	s = strings.TrimSpace(s)
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		hi = lo
	}
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	b, err2 := strconv.Atoi(strings.TrimSpace(hi))
	if err1 != nil || err2 != nil || a < 100 || b > 599 || a > b {
		return [2]int{}, fmt.Errorf("invalid expect_status %q (use 200 or 200-299)", s)
	}
	return [2]int{a, b}, nil
}

// Thresholds returns the debounce settings as plain ints.
func (c Check) Thresholds() (fail, recover int) { return c.FailThreshold, c.RecoverThreshold }

// ExpectRanges exposes the parsed status ranges, mainly for tests.
func (c Check) ExpectRanges() [][2]int { return c.expect }

// Restore re-parses derived fields after the check was loaded from JSON.
func (c *Check) Restore() error {
	if errs := c.validate(); len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// checkDockerHost accepts unix:///path and tcp://host:port, as DOCKER_HOST does.
func checkDockerHost(h string) error {
	switch {
	case strings.HasPrefix(h, "unix://"):
		if !strings.HasPrefix(strings.TrimPrefix(h, "unix://"), "/") {
			return errors.New("docker_host unix socket path must be absolute, e.g. unix:///var/run/docker.sock")
		}
		return nil
	case strings.HasPrefix(h, "tcp://"):
		if _, _, err := net.SplitHostPort(strings.TrimPrefix(h, "tcp://")); err != nil {
			return errors.New("docker_host must be tcp://host:port")
		}
		return nil
	}
	return errors.New("docker_host must start with unix:// or tcp://")
}
