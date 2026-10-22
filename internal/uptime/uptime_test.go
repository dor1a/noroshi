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
