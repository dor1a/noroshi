// Package prober runs the checks on the one instance that holds the leader
// lock, debounces their results, and records component status changes.
package prober

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/metrics"
	"github.com/dor1a/noroshi/internal/probe"
	"github.com/dor1a/noroshi/internal/store"
)

// Settings tune the leader loop. Zero values get sensible defaults.
type Settings struct {
	LockRetry     time.Duration // how often a follower tries to take the lead
	Heartbeat     time.Duration // how often the leader checks its lock connection and touches its session
	Retention     time.Duration // how long raw results are kept
	PruneInterval time.Duration
	Locale        string // language of the component names in metrics
}

// Prober is safe to start once.
type Prober struct {
	store    *store.Store
	metrics  *metrics.Metrics
	runner   *probe.Runner
	log      *slog.Logger
	settings Settings
	instance string

	leader atomic.Bool
}

// New creates a prober.
func New(st *store.Store, m *metrics.Metrics, log *slog.Logger, s Settings) *Prober {
	if s.LockRetry == 0 {
		s.LockRetry = 10 * time.Second
	}
	if s.Heartbeat == 0 {
		s.Heartbeat = 15 * time.Second
	}
	if s.Retention == 0 {
		s.Retention = config.DefaultResultRetention
	}
	if s.PruneInterval == 0 {
		s.PruneInterval = time.Hour
	}
	host, _ := os.Hostname()
	return &Prober{
		store:    st,
		metrics:  m,
		runner:   probe.NewRunner(),
		log:      log,
		settings: s,
		instance: fmt.Sprintf("%s/%d", host, os.Getpid()),
	}
}

// IsLeader reports whether this instance currently runs the checks.
func (p *Prober) IsLeader() bool { return p.leader.Load() }

// Run blocks until ctx is done, taking the lead whenever it is free.
func (p *Prober) Run(ctx context.Context) {
	p.metrics.SetLeader(false)
	for {
		if err := p.tryLead(ctx); err != nil && ctx.Err() == nil {
			p.log.Warn("prober leadership attempt failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.settings.LockRetry):
		}
	}
}

// tryLead takes the advisory lock on a dedicated connection and runs the
// checks for as long as that connection stays healthy.
func (p *Prober) tryLead(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, p.store.DSN)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", store.LockProber).Scan(&got); err != nil {
		return err
	}
	if !got {
		return nil
	}

	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.leader.Store(true)
	p.metrics.SetLeader(true)
	p.log.Info("prober is leader", "instance", p.instance)
	defer func() {
		p.leader.Store(false)
		p.metrics.SetLeader(false)
		p.log.Info("prober released leadership", "instance", p.instance)
	}()

	// Watch the lock connection; if it dies the lock is gone too.
	go func() {
		t := time.NewTicker(p.settings.Heartbeat)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(lctx, 5*time.Second)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil && lctx.Err() == nil {
					p.log.Warn("lost lock connection", "err", err)
					cancel()
					return
				}
			}
		}
	}()

	return p.lead(lctx)
}

type checkRun struct {
	row   store.CheckRow
	state store.StateRow
}

type leaderState struct {
	mu        sync.Mutex
	checks    map[int64]*checkRun
	byComp    map[int64][]int64
	compRefs  map[int64]store.ComponentRef
	compState map[int64]judge.Status
}

func (p *Prober) lead(ctx context.Context) error {
	now := time.Now()
	sessionID, err := p.store.StartSession(ctx, p.instance, now)
	if err != nil {
		return err
	}
	checks, err := p.store.ActiveChecks(ctx)
	if err != nil {
		return err
	}
	states, err := p.store.CheckStates(ctx)
	if err != nil {
		return err
	}
	comps, err := p.store.ActiveComponents(ctx)
	if err != nil {
		return err
	}
	latest, err := p.store.LatestStatuses(ctx)
	if err != nil {
		return err
	}

	ls := &leaderState{
		checks:    map[int64]*checkRun{},
		byComp:    map[int64][]int64{},
		compRefs:  map[int64]store.ComponentRef{},
		compState: map[int64]judge.Status{},
	}
	for _, c := range comps {
		ls.compRefs[c.ID] = c
		if st, ok := latest[c.ID]; ok {
			ls.compState[c.ID] = st
		}
	}
	for _, c := range checks {
		st, ok := states[c.ID]
		if !ok {
			st = store.StateRow{State: judge.State{State: judge.Unknown}, Since: now}
		}
		ls.checks[c.ID] = &checkRun{row: c, state: st}
		ls.byComp[c.ComponentID] = append(ls.byComp[c.ComponentID], c.ID)
	}

	// Components may need a first status (no checks) or a corrected one
	// (checks were added or removed while no prober was running).
	for id, ref := range ls.compRefs {
		want := p.componentStatus(ls, id)
		p.metrics.ComponentStatus(ref.PageKey, ref.Key, want)
		if cur, ok := ls.compState[id]; !ok || cur != want {
			if err := p.store.AddStatusChange(ctx, store.StatusChange{ComponentID: id, Status: want, At: now, Source: "check"}); err != nil {
				return err
			}
			ls.compState[id] = want
		}
	}

	p.log.Info("starting checks", "checks", len(checks), "components", len(comps))
	var wg sync.WaitGroup
	for _, cr := range ls.checks {
		wg.Add(1)
		go func(cr *checkRun) {
			defer wg.Done()
			p.loop(ctx, ls, cr)
		}(cr)
	}

	hb := time.NewTicker(p.settings.Heartbeat)
	defer hb.Stop()
	prune := time.NewTicker(p.settings.PruneInterval)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			// Record the end of this session as precisely as possible.
			tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.store.TouchSession(tctx, sessionID, time.Now()) //nolint:errcheck
			cancel()
			return nil
		case t := <-hb.C:
			if err := p.store.TouchSession(ctx, sessionID, t); err != nil && ctx.Err() == nil {
				p.log.Warn("touch session failed", "err", err)
			}
		case t := <-prune.C:
			n, err := p.store.PruneResults(ctx, t.Add(-p.settings.Retention))
			if err != nil && ctx.Err() == nil {
				p.log.Warn("prune results failed", "err", err)
			} else if n > 0 {
				p.log.Info("pruned old results", "rows", n)
			}
		}
	}
}

