package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dor1a/noroshi/internal/config"
	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/uptime"
)

// CheckRow is an active check with what the prober needs to run it.
type CheckRow struct {
	ID            int64
	ComponentID   int64
	PageKey       string
	ComponentKey  string
	ComponentName i18n.Text
	Key           string
	Type          string
	Spec          config.Check
}

// ActiveChecks lists checks whose page, component, and check are not archived.
func (s *Store) ActiveChecks(ctx context.Context) ([]CheckRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT k.id, k.component_id, p.key, c.key, c.name, k.key, k.type, k.spec
		FROM checks k
		JOIN component c ON c.id = k.component_id
		JOIN page p ON p.id = c.page_id
		WHERE k.archived_at IS NULL AND c.archived_at IS NULL AND p.archived_at IS NULL
		ORDER BY p.position, c.position, k.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CheckRow
	for rows.Next() {
		var r CheckRow
		var name, spec []byte
		if err := rows.Scan(&r.ID, &r.ComponentID, &r.PageKey, &r.ComponentKey, &name, &r.Key, &r.Type, &spec); err != nil {
			return nil, err
		}
		if err := scanJSON(name, &r.ComponentName); err != nil {
			return nil, fmt.Errorf("check %d component name: %w", r.ID, err)
		}
		if err := scanJSON(spec, &r.Spec); err != nil {
			return nil, fmt.Errorf("check %d spec: %w", r.ID, err)
		}
		if err := r.Spec.Restore(); err != nil {
			return nil, fmt.Errorf("check %d spec: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ComponentRef identifies an active component for the prober.
type ComponentRef struct {
	ID      int64
	PageKey string
	Key     string
}

// ActiveComponents lists components that are not archived.
func (s *Store) ActiveComponents(ctx context.Context) ([]ComponentRef, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, p.key, c.key
		FROM component c JOIN page p ON p.id = c.page_id
		WHERE c.archived_at IS NULL AND p.archived_at IS NULL
		ORDER BY p.position, c.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComponentRef
	for rows.Next() {
		var r ComponentRef
		if err := rows.Scan(&r.ID, &r.PageKey, &r.Key); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StateRow is the stored state of one check.
type StateRow struct {
	judge.State
	Since         time.Time
	LastAt        *time.Time
	LastOK        *bool
	LastLatencyMS *int
	LastDetail    string
	CertExpiry    *time.Time
	DomainExpiry  *time.Time
}

// CheckStates loads every stored check state keyed by check ID.
func (s *Store) CheckStates(ctx context.Context) (map[int64]StateRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT check_id, state, degraded, since, consecutive_fail, consecutive_ok,
		       last_at, last_ok, last_latency_ms, last_detail, cert_expiry, domain_expiry
		FROM check_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]StateRow{}
	for rows.Next() {
		var id int64
		var r StateRow
		var state string
		if err := rows.Scan(&id, &state, &r.Degraded, &r.Since, &r.ConsecutiveFail, &r.ConsecutiveOK,
			&r.LastAt, &r.LastOK, &r.LastLatencyMS, &r.LastDetail, &r.CertExpiry, &r.DomainExpiry); err != nil {
			return nil, err
		}
		r.State.State = judge.CheckState(state)
		out[id] = r
	}
	return out, rows.Err()
}

// LatestStatuses returns the most recent status event per component.
func (s *Store) LatestStatuses(ctx context.Context) (map[int64]judge.Status, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (component_id) component_id, status
		FROM component_status_event
		ORDER BY component_id, at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]judge.Status{}
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		out[id] = judge.Status(st)
	}
	return out, rows.Err()
}

// Result is one probe result to store.
type Result struct {
	CheckID   int64
	At        time.Time
	OK        bool
	Slow      bool
	Skipped   bool
	LatencyMS int
	Detail    string
}

// StatusChange records a component status change.
type StatusChange struct {
	ComponentID int64
	Status      judge.Status
	At          time.Time
	Source      string
}

// Record stores a result, the new check state, and any status change in
// one transaction.
func (s *Store) Record(ctx context.Context, res Result, st StateRow, change *StatusChange) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `
		INSERT INTO check_result (check_id, at, ok, slow, skipped, latency_ms, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		res.CheckID, res.At, res.OK, res.Slow, res.Skipped, res.LatencyMS, res.Detail); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO check_state (check_id, state, degraded, since, consecutive_fail, consecutive_ok,
		                         last_at, last_ok, last_latency_ms, last_detail, cert_expiry, domain_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (check_id) DO UPDATE SET
		    state = EXCLUDED.state, degraded = EXCLUDED.degraded, since = EXCLUDED.since,
		    consecutive_fail = EXCLUDED.consecutive_fail, consecutive_ok = EXCLUDED.consecutive_ok,
		    last_at = EXCLUDED.last_at, last_ok = EXCLUDED.last_ok,
		    last_latency_ms = EXCLUDED.last_latency_ms, last_detail = EXCLUDED.last_detail,
		    cert_expiry = COALESCE(EXCLUDED.cert_expiry, check_state.cert_expiry),
		    domain_expiry = COALESCE(EXCLUDED.domain_expiry, check_state.domain_expiry)`,
		res.CheckID, string(st.State.State), st.Degraded, st.Since, st.ConsecutiveFail, st.ConsecutiveOK,
		st.LastAt, st.LastOK, st.LastLatencyMS, st.LastDetail, st.CertExpiry, st.DomainExpiry); err != nil {
		return err
	}
	if change != nil {
		if err := insertChange(ctx, tx, *change); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AddStatusChange records a status change outside a probe result, for
// example when a component without checks first appears.
func (s *Store) AddStatusChange(ctx context.Context, c StatusChange) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO component_status_event (component_id, status, at, source)
		VALUES ($1, $2, $3, $4)`, c.ComponentID, string(c.Status), c.At, c.Source)
	return err
}

func insertChange(ctx context.Context, tx pgx.Tx, c StatusChange) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO component_status_event (component_id, status, at, source)
		VALUES ($1, $2, $3, $4)`, c.ComponentID, string(c.Status), c.At, c.Source)
	return err
}

// StartSession records that this instance became the prober leader.
func (s *Store) StartSession(ctx context.Context, instance string, now time.Time) (int64, error) {
	var id int64
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO prober_session (instance, started_at, last_seen_at)
		VALUES ($1, $2, $2) RETURNING id`, instance, now).Scan(&id)
	return id, err
}

// TouchSession extends the current leader session.
func (s *Store) TouchSession(ctx context.Context, id int64, now time.Time) error {
	_, err := s.Pool.Exec(ctx, `UPDATE prober_session SET last_seen_at = $2 WHERE id = $1`, id, now)
	return err
}

// PruneResults deletes raw results older than before.
func (s *Store) PruneResults(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM check_result WHERE at < $1`, before)
	return tag.RowsAffected(), err
}

