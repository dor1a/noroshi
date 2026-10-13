package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/judge"
)

var msg = Message{
	Page:  "Status",
	Title: "Database unreachable",
	Stage: "investigating",
	Body:  "We are looking into it.\n@everyone stay calm",
	URL:   "https://status.example.com/p/home/incidents/7",
	At:    time.Date(2026, 10, 13, 3, 0, 0, 0, time.UTC),
	Components: []Component{
		{Name: "Postgres", Status: judge.Major},
		{Name: "Web", Status: judge.Degraded},
	},
}

func TestDiscordPayload(t *testing.T) {
	b, err := Payload(Target{Type: config.NotifierDiscord, Locale: "en"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Embeds []struct {
			Title, URL, Description string
			Color                   int
			Fields                  []struct{ Name, Value string }
		}
		AllowedMentions struct {
			Parse []string `json:"parse"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"parse":[]`) {
		t.Fatalf("mentions must be disabled: %s", b)
	}
	e := got.Embeds[0]
	if e.Title != "[Investigating] Database unreachable" || e.Color != discordColors[judge.Major] || e.URL != msg.URL {
		t.Fatalf("embed: %+v", e)
	}
	if len(e.Fields) != 1 || !strings.Contains(e.Fields[0].Value, "× Postgres: Major outage") {
		t.Fatalf("fields: %+v", e.Fields)
	}

	resolved := msg
	resolved.Stage = "resolved"
	b, _ = Payload(Target{Type: config.NotifierDiscord, Locale: "ko"}, resolved)
	if !strings.Contains(string(b), "[해결됨]") || !strings.Contains(string(b), `"color":4235307`) {
		t.Fatalf("resolved: %s", b)
	}
}

func TestTeamsPayload(t *testing.T) {
	b, err := Payload(Target{Type: config.NotifierTeams, Locale: "ko"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Type        string
		Attachments []struct {
			ContentType string
			Content     struct {
				Type, Version string
				Body          []map[string]any
			}
		}
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	a := got.Attachments[0]
	if got.Type != "message" || a.ContentType != "application/vnd.microsoft.card.adaptive" || a.Content.Type != "AdaptiveCard" {
		t.Fatalf("envelope: %s", b)
	}
	s := string(b)
	for _, want := range []string{"[조사 중] Database unreachable", "주요 장애", "FactSet", `"color":"Attention"`, "(https://status.example.com/p/home/incidents/7)"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %s", want, s)
		}
	}
}

func TestSend(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/flaky") && calls.Add(1) == 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case strings.HasSuffix(r.URL.Path, "/bad"):
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"message": "Invalid Form Body"}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	s := NewSender()
	s.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	send := func(u string) Result {
		return s.Send(context.Background(), Target{Type: config.NotifierDiscord, URL: u}, msg)
	}

	if r := send(srv.URL + "/ok"); !r.OK || r.Attempts != 1 {
		t.Fatalf("ok: %+v", r)
	}
	if r := send(srv.URL + "/flaky"); !r.OK || r.Attempts != 2 {
		t.Fatalf("flaky: %+v", r)
	}
	if r := send(srv.URL + "/bad"); r.OK || r.Attempts != 1 || r.Detail != `HTTP 400: {"message": "Invalid Form Body"}` {
		t.Fatalf("bad: %+v", r)
	}
	r := send("http://127.0.0.1:1/api/webhooks/123/secret-token")
	if r.OK || r.Attempts != 3 || strings.Contains(r.Detail, "secret-token") {
		t.Fatalf("unreachable: %+v", r)
	}
}
