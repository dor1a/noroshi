package uptime

import (
	"sort"
	"time"

	"github.com/dor1a/noroshi/internal/judge"
)

// Override is a status declared by hand for a period, such as by an
// incident. End is zero while it still applies.
type Override struct {
	Start  time.Time
	End    time.Time
	Status judge.Status
}

func (o Override) activeAt(t time.Time) bool {
	return !t.Before(o.Start) && (o.End.IsZero() || t.Before(o.End))
}

// Apply merges overrides into a component's check events. While an override
// applies, its status replaces the check status (the worst one if several
// overlap); otherwise the check status shows through. events must be sorted
// by time, and so is the result.
func Apply(events []Event, overrides []Override) []Event {
	if len(overrides) == 0 {
		return events
	}
	var points []time.Time
	for _, e := range events {
		points = append(points, e.At)
	}
	for _, o := range overrides {
		points = append(points, o.Start)
		if !o.End.IsZero() {
			points = append(points, o.End)
		}
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })

	checkAt := func(t time.Time) judge.Status {
		i := sort.Search(len(events), func(i int) bool { return events[i].At.After(t) })
		if i == 0 {
			return judge.NoData
		}
		return events[i-1].Status
	}

	var out []Event
	for i, t := range points {
		if i > 0 && t.Equal(points[i-1]) {
			continue
		}
		var active []judge.Status
		for _, o := range overrides {
			if o.activeAt(t) {
				active = append(active, o.Status)
			}
		}
		st := checkAt(t)
		if len(active) > 0 {
			st = judge.Worst(active)
		}
		if n := len(out); n == 0 || out[n-1].Status != st {
			out = append(out, Event{At: t, Status: st})
		}
	}
	return out
}
