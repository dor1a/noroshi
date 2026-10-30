package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/uptime"
)

// Incident stages, in the order they usually follow.
const (
	StageInvestigating = "investigating"
	StageIdentified    = "identified"
	StageMonitoring    = "monitoring"
	StageResolved      = "resolved"
	// StageScheduled is the stage a maintenance is stored with. What it
	// shows, upcoming, in progress, or done, follows from its window.
	StageScheduled = "scheduled"
)

// Stages lists the incident stages in order.
var Stages = []string{StageInvestigating, StageIdentified, StageMonitoring, StageResolved}

// ErrNotFound is returned when an incident does not exist.
var ErrNotFound = errors.New("not found")

// ErrOpenIncident is returned when an edit needs a resolved incident.
var ErrOpenIncident = errors.New("incident is still open")

// ErrLatestUpdate is returned when deleting an incident's newest update,
// which carries its current stage.
var ErrLatestUpdate = errors.New("latest update")

// Incident is an incident with its timeline and affected components.
// Kinds of incident.
const (
	KindIncident    = "incident"
	KindMaintenance = "maintenance"
)

type Incident struct {
	ID         int64
	Kind       string
	PageID     int64
	PageKey    string
	PageTitle  i18n.Text
	Title      string
	Stage      string
	StartedAt  time.Time
	ResolvedAt *time.Time
	CreatedBy  string
	Updates    []IncidentUpdate    // newest first
	Components []IncidentComponent // in page order
}

// Open reports whether the incident is not resolved yet.
func (i Incident) Open() bool { return i.ResolvedAt == nil }

// Maintenance reports whether the incident is a planned maintenance.
func (i Incident) Maintenance() bool { return i.Kind == KindMaintenance }

// Maintenance phases.
const (
	PhaseUpcoming = "upcoming"
	PhaseActive   = "active"
	PhaseDone     = "done"
)

// Phase is where a maintenance stands at now: upcoming before its window,
// active inside it, done after it.
func (i Incident) Phase(now time.Time) string {
	switch {
	case now.Before(i.StartedAt):
		return PhaseUpcoming
	case i.ResolvedAt == nil || now.Before(*i.ResolvedAt):
		return PhaseActive
	}
	return PhaseDone
}

// Active reports whether the incident is going on at now: an open incident,
// or a maintenance inside its window.
func (i Incident) Active(now time.Time) bool {
	if i.Maintenance() {
		return i.Phase(now) == PhaseActive
	}
	return i.Open()
}

// Worst is the most severe status among the affected components, or
// NoData when the incident names none.
func (i Incident) Worst() judge.Status {
	var st []judge.Status
	for _, c := range i.Components {
		st = append(st, c.Status)
	}
	return judge.Worst(st)
}

// IncidentUpdate is one entry in an incident's timeline.
type IncidentUpdate struct {
	ID            int64
	Stage         string
	Body          string
	At            time.Time
	Author        string
	EditedAt      *time.Time
	EditedBy      string
	Notifications []Notification
}

// IncidentComponent is a component named by an incident. For an open
// incident Status is the declared status now; for a resolved one it is the
// worst status declared while it was open.
type IncidentComponent struct {
	ComponentID int64
	Key         string
	Name        i18n.Text
	Status      judge.Status
}

// Notification is the delivery result of one announcement.
type Notification struct {
	UpdateID int64
	Notifier string
	OK       bool
	Attempts int
	Detail   string
	At       time.Time
}

// IncidentInput is what an admin submits when creating or updating an
// incident. Components maps component IDs to declared statuses; components
// left out are no longer affected.
type IncidentInput struct {
	Stage      string
	Body       string
	Author     string
	At         time.Time
	Components map[int64]judge.Status
	// Since is when a new incident began, for one recorded after the fact;
	// zero means At. Declared statuses start here.
	Since time.Time
	// Kind and Until are for a new maintenance: Until is the end of its
	// window, where its resolve time and declared statuses end.
	Kind  string
	Until *time.Time
}

