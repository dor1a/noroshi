// Package judge turns raw probe outcomes into debounced check states and
// rolls check states up into a component status. It has no I/O so the rules
// can be pinned down with table tests.
package judge

// CheckState is the debounced state of a single check.
type CheckState string

const (
	Unknown CheckState = "unknown"
	Up      CheckState = "up"
	Down    CheckState = "down"
)

// Status is the public status of a component or a page.
type Status string

const (
	Operational Status = "operational"
	Degraded    Status = "degraded_performance"
	Partial     Status = "partial_outage"
	Major       Status = "major_outage"
	Maintenance Status = "under_maintenance"
	NoData      Status = "unknown"
)

// AllStatuses lists every status in display order, worst last.
var AllStatuses = []Status{NoData, Operational, Maintenance, Degraded, Partial, Major}

// Severity orders statuses so the worst one can be picked. NoData ranks
// lowest so that a single silent component does not mask real problems.
func (s Status) Severity() int {
	switch s {
	case Major:
		return 5
	case Partial:
		return 4
	case Degraded:
		return 3
	case Maintenance:
		return 2
	case Operational:
		return 1
	default:
		return 0
	}
}

// State is what the prober keeps per check between runs.
type State struct {
	State           CheckState
	Degraded        bool
	ConsecutiveFail int
	ConsecutiveOK   int
}

// Result is one probe outcome as far as the state machine cares.
type Result struct {
	OK   bool
	Slow bool
	// Skip marks an outcome that could not decide anything, such as a
	// failed RDAP lookup. The state is left untouched.
	Skip bool
}

// Thresholds control debouncing.
type Thresholds struct {
	Fail    int // consecutive failures before a check goes down
	Recover int // consecutive successes before a down check comes back up
}

// Next applies one result to a state.
//
// An unknown check goes up on its first success, so a fresh install shows a
// status quickly, but it needs the full failure threshold to go down. A
// failure below the threshold leaves the displayed state as it was.
func Next(s State, r Result, th Thresholds) State {
	if s.State == "" {
		s.State = Unknown
	}
	if r.Skip {
		return s
	}
	if r.OK {
		s.ConsecutiveOK++
		s.ConsecutiveFail = 0
		switch s.State {
		case Unknown:
			s.State = Up
		case Down:
			if s.ConsecutiveOK >= th.Recover {
				s.State = Up
			}
		}
		s.Degraded = s.State == Up && r.Slow
		return s
	}
	s.ConsecutiveFail++
	s.ConsecutiveOK = 0
	if s.State != Down && s.ConsecutiveFail >= th.Fail {
		s.State = Down
	}
	if s.State == Down {
		s.Degraded = false
	}
	return s
}

// Component rolls the states of a component's checks into one status.
//
// Checks that are still unknown are ignored as long as at least one check
// has a known state. All known checks down is a major outage, some of them
// down is a partial outage, and a slow but working check is degraded.
func Component(states []State) Status {
	if len(states) == 0 {
		return Operational
	}
	known, down, degraded := 0, 0, 0
	for _, s := range states {
		switch s.State {
		case Up:
			known++
			if s.Degraded {
				degraded++
			}
		case Down:
			known++
			down++
		}
	}
	switch {
	case known == 0:
		return NoData
	case down > 0 && down == known:
		return Major
	case down > 0:
		return Partial
	case degraded > 0:
		return Degraded
	default:
		return Operational
	}
}

// Glyph is the status mark, after the o / triangle / x convention of
// railway service boards.
func (s Status) Glyph() string {
	switch s {
	case Operational:
		return "○"
	case Degraded:
		return "△"
	case Partial:
		return "▲"
	case Major:
		return "×"
	case Maintenance:
		return "◇"
	default:
		return "–"
	}
}

// Worst returns the most severe status. With no input, or only NoData, it
// returns NoData.
func Worst(statuses []Status) Status {
	worst := NoData
	for _, s := range statuses {
		if s.Severity() > worst.Severity() {
			worst = s
		}
	}
	return worst
}
