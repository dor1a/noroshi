// Package uptime turns a component's status history into daily uptime
// buckets. Time when no prober was running counts as no data, not as an
// outage.
package uptime

import (
	"sort"
	"time"

	"github.com/dor1a/noroshi/internal/judge"
)

// Event is a status change of one component.
type Event struct {
	At     time.Time
	Status judge.Status
}

// Interval is a half-open time range [Start, End).
type Interval struct {
	Start time.Time
	End   time.Time
}

// Day is one bucket in the page's time zone.
type Day struct {
	Date  time.Time // local midnight
	Up    time.Duration
	Down  time.Duration
	Maint time.Duration // maintenance time, left out of Up and Down
	Worst judge.Status  // worst status seen while covered, NoData if none
	Spans []Span        // outage and maintenance stretches, in time order
}

// Span is a stretch of one outage or maintenance status within a day. A
// status that carries on across a gap in prober coverage stays one span;
// Dur counts only the covered time, which is what uptime counts too.
type Span struct {
	Status  judge.Status
	Start   time.Time
	End     time.Time
	Dur     time.Duration
	Ongoing bool // still in this status when last seen, shortly before now

	event int // index of the status event the span comes from
}

// addSpan extends the last span when the same status carries on, either
// without a break or from the same event across a coverage gap.
func (d *Day) addSpan(st judge.Status, start, end time.Time, ongoing bool, event int) {
	if n := len(d.Spans); n > 0 {
		last := &d.Spans[n-1]
		if last.Status == st && (last.End.Equal(start) || last.event == event) {
			last.End, last.Ongoing = end, ongoing
			last.Dur += end.Sub(start)
			last.event = event
			return
		}
	}
	d.Spans = append(d.Spans, Span{Status: st, Start: start, End: end, Dur: end.Sub(start), Ongoing: ongoing, event: event})
}

// HasData reports whether the day has any time that counts toward uptime.
func (d Day) HasData() bool { return d.Up+d.Down > 0 }

// Percent is the share of counted time that was up, from 0 to 100.
func (d Day) Percent() float64 { return percent(d.Up, d.Down) }

// Summary holds the daily buckets and their totals.
type Summary struct {
	Days []Day
	Up   time.Duration
	Down time.Duration
}

// HasData reports whether any day has counted time.
func (s Summary) HasData() bool { return s.Up+s.Down > 0 }

// Percent is the share of counted time that was up across all days.
func (s Summary) Percent() float64 { return percent(s.Up, s.Down) }

func percent(up, down time.Duration) float64 {
	if up+down <= 0 {
		return 0
	}
	return float64(up) / float64(up+down) * 100
}

// counts reports how a status contributes to uptime.
// Operational and degraded time is up, partial and major outage time is
// down, and maintenance and unknown time is left out.
func counts(s judge.Status) (up, down bool) {
	switch s {
	case judge.Operational, judge.Degraded:
		return true, false
	case judge.Partial, judge.Major:
		return false, true
	default:
		return false, false
	}
}

// liveGap is how close to now the covered time must reach for a status that
// lasts to its end to count as still going. The prober touches its session
// every 15 seconds by default.
const liveGap = 2 * time.Minute

// Compute builds n daily buckets in loc, the last one containing now.
//
// events must be sorted by time and should include the last event before
// the first bucket so the window opens with a known status. covered lists
// the periods when a prober was running; only covered time is counted.
func Compute(events []Event, covered []Interval, loc *time.Location, now time.Time, n int) Summary {
	if n <= 0 {
		return Summary{}
	}
	local := now.In(loc)
	days := make([]Day, n)
	for i := range days {
		days[i] = Day{
			Date:  time.Date(local.Year(), local.Month(), local.Day()-(n-1)+i, 0, 0, 0, 0, loc),
			Worst: judge.NoData,
		}
	}
	windowStart := days[0].Date
	windowEnd := now
	dayEnd := func(i int) time.Time {
		if i+1 < n {
			return days[i+1].Date
		}
		return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
	}

	cov := merge(clip(covered, windowStart, windowEnd))
	// A status that runs to the end of covered time is still going, as far
	// as anyone knows, when that end is recent.
	var liveEnd time.Time
	if n := len(cov); n > 0 && windowEnd.Sub(cov[n-1].End) <= liveGap {
		liveEnd = cov[n-1].End
	}

	for i, e := range events {
		segStart := e.At
		segEnd := windowEnd
		if i+1 < len(events) {
			segEnd = events[i+1].At
		}
		if segStart.Before(windowStart) {
			segStart = windowStart
		}
		if segEnd.After(windowEnd) {
			segEnd = windowEnd
		}
		if !segEnd.After(segStart) {
			continue
		}
		up, down := counts(e.Status)
		last := i == len(events)-1
		for _, c := range cov {
			ps, pe := maxTime(segStart, c.Start), minTime(segEnd, c.End)
			if !pe.After(ps) {
				continue
			}
			for d := range days {
				ds, de := maxTime(ps, days[d].Date), minTime(pe, dayEnd(d))
				if !de.After(ds) {
					continue
				}
				dur := de.Sub(ds)
				switch {
				case up:
					days[d].Up += dur
				case down:
					days[d].Down += dur
					days[d].addSpan(e.Status, ds, de, last && de.Equal(liveEnd), i)
				case e.Status == judge.Maintenance:
					days[d].Maint += dur
					days[d].addSpan(e.Status, ds, de, last && de.Equal(liveEnd), i)
				}
				if e.Status.Severity() > days[d].Worst.Severity() {
					days[d].Worst = e.Status
				}
			}
		}
	}

	s := Summary{Days: days}
	for _, d := range days {
		s.Up += d.Up
		s.Down += d.Down
	}
	return s
}

func clip(in []Interval, start, end time.Time) []Interval {
	out := make([]Interval, 0, len(in))
	for _, iv := range in {
		s, e := maxTime(iv.Start, start), minTime(iv.End, end)
		if e.After(s) {
			out = append(out, Interval{Start: s, End: e})
		}
	}
	return out
}

func merge(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Start.Before(in[j].Start) })
	out := []Interval{in[0]}
	for _, iv := range in[1:] {
		last := &out[len(out)-1]
		if !iv.Start.After(last.End) {
			if iv.End.After(last.End) {
				last.End = iv.End
			}
			continue
		}
		out = append(out, iv)
	}
	return out
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Outage is a stretch of partial or major outage of one component. End is
// nil while it lasts.
type Outage struct {
	Start time.Time
	End   *time.Time
	Worst judge.Status
}

// Outages lists the outages in events that were still going at from or
// began after it, oldest first. events must be sorted by time; partial and
// major outage in a row are one outage at its worst status.
func Outages(events []Event, from time.Time) []Outage {
	var out []Outage
	var cur *Outage
	for _, e := range events {
		_, down := counts(e.Status)
		switch {
		case down && cur == nil:
			cur = &Outage{Start: e.At, Worst: e.Status}
		case down:
			if e.Status.Severity() > cur.Worst.Severity() {
				cur.Worst = e.Status
			}
		case cur != nil:
			end := e.At
			cur.End = &end
			if end.After(from) {
				out = append(out, *cur)
			}
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}