func (in IncidentInput) since() time.Time {
	if in.Since.IsZero() {
		return in.At
	}
	return in.Since
}

// CreateIncident stores a new incident with its first update.
//
// An incident created resolved keeps its declared statuses for the period
// it covers, Since to At: the uptime of that period counts them and the
// days show the incident. Without Since the period is empty, which links
// the components without touching uptime.
func (s *Store) CreateIncident(ctx context.Context, pageID int64, title string, in IncidentInput) (incidentID, updateID int64, err error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	kind := in.Kind
	if kind == "" {
		kind = KindIncident
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO incident (page_id, title, stage, started_at, created_by, kind)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		pageID, title, in.Stage, in.since(), in.Author, kind).Scan(&incidentID); err != nil {
		return 0, 0, err
	}
	if updateID, err = applyUpdate(ctx, tx, incidentID, in); err != nil {
		return 0, 0, err
	}
	if in.Until != nil {
		if _, err := tx.Exec(ctx, `UPDATE incident SET resolved_at = $2 WHERE id = $1`, incidentID, *in.Until); err != nil {
			return 0, 0, err
		}
		if _, err := tx.Exec(ctx, `UPDATE incident_component SET until = $2 WHERE incident_id = $1 AND until IS NULL`, incidentID, *in.Until); err != nil {
			return 0, 0, err
		}
	}
	if in.Stage == StageResolved {
		for cid, st := range in.Components {
			if _, err := tx.Exec(ctx, `
				INSERT INTO incident_component (incident_id, component_id, status, since, until)
				VALUES ($1, $2, $3, $4, $5)`, incidentID, cid, string(st), in.since(), in.At); err != nil {
				return 0, 0, err
			}
		}
	}
	return incidentID, updateID, tx.Commit(ctx)
}

// UpdateIncident appends an update. Updates to a resolved incident are kept
// as notes: the stage stays resolved and component statuses are not touched.
func (s *Store) UpdateIncident(ctx context.Context, id int64, in IncidentInput) (int64, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var resolvedAt *time.Time
	var stage string
	if err := tx.QueryRow(ctx, `SELECT resolved_at, stage FROM incident WHERE id = $1 FOR UPDATE`, id).Scan(&resolvedAt, &stage); err != nil {
		if isNoRows(err) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if resolvedAt != nil {
		var updateID int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO incident_update (incident_id, stage, body, at, author)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`,
			id, stage, in.Body, in.At, in.Author).Scan(&updateID); err != nil {
			return 0, err
		}
		return updateID, tx.Commit(ctx)
	}
	updateID, err := applyUpdate(ctx, tx, id, in)
	if err != nil {
		return 0, err
	}
	return updateID, tx.Commit(ctx)
}

// applyUpdate records an update on an open incident and moves the declared
// component statuses to match it.
func applyUpdate(ctx context.Context, tx pgx.Tx, id int64, in IncidentInput) (int64, error) {
	var updateID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO incident_update (incident_id, stage, body, at, author)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		id, in.Stage, in.Body, in.At, in.Author).Scan(&updateID); err != nil {
		return 0, err
	}
	resolved := in.Stage == StageResolved
	if resolved {
		if _, err := tx.Exec(ctx, `UPDATE incident SET stage = $2, resolved_at = $3 WHERE id = $1`, id, in.Stage, in.At); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE incident SET stage = $2 WHERE id = $1`, id, in.Stage); err != nil {
		return 0, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, component_id, status FROM incident_component
		WHERE incident_id = $1 AND until IS NULL`, id)
	if err != nil {
		return 0, err
	}
	type open struct {
		rowID  int64
		status judge.Status
	}
	current := map[int64]open{}
	for rows.Next() {
		var o open
		var cid int64
		var st string
		if err := rows.Scan(&o.rowID, &cid, &st); err != nil {
			rows.Close()
			return 0, err
		}
		o.status = judge.Status(st)
		current[cid] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for cid, o := range current {
		want, keep := in.Components[cid]
		if resolved || !keep || want != o.status {
			if _, err := tx.Exec(ctx, `UPDATE incident_component SET until = $2 WHERE id = $1`, o.rowID, in.At); err != nil {
				return 0, err
			}
		}
	}
	if !resolved {
		for cid, want := range in.Components {
			if o, ok := current[cid]; ok && o.status == want {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO incident_component (incident_id, component_id, status, since)
				VALUES ($1, $2, $3, $4)`, id, cid, string(want), in.since()); err != nil {
				return 0, err
			}
		}
	}
	return updateID, nil
}

