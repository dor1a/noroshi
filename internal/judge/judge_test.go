package judge

import "testing"

var th = Thresholds{Fail: 3, Recover: 2}

var (
	ok   = Result{OK: true}
	slow = Result{OK: true, Slow: true}
	fail = Result{OK: false}
	skip = Result{Skip: true}
)

func run(start State, results ...Result) State {
	s := start
	for _, r := range results {
		s = Next(s, r, th)
	}
	return s
}

func TestNext(t *testing.T) {
	up := State{State: Up}
	down := State{State: Down}
	cases := []struct {
		name    string
		start   State
		results []Result
		want    CheckState
		degr    bool
	}{
		{"new check goes up on first success", State{}, []Result{ok}, Up, false},
		{"new check stays unknown below the failure threshold", State{}, []Result{fail, fail}, Unknown, false},
		{"new check goes down after three failures", State{}, []Result{fail, fail, fail}, Down, false},
		{"one failure does not take an up check down", up, []Result{fail}, Up, false},
		{"fail fail ok fail is not three in a row", up, []Result{fail, fail, ok, fail}, Up, false},
		{"three failures in a row take it down", up, []Result{fail, fail, fail}, Down, false},
		{"one success does not bring a down check back", down, []Result{ok}, Down, false},
		{"two successes bring it back", down, []Result{ok, ok}, Up, false},
		{"ok fail ok does not recover", down, []Result{ok, fail, ok}, Down, false},
		{"slow success marks it degraded", up, []Result{slow}, Up, true},
		{"fast success clears degraded", up, []Result{slow, ok}, Up, false},
		{"slow successes while down do not count as degraded", down, []Result{slow}, Down, false},
		{"recovering with a slow response is degraded", down, []Result{ok, slow}, Up, true},
		{"failure below threshold keeps degraded", up, []Result{slow, fail}, Up, true},
		{"going down clears degraded", up, []Result{slow, fail, fail, fail}, Down, false},
		{"skip changes nothing", up, []Result{fail, fail, skip}, Up, false},
		{"skip does not reset the failure streak", up, []Result{fail, fail, skip, fail}, Down, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := run(c.start, c.results...)
			if got.State != c.want || got.Degraded != c.degr {
				t.Fatalf("got %s degraded=%v, want %s degraded=%v", got.State, got.Degraded, c.want, c.degr)
			}
		})
	}
}

func TestComponent(t *testing.T) {
	u := State{State: Up}
	d := State{State: Down}
	g := State{State: Up, Degraded: true}
	n := State{State: Unknown}
	cases := []struct {
		name   string
		states []State
		want   Status
	}{
		{"no checks is operational", nil, Operational},
		{"all unknown is no data", []State{n, n}, NoData},
		{"all up", []State{u, u}, Operational},
		{"one degraded", []State{u, g}, Degraded},
		{"one of two down", []State{u, d}, Partial},
		{"all down", []State{d, d}, Major},
		{"single check down", []State{d}, Major},
		{"down plus unknown counts only known checks", []State{d, n}, Major},
		{"down beats degraded", []State{g, d}, Partial},
		{"unknown ignored when others are up", []State{u, n}, Operational},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Component(c.states); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestWorst(t *testing.T) {
	cases := []struct {
		in   []Status
		want Status
	}{
		{nil, NoData},
		{[]Status{NoData, NoData}, NoData},
		{[]Status{Operational, NoData}, Operational},
		{[]Status{Operational, Degraded}, Degraded},
		{[]Status{Partial, Major, Operational}, Major},
		{[]Status{Maintenance, Operational}, Maintenance},
	}
	for _, c := range cases {
		if got := Worst(c.in); got != c.want {
			t.Fatalf("Worst(%v) = %s, want %s", c.in, got, c.want)
		}
	}
}