// Session is one prober leadership period.
type Session struct {
	Instance   string
	StartedAt  time.Time
	LastSeenAt time.Time
}

// LastSession returns the most recently active prober session, if any.
func (s *Store) LastSession(ctx context.Context) (*Session, error) {
	var x Session
	err := s.Pool.QueryRow(ctx, `
		SELECT instance, started_at, last_seen_at FROM prober_session
		ORDER BY last_seen_at DESC LIMIT 1`).Scan(&x.Instance, &x.StartedAt, &x.LastSeenAt)
	if isNoRows(err) {
		return nil, nil
	}
	return &x, err
}

// Coverage returns prober sessions overlapping [from, now).
func (s *Store) Coverage(ctx context.Context, from time.Time) ([]uptime.Interval, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT started_at, last_seen_at FROM prober_session WHERE last_seen_at >= $1`, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uptime.Interval
	for rows.Next() {
		var iv uptime.Interval
		if err := rows.Scan(&iv.Start, &iv.End); err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}

// PageRow is an active page.
type PageRow struct {
	ID       int64
	Key      string
	Title    i18n.Text
	Timezone string
}

// Pages lists active pages in config order.
func (s *Store) Pages(ctx context.Context) ([]PageRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, key, title, timezone FROM page
		WHERE archived_at IS NULL ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PageRow
	for rows.Next() {
		var p PageRow
		var title []byte
		if err := rows.Scan(&p.ID, &p.Key, &title, &p.Timezone); err != nil {
			return nil, err
		}
		if err := scanJSON(title, &p.Title); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ComponentRow is an active component with its page and group.
type ComponentRow struct {
	ID          int64
	PageKey     string
	PageTitle   i18n.Text
	GroupKey    string
	GroupName   i18n.Text
	Key         string
	Name        i18n.Text
	Description i18n.Text
}

// PageComponents lists a page's active components in display order.
func (s *Store) PageComponents(ctx context.Context, pageID int64) ([]ComponentRow, error) {
	return s.components(ctx, `c.page_id = $1`, pageID)
}

// AllComponents lists the active components of every active page, page by
// page in display order.
func (s *Store) AllComponents(ctx context.Context) ([]ComponentRow, error) {
	return s.components(ctx, `p.archived_at IS NULL`)
}

func (s *Store) components(ctx context.Context, where string, args ...any) ([]ComponentRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT c.id, p.key, p.title, g.key, g.name, c.key, c.name, c.description
		FROM component c JOIN component_group g ON g.id = c.group_id JOIN page p ON p.id = c.page_id
		WHERE `+where+` AND c.archived_at IS NULL AND g.archived_at IS NULL
		ORDER BY p.position, g.position, c.position`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComponentRow
	for rows.Next() {
		var c ComponentRow
		var ptitle, gname, name, desc []byte
		if err := rows.Scan(&c.ID, &c.PageKey, &ptitle, &c.GroupKey, &gname, &c.Key, &name, &desc); err != nil {
			return nil, err
		}
		if err := scanJSON(ptitle, &c.PageTitle); err != nil {
			return nil, err
		}
		if err := scanJSON(gname, &c.GroupName); err != nil {
			return nil, err
		}
		if err := scanJSON(name, &c.Name); err != nil {
			return nil, err
		}
		if err := scanJSON(desc, &c.Description); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StatusEvents returns, per component, the events since from plus the last
// event before from, sorted by time.
func (s *Store) StatusEvents(ctx context.Context, ids []int64, from time.Time) (map[int64][]uptime.Event, error) {
	rows, err := s.Pool.Query(ctx, `
		(SELECT component_id, status, at, id FROM component_status_event
		 WHERE component_id = ANY($1) AND at >= $2)
		UNION ALL
		(SELECT DISTINCT ON (component_id) component_id, status, at, id FROM component_status_event
		 WHERE component_id = ANY($1) AND at < $2
		 ORDER BY component_id, at DESC, id DESC)`, ids, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		ev uptime.Event
		id int64
	}
	byComp := map[int64][]row{}
	for rows.Next() {
		var cid, id int64
		var st string
		var at time.Time
		if err := rows.Scan(&cid, &st, &at, &id); err != nil {
			return nil, err
		}
		byComp[cid] = append(byComp[cid], row{uptime.Event{At: at, Status: judge.Status(st)}, id})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[int64][]uptime.Event, len(byComp))
	for cid, rs := range byComp {
		sort.Slice(rs, func(i, j int) bool {
			if rs[i].ev.At.Equal(rs[j].ev.At) {
				return rs[i].id < rs[j].id
			}
			return rs[i].ev.At.Before(rs[j].ev.At)
		})
		evs := make([]uptime.Event, len(rs))
		for i, r := range rs {
			evs[i] = r.ev
		}
		out[cid] = evs
	}
	return out, nil
}

// AdminCheck is a check with its location and state for the admin view.
type AdminCheck struct {
	ID            int64
	ComponentID   int64
	PageKey       string
	PageTitle     i18n.Text
	ComponentKey  string
	ComponentName i18n.Text
	Key           string
	Type          string
	Spec          config.Check
	State         *StateRow
}

// AdminChecks lists active checks with their current state.
func (s *Store) AdminChecks(ctx context.Context) ([]AdminCheck, error) {
	return s.adminChecks(ctx, 0)
}

// AdminCheck returns one check by ID, or nil.
func (s *Store) AdminCheck(ctx context.Context, id int64) (*AdminCheck, error) {
	list, err := s.adminChecks(ctx, id)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func (s *Store) adminChecks(ctx context.Context, only int64) ([]AdminCheck, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT k.id, c.id, p.key, p.title, c.key, c.name, k.key, k.type, k.spec,
		       st.state, st.degraded, st.since, st.consecutive_fail, st.consecutive_ok,
		       st.last_at, st.last_ok, st.last_latency_ms, st.last_detail, st.cert_expiry, st.domain_expiry
		FROM checks k
		JOIN component c ON c.id = k.component_id
		JOIN page p ON p.id = c.page_id
		LEFT JOIN check_state st ON st.check_id = k.id
		WHERE k.archived_at IS NULL AND c.archived_at IS NULL AND p.archived_at IS NULL
		  AND ($1::bigint = 0 OR k.id = $1::bigint)
		ORDER BY p.position, c.position, k.position`, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminCheck
	for rows.Next() {
		var a AdminCheck
		var ptitle, cname, spec []byte
		var state *string
		var degraded *bool
		var since *time.Time
		var cf, co *int
		st := StateRow{}
		if err := rows.Scan(&a.ID, &a.ComponentID, &a.PageKey, &ptitle, &a.ComponentKey, &cname, &a.Key, &a.Type, &spec,
			&state, &degraded, &since, &cf, &co,
			&st.LastAt, &st.LastOK, &st.LastLatencyMS, &st.LastDetail, &st.CertExpiry, &st.DomainExpiry); err != nil {
			return nil, err
		}
		if err := scanJSON(ptitle, &a.PageTitle); err != nil {
			return nil, err
		}
		if err := scanJSON(cname, &a.ComponentName); err != nil {
			return nil, err
		}
		if err := scanJSON(spec, &a.Spec); err != nil {
			return nil, err
		}
		if state != nil {
			st.State.State = judge.CheckState(*state)
			st.Degraded = deref(degraded)
			st.Since = derefTime(since)
			st.ConsecutiveFail = deref(cf)
			st.ConsecutiveOK = deref(co)
			a.State = &st
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FailureRun is a stretch of consecutive failed results of one check.
type FailureRun struct {
	Start  time.Time  // first failed result
	Last   time.Time  // last failed result
	End    *time.Time // first passing result after the run, nil while failing
	Count  int
	Detail string // reason of the first failure
}

// FailureRuns returns the newest runs of consecutive failures that overlap
// [from, to). Runs are found among the results since `since`, so one that
// began before from is returned whole. Skipped results neither break nor
// extend a run, and slow results pass.
func (s *Store) FailureRuns(ctx context.Context, checkID int64, since, from, to time.Time, limit int) ([]FailureRun, error) {
	rows, err := s.Pool.Query(ctx, `
		WITH r AS (
			SELECT at, ok, detail,
			       row_number() OVER (ORDER BY at) - row_number() OVER (PARTITION BY ok ORDER BY at) AS grp
			FROM check_result
			WHERE check_id = $1 AND at >= $2 AND NOT skipped
		), runs AS (
			SELECT min(at) AS start_at, max(at) AS last_at, count(*) AS n,
			       (array_agg(detail ORDER BY at))[1] AS detail
			FROM r WHERE NOT ok GROUP BY grp
		)
		SELECT runs.start_at, runs.last_at, nxt.at, runs.n, runs.detail
		FROM runs
		LEFT JOIN LATERAL (
			SELECT at FROM check_result
			WHERE check_id = $1 AND at > runs.last_at AND ok AND NOT skipped
			ORDER BY at LIMIT 1
		) nxt ON true
		WHERE runs.start_at < $4 AND (nxt.at IS NULL OR nxt.at > $3)
		ORDER BY runs.start_at DESC
		LIMIT $5`, checkID, since, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FailureRun
	for rows.Next() {
		var f FailureRun
		if err := rows.Scan(&f.Start, &f.Last, &f.End, &f.Count, &f.Detail); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// LatencyBucket sums up a check's results in one slice of a period.
type LatencyBucket struct {
	Index    int // slice number from the start of the period
	AvgMS    *float64
	MaxMS    *int
	Failures int
	Count    int
}

// LatencyBuckets cuts [from, to) into n equal slices and returns, for each
// slice that has results, the average and highest latency of passing
// results and the number of failures.
func (s *Store) LatencyBuckets(ctx context.Context, checkID int64, from, to time.Time, n int) ([]LatencyBucket, error) {
	stride := to.Sub(from).Seconds() / float64(n)
	if stride <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT floor(extract(epoch FROM at - $2) / $4)::int AS b,
		       (avg(latency_ms) FILTER (WHERE ok))::float8, max(latency_ms) FILTER (WHERE ok),
		       count(*) FILTER (WHERE NOT ok AND NOT skipped), count(*)
		FROM check_result
		WHERE check_id = $1 AND at >= $2 AND at < $3
		GROUP BY b ORDER BY b`, checkID, from, to, stride)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LatencyBucket
	for rows.Next() {
		var b LatencyBucket
		if err := rows.Scan(&b.Index, &b.AvgMS, &b.MaxMS, &b.Failures, &b.Count); err != nil {
			return nil, err
		}
		if b.Index >= 0 && b.Index < n {
			out = append(out, b)
		}
	}
	return out, rows.Err()
}

// Failure is one failed result and the check it came from.
type Failure struct {
	CheckID int64
	Key     string
	At      time.Time
	Detail  string
}

// FailureBefore returns the newest failed result among a component's checks
// at or before at, which is what turned the component down. ok is false when
// there is none, as after the results were pruned.
func (s *Store) FailureBefore(ctx context.Context, componentID int64, at time.Time) (f Failure, ok bool, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT k.id, k.key, cr.at, cr.detail
		FROM check_result cr JOIN checks k ON k.id = cr.check_id
		WHERE k.component_id = $1 AND k.archived_at IS NULL AND NOT cr.ok AND NOT cr.skipped AND cr.at <= $2
		ORDER BY cr.at DESC LIMIT 1`, componentID, at).Scan(&f.CheckID, &f.Key, &f.At, &f.Detail)
	if isNoRows(err) {
		return f, false, nil
	}
	return f, err == nil, err
}

// Results returns the newest results for a check in [from, to), at most
// limit of them, and how many there are in all. failedOnly keeps failed and
// skipped results.
func (s *Store) Results(ctx context.Context, checkID int64, from, to time.Time, failedOnly bool, limit int) ([]Result, int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT check_id, at, ok, slow, skipped, latency_ms, detail, count(*) OVER ()
		FROM check_result
		WHERE check_id = $1 AND at >= $2 AND at < $3 AND (NOT $4 OR NOT ok OR skipped)
		ORDER BY at DESC LIMIT $5`, checkID, from, to, failedOnly, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Result
	total := 0
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.CheckID, &r.At, &r.OK, &r.Slow, &r.Skipped, &r.LatencyMS, &r.Detail, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func derefTime(p *time.Time) time.Time { return deref(p) }
