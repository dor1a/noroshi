package uptime

import (
	"reflect"
	"testing"
	"time"

	"github.com/dor1a/noroshi/internal/judge"
)

func TestApply(t *testing.T) {
	checks := []Event{
		{At: at(1, 0, 0), Status: judge.Operational},
		{At: at(11, 10, 0), Status: judge.Major},
		{At: at(11, 11, 0), Status: judge.Operational},
	}

	t.Run("no overrides returns the events", func(t *testing.T) {
		if got := Apply(checks, nil); !reflect.DeepEqual(got, checks) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("override hides a passing check and ends", func(t *testing.T) {
		got := Apply(checks, []Override{{Start: at(12, 9, 0), End: at(12, 10, 0), Status: judge.Partial}})
		want := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 10, 0), Status: judge.Major},
			{At: at(11, 11, 0), Status: judge.Operational},
			{At: at(12, 9, 0), Status: judge.Partial},
			{At: at(12, 10, 0), Status: judge.Operational},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("open override replaces check changes while it lasts", func(t *testing.T) {
		got := Apply(checks, []Override{{Start: at(11, 9, 0), Status: judge.Maintenance}})
		want := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 9, 0), Status: judge.Maintenance},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("overlapping overrides take the worst", func(t *testing.T) {
		got := Apply(checks, []Override{
			{Start: at(12, 9, 0), End: at(12, 12, 0), Status: judge.Degraded},
			{Start: at(12, 10, 0), End: at(12, 11, 0), Status: judge.Major},
		})
		want := []Event{
			{At: at(1, 0, 0), Status: judge.Operational},
			{At: at(11, 10, 0), Status: judge.Major},
			{At: at(11, 11, 0), Status: judge.Operational},
			{At: at(12, 9, 0), Status: judge.Degraded},
			{At: at(12, 10, 0), Status: judge.Major},
			{At: at(12, 11, 0), Status: judge.Degraded},
			{At: at(12, 12, 0), Status: judge.Operational},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("override before any check data", func(t *testing.T) {
		got := Apply(nil, []Override{{Start: at(12, 9, 0), End: at(12, 10, 0), Status: judge.Major}})
		want := []Event{
			{At: at(12, 9, 0), Status: judge.Major},
			{At: at(12, 10, 0), Status: judge.NoData},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("declared outage counts against uptime", func(t *testing.T) {
		now := at(12, 24, 0).Add(-time.Nanosecond)
		full := []Interval{{Start: at(1, 0, 0), End: now}}
		ev := Apply([]Event{{At: at(1, 0, 0), Status: judge.Operational}},
			[]Override{{Start: at(12, 0, 0), End: at(12, 12, 0), Status: judge.Major}})
		s := Compute(ev, full, kst, now, 1)
		if !near(s.Percent(), 50) {
			t.Fatalf("got %.4f", s.Percent())
		}
	})
}
