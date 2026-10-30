package store

import (
	"testing"
	"time"
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
