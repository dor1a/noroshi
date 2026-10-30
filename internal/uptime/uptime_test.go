package uptime

import (
	"math"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/judge"
)

var kst = time.FixedZone("KST", 9*60*60)

func at(day, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, kst)
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCompute(t *testing.T) {
	now := at(12, 24, 0).Add(-time.Nanosecond) // end of 2026-10-12 KST
	full := []Interval{{Start: at(1, 0, 0), End: now}}

	t.Run("operational all along is 100 percent", func(t *testing.T) {
		s := Compute([]Event{{At: at(1, 0, 0), Status: judge.Operational}}, full, kst, now, 3)
		if !s.HasData() || !near(s.Percent(), 100) {
			t.Fatalf("got %v %.4f", s.HasData(), s.Percent())
		}
		for _, d := range s.Days {
			if d.Worst != judge.Operational {
				t.Fatalf("day %s worst %s", d.Date, d.Worst)
			}
		}
	})

	t.Run("one hour major outage on one day", func(t *testing.T) {
		ev := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 10, 0), Status: judge.Major},
			{At: at(11, 11, 0), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 3)
		if got := s.Days[1].Percent(); !near(got, 23.0/24*100) {
			t.Fatalf("day 11 got %.4f", got)
		}
		if s.Days[1].Worst != judge.Major {
			t.Fatalf("day 11 worst %s", s.Days[1].Worst)
		}
		if !near(s.Days[0].Percent(), 100) || !near(s.Days[2].Percent(), 100) {
			t.Fatal("other days should be 100")
		}
	})

	t.Run("partial outage counts as down", func(t *testing.T) {
		ev := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(12, 0, 0), Status: judge.Partial},
			{At: at(12, 6, 0), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 1)
		if got := s.Days[0].Percent(); !near(got, 75) {
			t.Fatalf("got %.4f", got)
		}
	})

	t.Run("degraded counts as up", func(t *testing.T) {
		ev := []Event{{At: at(1, 0, 0), Status: judge.Degraded}}
		s := Compute(ev, full, kst, now, 1)
		if !near(s.Percent(), 100) || s.Days[0].Worst != judge.Degraded {
			t.Fatalf("got %.4f worst %s", s.Percent(), s.Days[0].Worst)
		}
	})

	t.Run("maintenance is left out", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 0, 0), Status: judge.Maintenance},
			{At: at(12, 12, 0), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 1)
		d := s.Days[0]
		if d.Up != 12*time.Hour-time.Nanosecond || d.Down != 0 || !near(d.Percent(), 100) {
			t.Fatalf("up %s down %s", d.Up, d.Down)
		}
		if d.Worst != judge.Maintenance || d.Maint != 12*time.Hour {
			t.Fatalf("worst %s maint %s", d.Worst, d.Maint)
		}
	})

	t.Run("time without a prober is no data", func(t *testing.T) {
		ev := []Event{{At: at(1, 0, 0), Status: judge.Major}}
		cov := []Interval{{Start: at(12, 0, 0), End: now}}
		s := Compute(ev, cov, kst, now, 3)
		if s.Days[0].HasData() || s.Days[1].HasData() {
			t.Fatal("uncovered days should have no data")
		}
		if s.Days[0].Worst != judge.NoData {
			t.Fatalf("uncovered day worst %s", s.Days[0].Worst)
		}
		if !s.Days[2].HasData() || !near(s.Days[2].Percent(), 0) {
			t.Fatal("covered major day should be 0 percent")
		}
	})

	t.Run("gap in coverage is not counted", func(t *testing.T) {
		ev := []Event{{At: at(1, 0, 0), Status: judge.Operational}}
		cov := []Interval{
			{Start: at(12, 0, 0), End: at(12, 6, 0)},
			{Start: at(12, 18, 0), End: now},
		}
		s := Compute(ev, cov, kst, now, 1)
		want := 12*time.Hour - time.Nanosecond
		if s.Days[0].Up != want {
			t.Fatalf("up %s, want %s", s.Days[0].Up, want)
		}
	})

	t.Run("overlapping coverage is counted once", func(t *testing.T) {
		ev := []Event{{At: at(1, 0, 0), Status: judge.Operational}}
		cov := []Interval{
			{Start: at(12, 0, 0), End: at(12, 10, 0)},
			{Start: at(12, 5, 0), End: at(12, 12, 0)},
		}
		s := Compute(ev, cov, kst, now, 1)
		if s.Days[0].Up != 12*time.Hour {
			t.Fatalf("up %s", s.Days[0].Up)
		}
	})

	t.Run("event before the window carries in", func(t *testing.T) {
		ev := []Event{{At: at(1, 0, 0), Status: judge.Major}}
		s := Compute(ev, full, kst, now, 1)
		if !near(s.Percent(), 0) || s.Days[0].Worst != judge.Major {
			t.Fatalf("got %.4f worst %s", s.Percent(), s.Days[0].Worst)
		}
	})

	t.Run("no events is no data", func(t *testing.T) {
		s := Compute(nil, full, kst, now, 3)
		if s.HasData() {
			t.Fatal("expected no data")
		}
	})

	t.Run("outage across midnight splits between days", func(t *testing.T) {
		ev := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 23, 30), Status: judge.Major},
			{At: at(12, 0, 30), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 2)
		if s.Days[0].Down != 30*time.Minute || s.Days[1].Down != 30*time.Minute {
			t.Fatalf("down %s / %s", s.Days[0].Down, s.Days[1].Down)
		}
	})

	t.Run("today only counts up to now", func(t *testing.T) {
		mid := at(12, 12, 0)
		ev := []Event{{At: at(1, 0, 0), Status: judge.Operational}}
		s := Compute(ev, []Interval{{Start: at(1, 0, 0), End: mid.Add(time.Hour)}}, kst, mid, 1)
		if s.Days[0].Up != 12*time.Hour {
			t.Fatalf("up %s", s.Days[0].Up)
		}
	})
}

