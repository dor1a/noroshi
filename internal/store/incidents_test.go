package store

import (
	"slices"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/i18n"
)

func TestMaintenancePhase(t *testing.T) {
	start := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	m := Incident{Kind: KindMaintenance, StartedAt: start, ResolvedAt: &end}
	for _, c := range []struct {
		at     time.Time
		phase  string
		active bool
	}{
		{start.Add(-time.Minute), PhaseUpcoming, false},
		{start, PhaseActive, true},
		{end.Add(-time.Second), PhaseActive, true},
		{end, PhaseDone, false},
	} {
		if got := m.Phase(c.at); got != c.phase || m.Active(c.at) != c.active {
			t.Errorf("%s: %s %v", c.at, got, m.Active(c.at))
		}
	}
	// An incident is active while open, whatever the time.
	in := Incident{Kind: KindIncident, StartedAt: start}
	if !in.Active(start.Add(-time.Hour)) || in.Maintenance() {
		t.Fatal("open incident")
	}
	in.ResolvedAt = &end
	if in.Active(start) {
		t.Fatal("resolved incident")
	}
}

func TestIncidentAcrossPages(t *testing.T) {
	in := Incident{
		ID: 1, PageID: 1, PageKey: "public", PageTitle: i18n.Text{"*": "Public"},
		Components: []IncidentComponent{
			{ComponentID: 10, PageKey: "public", Key: "plex"},
			{ComponentID: 20, PageKey: "internal", Key: "router"},
			{ComponentID: 21, PageKey: "internal", Key: "ap"},
		},
	}
	if got := in.PageKeys(); !slices.Equal(got, []string{"public", "internal"}) {
		t.Fatalf("pages %v", got)
	}
	if !in.OnPage("internal") || in.OnPage("other") {
		t.Fatal("OnPage")
	}
	v := in.ForPage(PageRow{ID: 2, Key: "internal", Title: i18n.Text{"*": "Internal"}})
	if v.PageKey != "internal" || v.PageID != 2 || len(v.Components) != 2 || v.Components[0].Key != "router" {
		t.Fatalf("internal view %+v", v)
	}
	if len(in.Components) != 3 || in.PageKey != "public" {
		t.Fatal("ForPage changed the incident it was called on")
	}
	// Its own page lists it even without a component there.
	own := Incident{PageKey: "public", Components: []IncidentComponent{{PageKey: "internal"}}}
	if !own.OnPage("public") || len(own.ForPage(PageRow{Key: "public"}).Components) != 0 {
		t.Fatal("own page")
	}
}