// IncidentEdit corrects an incident after the fact. Nil fields stay as
// they are. ResolvedAt and Components apply to resolved incidents only.
type IncidentEdit struct {
	Title      *string
	StartedAt  *time.Time
	ResolvedAt *time.Time
	Components map[int64]judge.Status
}

// EditIncident corrects an incident's title and times, and for a resolved
// incident its components. Declared statuses that began with the incident
// move with its start. When a resolved incident's period or components
// change, its statuses are stored again for the whole period, each at the
// worst status it had unless new ones are given, and the update that
// resolved it moves with the resolve time.
func (s *Store) EditIncident(ctx context.Context, id int64, e IncidentEdit) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var started time.Time
	var resolved *time.Time
	if err := tx.QueryRow(ctx, `SELECT started_at, resolved_at FROM incident WHERE id = $1 FOR UPDATE`, id).Scan(&started, &resolved); err != nil {
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	if e.Title != nil {
		if _, err := tx.Exec(ctx, `UPDATE incident SET title = $2 WHERE id = $1`, id, *e.Title); err != nil {
			return err
		}
	}
	start := started
	if e.StartedAt != nil {
		start = *e.StartedAt
	}
	if resolved == nil {
		if e.ResolvedAt != nil || e.Components != nil {
			return ErrOpenIncident
		}
		if !start.Equal(started) {
			if _, err := tx.Exec(ctx, `UPDATE incident_component SET since = $2 WHERE incident_id = $1 AND since = $3`, id, start, started); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE incident SET started_at = $2 WHERE id = $1`, id, start); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	}

	end := *resolved
	if e.ResolvedAt != nil {
		end = *e.ResolvedAt
	}
	moved := !start.Equal(started) || !end.Equal(*resolved)
	comps := e.Components
	if comps == nil && moved {
		comps = map[int64]judge.Status{}
		rows, err := tx.Query(ctx, `SELECT component_id, status FROM incident_component WHERE incident_id = $1`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var cid int64
			var st string
			if err := rows.Scan(&cid, &st); err != nil {
				rows.Close()
				return err
			}
			if cur, ok := comps[cid]; !ok || judge.Status(st).Severity() > cur.Severity() {
				comps[cid] = judge.Status(st)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if comps != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM incident_component WHERE incident_id = $1`, id); err != nil {
			return err
		}
		for cid, st := range comps {
			if _, err := tx.Exec(ctx, `
				INSERT INTO incident_component (incident_id, component_id, status, since, until)
				VALUES ($1, $2, $3, $4, $5)`, id, cid, string(st), start, end); err != nil {
				return err
			}
		}
	}
	if moved {
		if _, err := tx.Exec(ctx, `UPDATE incident SET started_at = $2, resolved_at = $3 WHERE id = $1`, id, start, end); err != nil {
			return err
		}
		if !end.Equal(*resolved) {
			if _, err := tx.Exec(ctx, `
				UPDATE incident_update SET at = $2
				WHERE incident_id = $1 AND stage = $3 AND at = $4`, id, end, StageResolved, *resolved); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// DeleteIncident removes an incident, its timeline, and its declared
// statuses, which also takes it out of uptime.
func (s *Store) DeleteIncident(ctx context.Context, id int64) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM incident WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// EditUpdate replaces an update's text. Its stage, time, and the component
// statuses it set stay as posted.
func (s *Store) EditUpdate(ctx context.Context, incidentID, updateID int64, body, by string, at time.Time) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE incident_update SET body = $3, edited_at = $4, edited_by = $5
		WHERE id = $2 AND incident_id = $1`, incidentID, updateID, body, at, by)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteUpdate removes one update from a timeline. The newest update carries
// the incident's stage and stays; component statuses are not touched.
func (s *Store) DeleteUpdate(ctx context.Context, incidentID, updateID int64) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT 1 FROM incident WHERE id = $1 FOR UPDATE`, incidentID); err != nil {
		return err
	}
	var latest int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM incident_update WHERE incident_id = $1
		ORDER BY at DESC, id DESC LIMIT 1`, incidentID).Scan(&latest); err != nil {
		if isNoRows(err) {
			return ErrNotFound
		}
		return err
	}
	if latest == updateID {
		return ErrLatestUpdate
	}
	tag, err := tx.Exec(ctx, `DELETE FROM incident_update WHERE id = $2 AND incident_id = $1`, incidentID, updateID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// Incident loads one incident.
func (s *Store) Incident(ctx context.Context, id int64) (*Incident, error) {
	list, err := s.loadIncidents(ctx, `i.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// PageIncidents lists a page's open incidents and those resolved since the
// given time, newest first.
func (s *Store) PageIncidents(ctx context.Context, pageID int64, since time.Time) ([]Incident, error) {
	return s.loadIncidents(ctx, `i.page_id = $1 AND (i.resolved_at IS NULL OR i.resolved_at >= $2)`, pageID, since)
}

// AllIncidents lists open incidents on every page and those resolved since
// the given time, newest first.
func (s *Store) AllIncidents(ctx context.Context, since time.Time) ([]Incident, error) {
	return s.loadIncidents(ctx, `(i.resolved_at IS NULL OR i.resolved_at >= $1)`, since)
}

func (s *Store) loadIncidents(ctx context.Context, where string, args ...any) ([]Incident, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.kind, i.page_id, p.key, p.title, i.title, i.stage, i.started_at, i.resolved_at, i.created_by
		FROM incident i JOIN page p ON p.id = i.page_id
		WHERE `+where+`
		ORDER BY i.started_at DESC, i.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	var out []Incident
	index := map[int64]int{}
	for rows.Next() {
		var in Incident
		var ptitle []byte
		if err := rows.Scan(&in.ID, &in.Kind, &in.PageID, &in.PageKey, &ptitle, &in.Title, &in.Stage,
			&in.StartedAt, &in.ResolvedAt, &in.CreatedBy); err != nil {
			rows.Close()
			return nil, err
		}
		if err := scanJSON(ptitle, &in.PageTitle); err != nil {
			rows.Close()
			return nil, err
		}
		index[in.ID] = len(out)
		out = append(out, in)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]int64, len(out))
	for i, in := range out {
		ids[i] = in.ID
	}

	byUpdate := map[int64][]Notification{}
	nrows, err := s.Pool.Query(ctx, `
		SELECT n.update_id, n.notifier, n.ok, n.attempts, n.detail, n.at
		FROM notification n JOIN incident_update u ON u.id = n.update_id
		WHERE u.incident_id = ANY($1) ORDER BY n.id`, ids)
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var n Notification
		if err := nrows.Scan(&n.UpdateID, &n.Notifier, &n.OK, &n.Attempts, &n.Detail, &n.At); err != nil {
			nrows.Close()
			return nil, err
		}
		byUpdate[n.UpdateID] = append(byUpdate[n.UpdateID], n)
	}
	nrows.Close()
	if err := nrows.Err(); err != nil {
		return nil, err
	}

	urows, err := s.Pool.Query(ctx, `
		SELECT id, incident_id, stage, body, at, author, edited_at, coalesce(edited_by, '') FROM incident_update
		WHERE incident_id = ANY($1) ORDER BY at DESC, id DESC`, ids)
	if err != nil {
		return nil, err
	}
	for urows.Next() {
		var u IncidentUpdate
		var iid int64
		if err := urows.Scan(&u.ID, &iid, &u.Stage, &u.Body, &u.At, &u.Author, &u.EditedAt, &u.EditedBy); err != nil {
			urows.Close()
			return nil, err
		}
		u.Notifications = byUpdate[u.ID]
		out[index[iid]].Updates = append(out[index[iid]].Updates, u)
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return nil, err
	}

	// Current status for open rows; for closed ones the worst seen, which is
	// what a resolved incident shows.
	crows, err := s.Pool.Query(ctx, `
		SELECT ic.incident_id, c.id, c.key, c.name, ic.status, ic.until IS NULL
		FROM incident_component ic JOIN component c ON c.id = ic.component_id
		WHERE ic.incident_id = ANY($1)
		ORDER BY c.position, ic.since`, ids)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	type key struct{ incident, component int64 }
	seen := map[key]int{}
	for crows.Next() {
		var iid int64
		var c IncidentComponent
		var name []byte
		var st string
		var open bool
		if err := crows.Scan(&iid, &c.ComponentID, &c.Key, &name, &st, &open); err != nil {
			return nil, err
		}
		if err := scanJSON(name, &c.Name); err != nil {
			return nil, err
		}
		c.Status = judge.Status(st)
		in := &out[index[iid]]
		if in.Open() && !open {
			continue // an earlier declaration, already replaced or dropped
		}
		k := key{iid, c.ComponentID}
		if i, ok := seen[k]; ok {
			if c.Status.Severity() > in.Components[i].Status.Severity() {
				in.Components[i].Status = c.Status
			}
			continue
		}
		seen[k] = len(in.Components)
		in.Components = append(in.Components, c)
	}
	return out, crows.Err()
}