func TestComputeSpans(t *testing.T) {
	now := at(12, 24, 0).Add(-time.Nanosecond)
	full := []Interval{{Start: at(1, 0, 0), End: now}}

	t.Run("an outage is one span on its day", func(t *testing.T) {
		ev := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 22, 36), Status: judge.Major},
			{At: at(11, 22, 47), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 3)
		got := s.Days[1].Spans
		if len(got) != 1 || got[0].Status != judge.Major || !got[0].Start.Equal(at(11, 22, 36)) || !got[0].End.Equal(at(11, 22, 47)) || got[0].Ongoing {
			t.Fatalf("day 11 spans %+v", got)
		}
		if len(s.Days[0].Spans) != 0 || len(s.Days[2].Spans) != 0 {
			t.Fatal("other days should have no spans")
		}
	})

	t.Run("partial then major are separate spans", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 1, 0), Status: judge.Partial},
			{At: at(12, 1, 10), Status: judge.Major},
			{At: at(12, 1, 20), Status: judge.Operational},
		}
		got := Compute(ev, full, kst, now, 1).Days[0].Spans
		if len(got) != 2 || got[0].Status != judge.Partial || got[1].Status != judge.Major || !got[0].End.Equal(got[1].Start) {
			t.Fatalf("spans %+v", got)
		}
	})

	t.Run("an outage across midnight splits by day", func(t *testing.T) {
		ev := []Event{
			{At: at(10, 23, 50), Status: judge.Major},
			{At: at(11, 0, 10), Status: judge.Operational},
		}
		s := Compute(ev, full, kst, now, 3)
		a, b := s.Days[0].Spans, s.Days[1].Spans
		if len(a) != 1 || !a[0].End.Equal(at(11, 0, 0)) || len(b) != 1 || !b[0].Start.Equal(at(11, 0, 0)) || !b[0].End.Equal(at(11, 0, 10)) {
			t.Fatalf("day 10 %+v, day 11 %+v", a, b)
		}
	})

	t.Run("a prober gap inside one status keeps one span", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 1, 0), Status: judge.Major},
			{At: at(12, 2, 0), Status: judge.Operational},
		}
		cov := []Interval{{Start: at(12, 0, 0), End: at(12, 1, 20)}, {Start: at(12, 1, 30), End: now}}
		got := Compute(ev, cov, kst, now, 1).Days[0].Spans
		if len(got) != 1 || !got[0].Start.Equal(at(12, 1, 0)) || !got[0].End.Equal(at(12, 2, 0)) || got[0].Dur != 50*time.Minute {
			t.Fatalf("spans %+v", got)
		}
	})

	t.Run("a recovery between two outages keeps them apart", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 1, 0), Status: judge.Major},
			{At: at(12, 1, 5), Status: judge.Operational},
			{At: at(12, 1, 6), Status: judge.Major},
			{At: at(12, 1, 9), Status: judge.Operational},
		}
		got := Compute(ev, full, kst, now, 1).Days[0].Spans
		if len(got) != 2 || got[0].Dur != 5*time.Minute || got[1].Dur != 3*time.Minute {
			t.Fatalf("spans %+v", got)
		}
	})

	t.Run("an outage still going is marked ongoing", func(t *testing.T) {
		ev := []Event{{At: at(12, 23, 0), Status: judge.Major}}
		got := Compute(ev, full, kst, now, 1).Days[0].Spans
		if len(got) != 1 || !got[0].Ongoing || !got[0].End.Equal(now) {
			t.Fatalf("spans %+v", got)
		}
		// The session is touched every few seconds, so covered time ends a
		// little before now while the prober is running.
		lag := []Interval{{Start: at(1, 0, 0), End: now.Add(-15 * time.Second)}}
		if got := Compute(ev, lag, kst, now, 1).Days[0].Spans; len(got) != 1 || !got[0].Ongoing {
			t.Fatalf("lagging session %+v", got)
		}
		stopped := []Interval{{Start: at(1, 0, 0), End: at(12, 23, 30)}}
		if got := Compute(ev, stopped, kst, now, 1).Days[0].Spans; len(got) != 1 || got[0].Ongoing {
			t.Fatalf("stopped prober %+v", got)
		}
	})

	t.Run("an ended outage before a live session end is not ongoing", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 23, 0), Status: judge.Major},
			{At: at(12, 23, 10), Status: judge.Operational},
		}
		if got := Compute(ev, full, kst, now, 1).Days[0].Spans; len(got) != 1 || got[0].Ongoing {
			t.Fatalf("spans %+v", got)
		}
	})

	t.Run("maintenance is a span too", func(t *testing.T) {
		ev := []Event{
			{At: at(12, 2, 0), Status: judge.Maintenance},
			{At: at(12, 3, 0), Status: judge.Operational},
		}
		got := Compute(ev, full, kst, now, 1).Days[0].Spans
		if len(got) != 1 || got[0].Status != judge.Maintenance {
			t.Fatalf("spans %+v", got)
		}
	})

	t.Run("degraded leaves no span", func(t *testing.T) {
		ev := []Event{{At: at(12, 0, 0), Status: judge.Degraded}}
		if got := Compute(ev, full, kst, now, 1).Days[0].Spans; len(got) != 0 {
			t.Fatalf("spans %+v", got)
		}
	})
}

func TestOutages(t *testing.T) {
	ev := []Event{
		{At: at(10, 0, 0), Status: judge.Operational},
		{At: at(10, 1, 0), Status: judge.Major},
		{At: at(10, 1, 5), Status: judge.Operational},
		{At: at(11, 22, 36), Status: judge.Partial},
		{At: at(11, 22, 40), Status: judge.Major},
		{At: at(11, 22, 47), Status: judge.Degraded},
		{At: at(12, 9, 0), Status: judge.Major},
	}
	got := Outages(ev, at(11, 0, 0))
	if len(got) != 2 {
		t.Fatalf("outages %+v", got)
	}
	if a := got[0]; !a.Start.Equal(at(11, 22, 36)) || a.End == nil || !a.End.Equal(at(11, 22, 47)) || a.Worst != judge.Major {
		t.Fatalf("first %+v", a)
	}
	if b := got[1]; !b.Start.Equal(at(12, 9, 0)) || b.End != nil {
		t.Fatalf("ongoing %+v", b)
	}
	// One that began before from and ended after it still counts.
	if got := Outages(ev[:3], at(10, 1, 2)); len(got) != 1 {
		t.Fatalf("overlapping %+v", got)
	}
}