// loop runs one check on its interval, starting after a random delay so
// checks spread out instead of firing together.
func (p *Prober) loop(ctx context.Context, ls *leaderState, cr *checkRun) {
	interval := cr.row.Spec.Interval.Duration
	first := time.Duration(rand.Int64N(int64(min(interval, 10*time.Second))))
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		p.runOnce(ctx, ls, cr)
		timer.Reset(interval)
	}
}

func (p *Prober) runOnce(ctx context.Context, ls *leaderState, cr *checkRun) {
	spec := cr.row.Spec
	out := p.runner.Run(ctx, spec)
	if ctx.Err() != nil {
		return // shutting down; the outcome is probably a cancellation
	}
	at := time.Now()

	ls.mu.Lock()
	defer ls.mu.Unlock()

	prev := cr.state
	next := prev
	next.State = judge.Next(prev.State, judge.Result{OK: out.OK, Slow: out.Slow, Skip: out.Skip},
		judge.Thresholds{Fail: spec.FailThreshold, Recover: spec.RecoverThreshold})
	if next.State.State != prev.State.State {
		next.Since = at
	}
	latency := int(out.Latency.Milliseconds())
	ok := out.OK
	next.LastAt = &at
	next.LastOK = &ok
	next.LastLatencyMS = &latency
	next.LastDetail = out.Detail
	if out.CertExpiry != nil {
		next.CertExpiry = out.CertExpiry
	}
	if out.DomainExpiry != nil {
		next.DomainExpiry = out.DomainExpiry
	}
	cr.state = next

	compID := cr.row.ComponentID
	ref := ls.compRefs[compID]
	status := p.componentStatus(ls, compID)
	var change *store.StatusChange
	if cur, ok := ls.compState[compID]; !ok || cur != status {
		change = &store.StatusChange{ComponentID: compID, Status: status, At: at, Source: "check"}
	}

	res := store.Result{
		CheckID: cr.row.ID, At: at, OK: out.OK, Slow: out.Slow, Skipped: out.Skip,
		LatencyMS: latency, Detail: out.Detail,
	}
	if err := p.store.Record(ctx, res, next, change); err != nil {
		if ctx.Err() == nil {
			p.log.Error("record result failed", "check", cr.row.Key, "component", cr.row.ComponentKey, "err", err)
		}
		cr.state = prev // try again on the next run with the stored state
		return
	}
	if change != nil {
		ls.compState[compID] = status
		p.log.Info("component status changed", "page", ref.PageKey, "component", ref.Key, "status", status)
	}

	name := cr.row.ComponentName.Get(p.settings.Locale)
	if name == "" {
		name = cr.row.ComponentKey
	}
	labels := metrics.CheckLabels{
		Page: cr.row.PageKey, Component: cr.row.ComponentKey, Check: cr.row.Key, Type: cr.row.Type,
		Alert: spec.AlertEnabled(), Name: name, Target: spec.Target(),
	}
	p.metrics.CheckRun(labels, next.State.State, at, out.Latency, spec.Interval.Duration, next.CertExpiry, next.DomainExpiry)
	// Keep the latest failure until the check is up again, so an alert that
	// is still firing during recovery keeps its reason.
	switch {
	case !out.OK && !out.Skip:
		p.metrics.CheckFailure(labels, or(out.Detail, "failed"))
	case out.OK && next.State.State == judge.Up:
		p.metrics.CheckFailure(labels, "")
	}
	p.metrics.ComponentStatus(ref.PageKey, ref.Key, status)

	if next.State.State != prev.State.State {
		p.log.Info("check state changed", "page", cr.row.PageKey, "component", cr.row.ComponentKey,
			"check", cr.row.Key, "from", prev.State.State, "to", next.State.State, "detail", out.Detail)
	}
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// componentStatus must be called with ls.mu held or before checks start.
func (p *Prober) componentStatus(ls *leaderState, compID int64) judge.Status {
	ids := ls.byComp[compID]
	states := make([]judge.State, 0, len(ids))
	for _, id := range ids {
		states = append(states, ls.checks[id].state.State)
	}
	return judge.Component(states)
}