// Overrides returns, per component, the incident-declared statuses that
// overlap [from, now).
func (s *Store) Overrides(ctx context.Context, ids []int64, from time.Time) (map[int64][]uptime.Override, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT component_id, status, since, until FROM incident_component
		WHERE component_id = ANY($1) AND (until IS NULL OR until > $2)
		ORDER BY since`, ids, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]uptime.Override{}
	for rows.Next() {
		var cid int64
		var st string
		var o uptime.Override
		var until *time.Time
		if err := rows.Scan(&cid, &st, &o.Start, &until); err != nil {
			return nil, err
		}
		o.Status = judge.Status(st)
		if until != nil {
			o.End = *until
		}
		out[cid] = append(out[cid], o)
	}
	return out, rows.Err()
}

// IncidentLink ties an incident to a component for the time the incident
// declared a status on it.
type IncidentLink struct {
	IncidentID int64
	Title      string
	Start      time.Time
	End        *time.Time // nil while the status is still declared
}

// IncidentLinks returns, per component, the incidents whose declared
// statuses overlap [from, now), oldest first.
func (s *Store) IncidentLinks(ctx context.Context, ids []int64, from time.Time) (map[int64][]IncidentLink, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT ic.component_id, i.id, i.title, ic.since, ic.until
		FROM incident_component ic JOIN incident i ON i.id = ic.incident_id
		WHERE ic.component_id = ANY($1) AND (ic.until IS NULL OR ic.until > $2)
		ORDER BY ic.since, ic.id`, ids, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]IncidentLink{}
	for rows.Next() {
		var cid int64
		var l IncidentLink
		if err := rows.Scan(&cid, &l.IncidentID, &l.Title, &l.Start, &l.End); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], l)
	}
	return out, rows.Err()
}

// RecordNotification stores a delivery result.
func (s *Store) RecordNotification(ctx context.Context, n Notification) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO notification (update_id, notifier, ok, attempts, detail, at)
		VALUES ($1, $2, $3, $4, $5, $6)`, n.UpdateID, n.Notifier, n.OK, n.Attempts, n.Detail, n.At)
	return err
}
