package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RDAP looks up domain expiry dates. It finds the registry's RDAP server
// through the IANA bootstrap file and caches that file for a day.
type RDAP struct {
	BootstrapURL string
	Client       *http.Client

	mu      sync.Mutex
	servers map[string]string // tld -> base URL ending in "/"
	fetched time.Time
}

// NewRDAP returns a client using the IANA bootstrap file.
func NewRDAP() *RDAP {
	return &RDAP{
		BootstrapURL: "https://data.iana.org/rdap/dns.json",
		Client:       &http.Client{Timeout: 30 * time.Second},
	}
}

// Expiry returns the registration expiry of a domain.
func (r *RDAP) Expiry(ctx context.Context, domain string) (time.Time, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	i := strings.LastIndexByte(domain, '.')
	if i < 0 {
		return time.Time{}, fmt.Errorf("not a domain: %q", domain)
	}
	base, err := r.server(ctx, domain[i+1:])
	if err != nil {
		return time.Time{}, err
	}
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := r.getJSON(ctx, base+"domain/"+domain, &doc); err != nil {
		return time.Time{}, err
	}
	for _, e := range doc.Events {
		if e.Action == "expiration" {
			t, err := time.Parse(time.RFC3339, e.Date)
			if err != nil {
				return time.Time{}, fmt.Errorf("bad expiration date %q", e.Date)
			}
			return t, nil
		}
	}
	return time.Time{}, errors.New("no expiration event in RDAP response")
}

func (r *RDAP) server(ctx context.Context, tld string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.servers == nil || time.Since(r.fetched) > 24*time.Hour {
		var boot struct {
			Services [][][]string `json:"services"`
		}
		if err := r.getJSON(ctx, r.BootstrapURL, &boot); err != nil {
			if r.servers == nil {
				return "", fmt.Errorf("bootstrap: %w", err)
			}
			// Keep using the stale table if the refresh fails.
		} else {
			servers := map[string]string{}
			for _, svc := range boot.Services {
				if len(svc) < 2 || len(svc[1]) == 0 {
					continue
				}
				url := pickURL(svc[1])
				for _, t := range svc[0] {
					servers[strings.ToLower(t)] = url
				}
			}
			r.servers, r.fetched = servers, time.Now()
		}
	}
	base, ok := r.servers[tld]
	if !ok {
		return "", fmt.Errorf("no RDAP server for .%s", tld)
	}
	return base, nil
}

func pickURL(urls []string) string {
	for _, u := range urls {
		if strings.HasPrefix(u, "https://") {
			return ensureSlash(u)
		}
	}
	return ensureSlash(urls[0])
}

func ensureSlash(u string) string {
	if strings.HasSuffix(u, "/") {
		return u
	}
	return u + "/"
}

func (r *RDAP) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := r.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}
