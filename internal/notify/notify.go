// Package notify posts incident announcements to chat webhooks: Discord
// embeds, and Microsoft Teams Workflows webhooks with an Adaptive Card.
// Webhook URLs carry their credential, so they never appear in errors.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
)

// UserAgent is sent with every webhook request.
var UserAgent = "noroshi"

// Target is one configured channel.
type Target struct {
	Key    string
	Type   string
	URL    string
	Locale string
}

// Component is an affected component, already named in the target's language.
type Component struct {
	Name   string
	Status judge.Status
}

// Message is one announcement. Page, Title, Body, and component names are
// already in the target's language; this package adds the labels.
type Message struct {
	Page       string
	Title      string
	Stage      string
	Body       string
	URL        string
	At         time.Time
	Components []Component
}

func (m Message) tone() judge.Status {
	if m.Stage == "resolved" {
		return judge.Operational
	}
	var st []judge.Status
	for _, c := range m.Components {
		st = append(st, c.Status)
	}
	return judge.Worst(st)
}

func (m Message) heading(locale string) string {
	return "[" + i18n.T(locale, "incident.stage."+m.Stage) + "] " + m.Title
}

// Payload renders the request body for a target.
func Payload(t Target, m Message) ([]byte, error) {
	switch t.Type {
	case config.NotifierDiscord:
		return discordPayload(t.Locale, m)
	case config.NotifierTeams:
		return teamsPayload(t.Locale, m)
	}
	return nil, fmt.Errorf("unknown notifier type %q", t.Type)
}

var discordColors = map[judge.Status]int{
	judge.Operational: 0x40a02b,
	judge.Degraded:    0xdf8e1d,
	judge.Partial:     0xfe640b,
	judge.Major:       0xd20f39,
	judge.Maintenance: 0x1e66f5,
	judge.NoData:      0x7c7f93,
}

// Discord limits: title 256, description 4096, field value 1024, and 6000
// characters per message in total.
func discordPayload(locale string, m Message) ([]byte, error) {
	type field struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	type footer struct {
		Text string `json:"text"`
	}
	type embed struct {
		Title       string  `json:"title"`
		URL         string  `json:"url,omitempty"`
		Description string  `json:"description,omitempty"`
		Color       int     `json:"color"`
		Fields      []field `json:"fields,omitempty"`
		Footer      *footer `json:"footer,omitempty"`
		Timestamp   string  `json:"timestamp"`
	}
	e := embed{
		Title:       truncate(m.heading(locale), 256),
		URL:         m.URL,
		Description: truncate(m.Body, 3500),
		Color:       discordColors[m.tone()],
		Timestamp:   m.At.UTC().Format(time.RFC3339),
	}
	if lines := componentLines(locale, m); lines != "" {
		e.Fields = []field{{Name: i18n.T(locale, "incident.affected"), Value: truncate(lines, 1024)}}
	}
	if m.Page != "" {
		e.Footer = &footer{Text: truncate(m.Page, 256)}
	}
	// No pings: an incident body must not reach @everyone by accident.
	return json.Marshal(map[string]any{
		"embeds":           []embed{e},
		"allowed_mentions": map[string][]string{"parse": {}},
	})
}

var teamsColors = map[judge.Status]string{
	judge.Operational: "Good",
	judge.Degraded:    "Warning",
	judge.Partial:     "Warning",
	judge.Major:       "Attention",
	judge.Maintenance: "Accent",
	judge.NoData:      "Default",
}

// Teams Workflows webhooks take the message activity format with an
// Adaptive Card attachment. Messages are limited to 28 KB.
func teamsPayload(locale string, m Message) ([]byte, error) {
	body := []map[string]any{}
	if m.Page != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": m.Page, "size": "Small", "isSubtle": true, "wrap": true})
	}
	body = append(body,
		map[string]any{"type": "TextBlock", "text": m.heading(locale), "size": "Medium", "weight": "Bolder", "color": teamsColors[m.tone()], "wrap": true},
		// A single newline does not break a line in an Adaptive Card.
		map[string]any{"type": "TextBlock", "text": strings.ReplaceAll(truncate(m.Body, 8000), "\n", "\n\n"), "wrap": true},
	)
	if len(m.Components) > 0 {
		var facts []map[string]string
		for _, c := range m.Components {
			facts = append(facts, map[string]string{
				"title": c.Status.Glyph() + " " + c.Name,
				"value": i18n.T(locale, "status."+string(c.Status)),
			})
		}
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}
	if m.URL != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": "[" + i18n.T(locale, "incident.view") + "](" + m.URL + ")", "wrap": true})
	}
	return json.Marshal(map[string]any{
		"type": "message",
		"attachments": []map[string]any{{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"contentUrl":  nil,
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body":    body,
				"msteams": map[string]string{"width": "Full"},
			},
		}},
	})
}

func componentLines(locale string, m Message) string {
	var b strings.Builder
	for _, c := range m.Components {
		fmt.Fprintf(&b, "%s %s: %s\n", c.Status.Glyph(), c.Name, i18n.T(locale, "status."+string(c.Status)))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Result is the outcome of one delivery.
type Result struct {
	OK       bool
	Attempts int
	Detail   string
}

// Sender posts payloads with retries. Requests honour HTTPS_PROXY.
type Sender struct {
	client  *http.Client
	backoff []time.Duration
}

// NewSender returns a sender that tries three times.
func NewSender() *Sender {
	return &Sender{
		client:  &http.Client{Timeout: 15 * time.Second, Transport: http.DefaultTransport.(*http.Transport).Clone()},
		backoff: []time.Duration{2 * time.Second, 6 * time.Second},
	}
}

// Send delivers one message, retrying network errors, 429, and 5xx.
func (s *Sender) Send(ctx context.Context, t Target, m Message) Result {
	body, err := Payload(t, m)
	if err != nil {
		return Result{Detail: err.Error()}
	}
	var res Result
	for attempt := 0; ; attempt++ {
		res.Attempts = attempt + 1
		retry, wait, detail := s.post(ctx, t.URL, body)
		if detail == "" {
			return Result{OK: true, Attempts: res.Attempts}
		}
		res.Detail = detail
		if !retry || attempt >= len(s.backoff) {
			return res
		}
		if wait == 0 {
			wait = s.backoff[attempt]
		}
		select {
		case <-ctx.Done():
			return res
		case <-time.After(wait):
		}
	}
}

func (s *Sender) post(ctx context.Context, u string, body []byte) (retry bool, wait time.Duration, detail string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return false, 0, "invalid webhook URL"
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		return true, 0, redact(err)
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	if resp.StatusCode/100 == 2 {
		return false, 0, ""
	}
	detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	if txt := strings.Join(strings.Fields(string(snippet)), " "); txt != "" {
		detail += ": " + txt
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true, retryAfter(resp.Header.Get("Retry-After")), detail
	case resp.StatusCode >= 500:
		return true, 0, detail
	}
	return false, 0, detail
}

// redact drops the URL that net/http puts in its errors.
func redact(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + ": " + ue.Err.Error()
	}
	return "request failed"
}

func retryAfter(v string) time.Duration {
	secs, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs*float64(time.Second)), 30*time.Second)
}
