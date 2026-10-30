package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dor1a/noroshi/internal/auth"
	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/notify"
	"github.com/dor1a/noroshi/internal/store"
)

// PastDays is how far back the public page lists resolved incidents.
const PastDays = 14

const (
	adminRecentDays = 30
	maxTitle        = 200
	maxBody         = 5000
)

// declarable are the statuses an admin can set on a component.
var declarable = []judge.Status{judge.Operational, judge.Degraded, judge.Partial, judge.Major, judge.Maintenance}

func incidentPath(pageKey string, id int64) string {
	return fmt.Sprintf("/p/%s/incidents/%d", pageKey, id)
}

type affectedView struct {
	Name   string
	Status judge.Status
	// On admin pages, the first component of each page carries its page
	// when the incident spans more than one.
	PageHead string
}

type noteView struct {
	OK   bool
	Text string
}

type updateView struct {
	ID         int64
	StageKey   string
	StageLabel string
	Body       string
	Author     string
	At         time.Time
	EditedTip  string // set when edited; says when, and by whom on admin pages
	Latest     bool   // the newest update cannot be deleted
	Notes      []noteView
}

type incidentView struct {
	ID         int64
	URL        string
	Title      string
	Page       string
	StageKey   string
	StageLabel string
	Tone       string
	Open       bool
	Started    time.Time
	Resolved   *time.Time
	Window     string // a maintenance's planned window
	Complete   bool   // a maintenance in progress that can end early
	Affected   []affectedView
	Updates    []updateView
	Admin      bool // shows authors and delivery results
	Locale     string
	Loc        *time.Location
}

// incidentTone picks the box colour: the worst declared status while open,
// "info" for an open incident that names no component, "resolved" after.
func incidentTone(in store.Incident) string {
	if in.Maintenance() {
		switch in.Phase(time.Now()) {
		case store.PhaseUpcoming:
			return "info"
		case store.PhaseActive:
			return string(judge.Maintenance)
		}
		return "resolved"
	}
	if !in.Open() {
		return "resolved"
	}
	if w := in.Worst(); w != judge.NoData {
		return string(w)
	}
	return "info"
}

func (s *Server) incidentView(in store.Incident, locale string, loc *time.Location, admin bool) incidentView {
	v := incidentView{
		ID: in.ID, Title: in.Title, Page: in.PageTitle.Get(locale),
		StageKey: in.Stage, StageLabel: i18n.T(locale, "incident.stage."+in.Stage), Tone: incidentTone(in),
		Open: in.Open(), Started: in.StartedAt, Resolved: in.ResolvedAt, Admin: admin, Locale: locale, Loc: loc,
	}
	if !admin {
		v.URL = incidentPath(in.PageKey, in.ID)
	}
	now := time.Now()
	if in.Maintenance() {
		v.StageKey = maintStage(in, now)
		v.StageLabel = i18n.T(locale, "incident.stage."+v.StageKey)
		v.Open = in.Active(now)
		v.Complete = admin && v.Open
		if in.ResolvedAt != nil {
			v.Window = fmtWindowRange(in.StartedAt, *in.ResolvedAt, loc)
		}
	}
	multi := admin && len(in.PageKeys()) > 1
	for i, c := range in.Components {
		av := affectedView{Name: c.Name.Get(locale), Status: c.Status}
		if multi && (i == 0 || in.Components[i-1].PageKey != c.PageKey) {
			av.PageHead = c.PageTitle.Get(locale)
		}
		v.Affected = append(v.Affected, av)
	}
	for i, u := range in.Updates {
		uv := updateView{
			ID: u.ID, StageKey: u.Stage, StageLabel: i18n.T(locale, "incident.stage."+u.Stage),
			Body: u.Body, At: u.At, Latest: i == 0,
		}
		if in.Maintenance() {
			// An update reads as the phase the maintenance was in when it was written.
			uv.StageKey = maintStage(in, u.At)
			uv.StageLabel = i18n.T(locale, "incident.stage."+uv.StageKey)
		}
		if admin {
			uv.Author = u.Author
		}
		if u.EditedAt != nil {
			uv.EditedTip = fmt.Sprintf(i18n.T(locale, "incident.edited_at"), fmtTime(*u.EditedAt, loc))
			if admin && u.EditedBy != "" {
				uv.EditedTip += " · " + u.EditedBy
			}
		}
		for _, n := range u.Notifications {
			if !admin {
				break
			}
			if n.OK {
				uv.Notes = append(uv.Notes, noteView{OK: true, Text: fmt.Sprintf(i18n.T(locale, "admin.notify_ok"), n.Notifier)})
			} else {
				uv.Notes = append(uv.Notes, noteView{Text: fmt.Sprintf(i18n.T(locale, "admin.notify_failed"), n.Notifier, n.Attempts, n.Detail)})
			}
		}
		v.Updates = append(v.Updates, uv)
	}
	return v
}

// ---- public ----

func (s *Server) handlePublicIncident(w http.ResponseWriter, r *http.Request) {
	locale := s.locale(w, r)
	notFound := func() { s.renderError(w, r, http.StatusNotFound, i18n.T(locale, "page.notfound")) }
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound()
		return
	}
	p, all, err := s.findPage(r, r.PathValue("key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		notFound()
		return
	}
	in, err := s.store.Incident(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !in.OnPage(p.Key)) {
		notFound()
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc := pageLocation(*p)
	iv := s.incidentView(in.ForPage(*p), locale, loc, false)
	home := "/"
	if len(all) > 1 || isAdminView(r) {
		home = "/p/" + p.Key
	}
	logo := s.logoURL(p.Key)
	s.render(w, http.StatusOK, "incident", incidentPage{
		Locale: locale, Theme: s.theme(w, r), DocTitle: docTitle(p.Title.Get(locale), in.Title),
		PageTitle: p.Title.Get(locale), Home: home, Logo: logo, Favicon: logo, Inc: iv,
	})
}

type incidentPage struct {
	Locale, Theme, DocTitle, PageTitle, Home, Logo, Favicon string
	Inc                                                     incidentView
}

// handleLogo serves a page's logo. Private pages keep theirs behind sign-in.
func (s *Server) handleLogo(w http.ResponseWriter, r *http.Request) {
	meta := s.pages[r.PathValue("key")]
	if meta.Logo == nil || (meta.Private && !isAdminView(r)) {
		http.NotFound(w, r)
		return
	}
	writeLogo(w, meta.Logo)
}

func (s *Server) handleAdminLogo(w http.ResponseWriter, r *http.Request) {
	if s.admin.Logo == nil {
		http.NotFound(w, r)
		return
	}
	writeLogo(w, s.admin.Logo)
}

func writeLogo(w http.ResponseWriter, l *Logo) {
	h := w.Header()
	h.Set("Content-Type", l.ContentType)
	h.Set("Cache-Control", "public, max-age=86400")
	// An SVG logo is a document of its own; it gets no scripts or requests.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Write(l.Data) //nolint:errcheck
}

// ---- admin ----

type stageChoice struct{ Key, Label string }

type statusChoice struct{ Key, Label, Glyph string }

type compChoice struct {
	ID      int64
	Name    string
	Group   string
	Current string
	// The first component of each page carries its page when the form
	// lists more than one.
	PageHead    string
	PagePrivate bool
}

// compChoices lists every page's components for a form, the incident's own
// page first, so one incident can name components on several pages.
func (s *Server) compChoices(locale, pageKey string, comps []store.ComponentRow, current map[int64]string) []compChoice {
	ordered := slices.Clone(comps)
	slices.SortStableFunc(ordered, func(a, b store.ComponentRow) int {
		switch {
		case a.PageKey == pageKey && b.PageKey != pageKey:
			return -1
		case a.PageKey != pageKey && b.PageKey == pageKey:
			return 1
		}
		return 0
	})
	multi := len(ordered) > 0 && slices.ContainsFunc(ordered, func(c store.ComponentRow) bool { return c.PageKey != ordered[0].PageKey })
	var out []compChoice
	for i, c := range ordered {
		cc := compChoice{ID: c.ID, Name: c.Name.Get(locale), Group: c.GroupName.Get(locale), Current: current[c.ID]}
		if multi && (i == 0 || ordered[i-1].PageKey != c.PageKey) {
			cc.PageHead, cc.PagePrivate = c.PageTitle.Get(locale), s.private(c.PageKey)
		}
		out = append(out, cc)
	}
	return out
}

type incidentForm struct {
	Title  string
	Stage  string
	Body   string
	Notify bool
	Errors []string
	// datetime-local values in the page's time zone, for a new incident
	// recorded after the fact
	StartedAt, ResolvedAt string
}

type formView struct {
	Locale      string
	Action      string
	Create      bool
	Resolved    bool
	PageKey     string
	Form        incidentForm
	Stages      []stageChoice
	Statuses    []statusChoice
	Components  []compChoice
	NotifyNames string
	NotifyOther bool // some of the channels belong to other pages
	Submit      string
	TimeZone    string
	Maintenance bool
}

func (s *Server) formView(locale, pageKey string, comps []store.ComponentRow, current map[int64]string) formView {
	// The page's own channels first, then those of other pages, which an
	// incident reaches by naming their components.
	names := slices.Clone(s.pageNotify[pageKey])
	var other []string
	for key, list := range s.pageNotify {
		for _, n := range list {
			if key != pageKey && !slices.Contains(names, n) && !slices.Contains(other, n) {
				other = append(other, n)
			}
		}
	}
	slices.Sort(other)
	fv := formView{Locale: locale, PageKey: pageKey, NotifyNames: strings.Join(append(names, other...), ", "), NotifyOther: len(other) > 0}
	for _, st := range store.Stages {
		fv.Stages = append(fv.Stages, stageChoice{Key: st, Label: i18n.T(locale, "incident.stage."+st)})
	}
	for _, st := range declarable {
		fv.Statuses = append(fv.Statuses, statusChoice{Key: string(st), Label: i18n.T(locale, "status."+string(st)), Glyph: st.Glyph()})
	}
	fv.Components = s.compChoices(locale, pageKey, comps, current)
	return fv
}

// readIncidentForm validates a posted form. raw keeps the submitted
// component choices so the form can be shown again as it was.
func readIncidentForm(r *http.Request, locale string, comps []store.ComponentRow, create, resolved bool) (f incidentForm, statuses map[int64]judge.Status, raw map[int64]string) {
	f = incidentForm{
		Title:      strings.TrimSpace(r.PostFormValue("title")),
		Stage:      r.PostFormValue("stage"),
		Body:       postedBody(r),
		Notify:     r.PostFormValue("notify") == "1",
		StartedAt:  strings.TrimSpace(r.PostFormValue("started_at")),
		ResolvedAt: strings.TrimSpace(r.PostFormValue("resolved_at")),
	}
	bad := func(key string) { f.Errors = append(f.Errors, i18n.T(locale, key)) }
	statuses, raw = map[int64]judge.Status{}, map[int64]string{}
	if !resolved {
		for _, c := range comps {
			v := r.PostFormValue(fmt.Sprintf("c%d", c.ID))
			if v == "" {
				continue
			}
			raw[c.ID] = v
			if !slices.Contains(declarable, judge.Status(v)) {
				bad("admin.err_status")
				continue
			}
			statuses[c.ID] = judge.Status(v)
		}
	}
	// A maintenance has no stage to pick; its window decides what it shows.
	maint := r.PostFormValue("kind") == store.KindMaintenance
	for _, key := range incidentErrors(f.Stage, f.Title, f.Body, create, resolved || maint) {
		bad(key)
	}
	return f, statuses, raw
}

// incidentErrors returns the message keys for an unusable stage, title, or
// body. The stage is not checked on a resolved incident, and the title only
// when creating one.
func incidentErrors(stage, title, body string, create, resolved bool) []string {
	var keys []string
	if !resolved && !slices.Contains(store.Stages, stage) {
		keys = append(keys, "admin.err_stage")
	}
	if create {
		switch {
		case title == "":
			keys = append(keys, "admin.err_title")
		case utf8.RuneCountInString(title) > maxTitle:
			keys = append(keys, "admin.err_title_long")
		}
	}
	if key := checkBody(body); key != "" {
		keys = append(keys, key)
	}
	return keys
}

// pageLocation is the page's time zone, UTC if it does not load.
func pageLocation(p store.PageRow) *time.Location {
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// parseLocal reads a datetime-local form value in loc; empty is nil.
func parseLocal(v string, loc *time.Location) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, loc); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("bad time %q", v)
}

// maintStage is the stage a maintenance shows at t: maint_upcoming,
// maint_active, or maint_done.
func maintStage(in store.Incident, t time.Time) string { return "maint_" + in.Phase(t) }

// fmtWindowRange prints a maintenance window, the end without its date when
// it falls on the same day: "2026-10-31 02:00 KST – 04:00".
func fmtWindowRange(start, end time.Time, loc *time.Location) string {
	s, e := start.In(loc), end.In(loc)
	if s.Format("2006-01-02") == e.Format("2006-01-02") {
		return fmtTime(start, loc) + " – " + e.Format("15:04")
	}
	return fmtTime(start, loc) + " – " + fmtTime(end, loc)
}

// maintenanceErrors checks a new maintenance's window: both ends given, the
// end after the start and not already past.
func maintenanceErrors(start, end *time.Time, now time.Time) []string {
	switch {
	case start == nil || end == nil:
		return []string{"admin.err_maint_times"}
	case !end.After(*start):
		return []string{"admin.err_resolved_order"}
	case !end.After(now):
		return []string{"admin.err_maint_over"}
	}
	return nil
}

// maintenanceStatuses puts every chosen component under maintenance, which
// is the only status a maintenance declares.
func maintenanceStatuses(in map[int64]judge.Status) map[int64]judge.Status {
	out := make(map[int64]judge.Status, len(in))
	for id := range in {
		out[id] = judge.Maintenance
	}
	return out
}

// clockSkew is how far ahead of the server a start or resolve time may be.
const clockSkew = time.Minute

// backfillErrors checks the optional times of a new incident recorded after
// the fact. A resolve time needs the resolved stage, neither time may be in
// the future, and the incident must start before it is resolved.
func backfillErrors(stage string, started, resolved *time.Time, now time.Time) []string {
	var keys []string
	if started != nil && started.After(now.Add(clockSkew)) {
		keys = append(keys, "admin.err_started_future")
	}
	if resolved != nil {
		switch {
		case stage != store.StageResolved:
			keys = append(keys, "admin.err_resolved_stage")
		case resolved.After(now.Add(clockSkew)):
			keys = append(keys, "admin.err_resolved_future")
		case started != nil && !resolved.After(*started):
			keys = append(keys, "admin.err_resolved_order")
		}
	}
	return keys
}

// newIncident builds the store input for a new incident, moving its start
// and first update to the given times when it is recorded after the fact.
func newIncident(stage, body, by string, statuses map[int64]judge.Status, started, resolved *time.Time, now time.Time) store.IncidentInput {
	in := store.IncidentInput{Stage: stage, Body: body, Author: by, At: now, Components: statuses}
	if resolved != nil {
		in.At = *resolved
	}
	if started != nil {
		in.Since = *started
	}
	return in
}

// newMaintenance builds the store input for a maintenance planned for
// [start, end): written now, its chosen components under maintenance for
// the window.
func newMaintenance(body, by string, statuses map[int64]judge.Status, start, end, now time.Time) store.IncidentInput {
	return store.IncidentInput{
		Kind: store.KindMaintenance, Stage: store.StageScheduled, Body: body, Author: by, At: now,
		Components: maintenanceStatuses(statuses), Since: start, Until: &end,
	}
}

func postedBody(r *http.Request) string { return normBody(r.PostFormValue("body")) }

func normBody(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")) }

// checkBody returns the message key for an unusable body, or "".
func checkBody(body string) string {
	switch {
	case body == "":
		return "admin.err_body"
	case utf8.RuneCountInString(body) > maxBody:
		return "admin.err_body_long"
	}
	return ""
}

func readForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	return r.ParseForm()
}

func author(r *http.Request) string {
	if u := auth.UserFrom(r.Context()); u != nil {
		if u.Name != "" {
			return u.Name
		}
		return u.Email
	}
	return "unknown"
}

type incidentRow struct {
	ID         int64
	Title      string
	Page       string
	StageKey   string
	StageLabel string
	Tone       string
	Affected   string
	Started    string
	Resolved   string
}

type newLink struct{ Href, Label string }

// affectedByPage names the pages an incident shows on and its components,
// put under their page's title when there is more than one page.
func affectedByPage(in store.Incident, locale string) (pages, affected string) {
	keys := in.PageKeys()
	titles := []string{in.PageTitle.Get(locale)}
	var parts []string
	for _, key := range keys {
		var names []string
		title := ""
		for _, c := range in.Components {
			if c.PageKey == key {
				names = append(names, c.Status.Glyph()+" "+c.Name.Get(locale))
				title = c.PageTitle.Get(locale)
			}
		}
		if key != in.PageKey {
			titles = append(titles, title)
		}
		if len(names) == 0 {
			continue
		}
		part := strings.Join(names, ", ")
		if len(keys) > 1 {
			part = title + ": " + part
		}
		parts = append(parts, part)
	}
	return strings.Join(titles, " · "), strings.Join(parts, " · ")
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	list, err := s.store.AllIncidents(r.Context(), time.Now().AddDate(0, 0, -adminRecentDays))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pages, err := s.store.Pages(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	view := struct {
		adminBase
		Open, Recent []incidentRow
		New          []newLink
		RecentDays   int
	}{adminBase: base, RecentDays: adminRecentDays}
	for _, p := range pages {
		label := i18n.T(base.Locale, "admin.incident_new")
		if len(pages) > 1 {
			label = fmt.Sprintf(i18n.T(base.Locale, "admin.incident_new_for"), p.Title.Get(base.Locale))
		}
		view.New = append(view.New, newLink{Href: "/admin/incidents/new?page=" + p.Key, Label: label})
		mlabel := i18n.T(base.Locale, "admin.maint_new")
		if len(pages) > 1 {
			mlabel = fmt.Sprintf(i18n.T(base.Locale, "admin.maint_new_for"), p.Title.Get(base.Locale))
		}
		view.New = append(view.New, newLink{Href: "/admin/incidents/new?kind=maintenance&page=" + p.Key, Label: mlabel})
	}
	now := time.Now()
	for _, in := range list {
		row := incidentRow{
			ID: in.ID, Title: in.Title, StageKey: in.Stage,
			StageLabel: i18n.T(base.Locale, "incident.stage."+in.Stage), Tone: incidentTone(in),
			Started: in.StartedAt.Local().Format("2006-01-02 15:04"), Resolved: "-",
		}
		row.Page, row.Affected = affectedByPage(in, base.Locale)
		if in.ResolvedAt != nil {
			row.Resolved = in.ResolvedAt.Local().Format("2006-01-02 15:04")
		}
		if in.Maintenance() {
			row.StageKey = maintStage(in, now)
			row.StageLabel = i18n.T(base.Locale, "incident.stage."+row.StageKey)
		}
		// Upcoming and running maintenance sit with the open incidents.
		if in.Active(now) || in.Maintenance() && in.Phase(now) == store.PhaseUpcoming {
			view.Open = append(view.Open, row)
		} else {
			view.Recent = append(view.Recent, row)
		}
	}
	s.render(w, http.StatusOK, "admin_incidents", view)
}

func (s *Server) pageByKey(ctx context.Context, key string) (*store.PageRow, error) {
	pages, err := s.store.Pages(ctx)
	if err != nil {
		return nil, err
	}
	for i := range pages {
		if pages[i].Key == key || (key == "" && len(pages) == 1) {
			return &pages[i], nil
		}
	}
	return nil, nil
}

func (s *Server) renderNew(w http.ResponseWriter, status int, base adminBase, p *store.PageRow, fv formView) {
	fv.Action, fv.Create, fv.Submit = "/admin/incidents", true, i18n.T(base.Locale, "admin.incident_create")
	fv.TimeZone = p.Timezone
	heading := "admin.incident_new"
	if fv.Maintenance {
		fv.Submit, heading = i18n.T(base.Locale, "admin.maint_create"), "admin.maint_new"
		fv.Statuses = []statusChoice{{Key: string(judge.Maintenance), Label: i18n.T(base.Locale, "status."+string(judge.Maintenance)), Glyph: judge.Maintenance.Glyph()}}
	}
	base.DocTitle = docTitle(base.Brand, i18n.T(base.Locale, heading))
	s.render(w, status, "admin_incident_new", struct {
		adminBase
		PageTitle string
		FormView  formView
	}{base, p.Title.Get(base.Locale), fv})
}

func (s *Server) handleIncidentNew(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.pageByKey(r.Context(), r.URL.Query().Get("page"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		http.Redirect(w, r, "/admin/incidents", http.StatusFound)
		return
	}
	comps, err := s.store.AllComponents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Start from what the checks see now, so an outage is one click away.
	d, err := s.loadPage(r.Context(), *p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	current := map[int64]string{}
	for _, c := range d.Components {
		if slices.Contains(declarable, c.Status) && c.Status != judge.Operational {
			current[c.ID] = string(c.Status)
		}
	}
	form := incidentForm{Stage: store.StageInvestigating, Notify: true}
	q := r.URL.Query()
	if q.Get("kind") == store.KindMaintenance {
		fv := s.formView(base.Locale, p.Key, comps, map[int64]string{})
		fv.Form, fv.Maintenance = form, true
		s.renderNew(w, http.StatusOK, base, p, fv)
		return
	}
	// A link from an outage on the admin pages fills the form with it. A past
	// one opens resolved and unannounced.
	if key := q.Get("component"); key != "" {
		st := judge.Status(q.Get("status"))
		if !slices.Contains(declarable, st) || st == judge.Operational {
			st = judge.Major
		}
		for _, c := range comps {
			if c.Key == key && c.PageKey == p.Key {
				current = map[int64]string{c.ID: string(st)}
				form.Title = fmt.Sprintf(i18n.T(base.Locale, "admin.prefill_title"), c.Name.Get(base.Locale))
			}
		}
	}
	form.StartedAt = q.Get("started_at")
	if v := q.Get("resolved_at"); v != "" {
		form.ResolvedAt, form.Stage, form.Notify = v, store.StageResolved, false
	}
	fv := s.formView(base.Locale, p.Key, comps, current)
	fv.Form = form
	s.renderNew(w, http.StatusOK, base, p, fv)
}

func (s *Server) handleIncidentCreate(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	p, err := s.pageByKey(r.Context(), r.PostFormValue("page"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p == nil {
		s.renderError(w, r, http.StatusNotFound, i18n.T(base.Locale, "page.notfound"))
		return
	}
	comps, err := s.store.AllComponents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	form, statuses, raw := readIncidentForm(r, base.Locale, comps, true, false)
	maint := r.PostFormValue("kind") == store.KindMaintenance
	now := time.Now()
	started, errStart := parseLocal(form.StartedAt, pageLocation(*p))
	resolved, errResolve := parseLocal(form.ResolvedAt, pageLocation(*p))
	var keys []string
	switch {
	case errStart != nil || errResolve != nil:
		keys = []string{"admin.err_time"}
	case maint:
		keys = maintenanceErrors(started, resolved, now)
	default:
		keys = backfillErrors(form.Stage, started, resolved, now)
	}
	for _, key := range keys {
		form.Errors = append(form.Errors, i18n.T(base.Locale, key))
	}
	if len(form.Errors) > 0 {
		fv := s.formView(base.Locale, p.Key, comps, raw)
		fv.Form, fv.Maintenance = form, maint
		s.renderNew(w, http.StatusUnprocessableEntity, base, p, fv)
		return
	}
	in := newIncident(form.Stage, form.Body, author(r), statuses, started, resolved, now)
	if maint {
		in = newMaintenance(form.Body, author(r), statuses, *started, *resolved, now)
	}
	id, updateID, err := s.store.CreateIncident(r.Context(), p.ID, form.Title, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident created", "incident", id, "page", p.Key, "stage", form.Stage, "by", author(r))
	if form.Notify {
		s.announce(r.Context(), id, updateID)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", id), http.StatusSeeOther)
}

func (s *Server) loadIncident(w http.ResponseWriter, r *http.Request, locale string) *store.Incident {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, i18n.T(locale, "page.notfound"))
		return nil
	}
	in, err := s.store.Incident(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusNotFound, i18n.T(locale, "page.notfound"))
		return nil
	}
	if err != nil {
		s.fail(w, r, err)
		return nil
	}
	return in
}

func (s *Server) renderIncident(w http.ResponseWriter, status int, base adminBase, in *store.Incident, fv formView, ev editView) {
	base.DocTitle = docTitle(base.Brand, in.Title)
	fv.Action = fmt.Sprintf("/admin/incidents/%d/updates", in.ID)
	fv.Resolved = !in.Open()
	fv.Submit = i18n.T(base.Locale, "admin.incident_post")
	if fv.Resolved {
		fv.Submit = i18n.T(base.Locale, "admin.incident_add_note")
	}
	// One link for each page the incident shows on.
	keys := in.PageKeys()
	var links []newLink
	for _, key := range keys {
		href := ""
		switch {
		case s.private(key):
			href = incidentPath(key, in.ID) // on this listener
		case s.env.BaseURL != "":
			href = s.env.BaseURL + incidentPath(key, in.ID)
		}
		if href == "" {
			continue
		}
		label := i18n.T(base.Locale, "admin.incident_public")
		if len(keys) > 1 {
			title := in.PageTitle.Get(base.Locale)
			for _, c := range in.Components {
				if key != in.PageKey && c.PageKey == key {
					title = c.PageTitle.Get(base.Locale)
					break
				}
			}
			label = fmt.Sprintf(i18n.T(base.Locale, "admin.incident_view_on"), title)
		}
		links = append(links, newLink{Href: href, Label: label})
	}
	s.render(w, status, "admin_incident", struct {
		adminBase
		Inc       incidentView
		FormView  formView
		Edit      editView
		PageLinks []newLink
	}{base, s.incidentView(*in, base.Locale, time.Local, true), fv, ev, links})
}

// editView is the form that corrects an incident's record.
type editView struct {
	Action     string
	Open       bool
	Title      string
	StartedAt  string
	ResolvedAt string
	TimeZone   string
	Statuses   []statusChoice
	Components []compChoice
	Errors     []string
	Show       bool // open after a failed save
}

// editView fills the correction form from the incident, with times in the
// page's time zone like the new incident form.
func (s *Server) editView(ctx context.Context, locale string, in *store.Incident, comps []store.ComponentRow) (editView, *time.Location, error) {
	p, err := s.pageByKey(ctx, in.PageKey)
	if err != nil {
		return editView{}, nil, err
	}
	loc, tz := time.UTC, "UTC"
	if p != nil {
		loc, tz = pageLocation(*p), p.Timezone
	}
	ev := editView{
		Action: fmt.Sprintf("/admin/incidents/%d/edit", in.ID), Open: in.Open(), Title: in.Title,
		StartedAt: in.StartedAt.In(loc).Format(localInput), TimeZone: tz,
	}
	if in.ResolvedAt != nil {
		ev.ResolvedAt = in.ResolvedAt.In(loc).Format(localInput)
	}
	cur := map[int64]string{}
	for _, c := range in.Components {
		cur[c.ComponentID] = string(c.Status)
	}
	for _, st := range declarable {
		ev.Statuses = append(ev.Statuses, statusChoice{Key: string(st), Label: i18n.T(locale, "status."+string(st)), Glyph: st.Glyph()})
	}
	ev.Components = s.compChoices(locale, in.PageKey, comps, cur)
	return ev, loc, nil
}

// editErrors checks a correction: a usable title, no time in the future,
// a resolved incident that does not end before it starts, and no resolve
// time or components on an open incident, which change with an update.
func editErrors(in *store.Incident, e store.IncidentEdit, now time.Time) []string {
	var keys []string
	if e.Title != nil {
		switch {
		case *e.Title == "":
			keys = append(keys, "admin.err_title")
		case utf8.RuneCountInString(*e.Title) > maxTitle:
			keys = append(keys, "admin.err_title_long")
		}
	}
	// A maintenance is planned, so its window may lie ahead.
	if e.StartedAt != nil && e.StartedAt.After(now.Add(clockSkew)) && !in.Maintenance() {
		keys = append(keys, "admin.err_started_future")
	}
	if in.Open() {
		if e.ResolvedAt != nil || e.Components != nil {
			keys = append(keys, "admin.err_edit_open")
		}
		return keys
	}
	if e.ResolvedAt != nil && e.ResolvedAt.After(now.Add(clockSkew)) && !in.Maintenance() {
		keys = append(keys, "admin.err_resolved_future")
	}
	start, end := in.StartedAt, *in.ResolvedAt
	if e.StartedAt != nil {
		start = *e.StartedAt
	}
	if e.ResolvedAt != nil {
		end = *e.ResolvedAt
	}
	if end.Before(start) {
		keys = append(keys, "admin.err_resolved_order")
	}
	return keys
}

// readEditForm turns the posted correction into an edit, leaving out what
// did not change: a time still shows as the form printed it, so seconds
// the form cannot show are not lost by saving the title alone.
func readEditForm(r *http.Request, in *store.Incident, ev editView, loc *time.Location) (e store.IncidentEdit, bad bool) {
	if title := strings.TrimSpace(r.PostFormValue("title")); title != in.Title {
		e.Title = &title
	}
	for _, f := range []struct {
		name, was string
		dst       **time.Time
	}{{"started_at", ev.StartedAt, &e.StartedAt}, {"resolved_at", ev.ResolvedAt, &e.ResolvedAt}} {
		v := strings.TrimSpace(r.PostFormValue(f.name))
		if v == "" || v == f.was {
			continue
		}
		t, err := parseLocal(v, loc)
		if err != nil {
			bad = true
			continue
		}
		*f.dst = t
	}
	if in.Open() {
		return e, bad
	}
	chosen, same := map[int64]judge.Status{}, true
	for _, c := range ev.Components {
		v := r.PostFormValue(fmt.Sprintf("c%d", c.ID))
		if v != "" {
			if !slices.Contains(declarable, judge.Status(v)) {
				bad = true
				continue
			}
			chosen[c.ID] = judge.Status(v)
		}
		if v != c.Current {
			same = false
		}
	}
	if !same {
		e.Components = chosen
	}
	return e, bad
}

// handleMaintenanceComplete ends a maintenance in progress now, which
// also ends its components' maintenance status.
func (s *Server) handleMaintenanceComplete(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := s.loadIncident(w, r, base.Locale)
	if in == nil {
		return
	}
	now := time.Now()
	if in.Maintenance() && in.Active(now) {
		if err := s.store.EditIncident(r.Context(), in.ID, store.IncidentEdit{ResolvedAt: &now}); err != nil {
			s.fail(w, r, err)
			return
		}
		s.invalidate()
		s.log.Info("maintenance completed early", "incident", in.ID, "by", author(r))
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", in.ID), http.StatusSeeOther)
}

func (s *Server) handleIncidentEdit(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	in := s.loadIncident(w, r, base.Locale)
	if in == nil {
		return
	}
	comps, err := s.store.AllComponents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ev, loc, err := s.editView(r.Context(), base.Locale, in, comps)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	e, bad := readEditForm(r, in, ev, loc)
	keys := editErrors(in, e, time.Now())
	if bad {
		keys = append(keys, "admin.err_time")
	}
	if len(keys) > 0 {
		for _, k := range keys {
			ev.Errors = append(ev.Errors, i18n.T(base.Locale, k))
		}
		ev.Show = true
		fv := s.formView(base.Locale, in.PageKey, comps, currentStatuses(in))
		fv.Form = incidentForm{Stage: in.Stage, Notify: true}
		s.renderIncident(w, http.StatusUnprocessableEntity, base, in, fv, ev)
		return
	}
	if err := s.store.EditIncident(r.Context(), in.ID, e); err != nil {
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident edited", "incident", in.ID, "by", author(r))
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", in.ID), http.StatusSeeOther)
}

func currentStatuses(in *store.Incident) map[int64]string {
	out := map[int64]string{}
	if in.Open() {
		for _, c := range in.Components {
			out[c.ComponentID] = string(c.Status)
		}
	}
	return out
}

func (s *Server) handleIncident(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	in := s.loadIncident(w, r, base.Locale)
	if in == nil {
		return
	}
	comps, err := s.store.AllComponents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	fv := s.formView(base.Locale, in.PageKey, comps, currentStatuses(in))
	fv.Form = incidentForm{Stage: in.Stage, Notify: true}
	ev, _, err := s.editView(r.Context(), base.Locale, in, comps)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderIncident(w, http.StatusOK, base, in, fv, ev)
}

func (s *Server) handleIncidentUpdate(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	in := s.loadIncident(w, r, base.Locale)
	if in == nil {
		return
	}
	comps, err := s.store.AllComponents(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	form, statuses, raw := readIncidentForm(r, base.Locale, comps, false, !in.Open())
	if !in.Open() {
		form.Stage = store.StageResolved
	}
	if len(form.Errors) > 0 {
		fv := s.formView(base.Locale, in.PageKey, comps, raw)
		fv.Form = form
		ev, _, err := s.editView(r.Context(), base.Locale, in, comps)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.renderIncident(w, http.StatusUnprocessableEntity, base, in, fv, ev)
		return
	}
	updateID, err := s.store.UpdateIncident(r.Context(), in.ID, store.IncidentInput{
		Stage: form.Stage, Body: form.Body, Author: author(r), At: time.Now(), Components: statuses,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident updated", "incident", in.ID, "stage", form.Stage, "by", author(r))
	if form.Notify {
		s.announce(r.Context(), in.ID, updateID)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", in.ID), http.StatusSeeOther)
}

func (s *Server) handleIncidentDelete(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	in := s.loadIncident(w, r, base.Locale)
	if in == nil {
		return
	}
	if r.PostFormValue("confirm") != "1" {
		http.Redirect(w, r, fmt.Sprintf("/admin/incidents/%d", in.ID), http.StatusSeeOther)
		return
	}
	if err := s.store.DeleteIncident(r.Context(), in.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Warn("incident deleted", "incident", in.ID, "title", in.Title, "by", author(r))
	http.Redirect(w, r, "/admin/incidents", http.StatusSeeOther)
}

// loadUpdate finds the update named in the path within its incident.
func (s *Server) loadUpdate(w http.ResponseWriter, r *http.Request, locale string) (*store.Incident, *store.IncidentUpdate) {
	in := s.loadIncident(w, r, locale)
	if in == nil {
		return nil, nil
	}
	if uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64); err == nil {
		for i := range in.Updates {
			if in.Updates[i].ID == uid {
				return in, &in.Updates[i]
			}
		}
	}
	s.renderError(w, r, http.StatusNotFound, i18n.T(locale, "page.notfound"))
	return nil, nil
}

// handleUpdateEdit corrects an update's text. It is not announced again.
func (s *Server) handleUpdateEdit(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	in, u := s.loadUpdate(w, r, base.Locale)
	if u == nil {
		return
	}
	body := postedBody(r)
	if key := checkBody(body); key != "" {
		s.renderError(w, r, http.StatusUnprocessableEntity, i18n.T(base.Locale, key))
		return
	}
	back := fmt.Sprintf("/admin/incidents/%d", in.ID)
	if body == u.Body {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if err := s.store.EditUpdate(r.Context(), in.ID, u.ID, body, author(r), time.Now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident update edited", "incident", in.ID, "update", u.ID, "by", author(r))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleUpdateDelete(w http.ResponseWriter, r *http.Request) {
	base, err := s.adminBase(w, r, "incidents")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := readForm(w, r); err != nil {
		s.renderError(w, r, http.StatusBadRequest, http.StatusText(http.StatusBadRequest))
		return
	}
	in, u := s.loadUpdate(w, r, base.Locale)
	if u == nil {
		return
	}
	back := fmt.Sprintf("/admin/incidents/%d", in.ID)
	if r.PostFormValue("confirm") != "1" {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	switch err := s.store.DeleteUpdate(r.Context(), in.ID, u.ID); {
	case errors.Is(err, store.ErrLatestUpdate):
		s.renderError(w, r, http.StatusConflict, i18n.T(base.Locale, "admin.err_delete_latest"))
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.fail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Warn("incident update deleted", "incident", in.ID, "update", u.ID, "stage", u.Stage, "at", u.At, "by", author(r))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// listenerBase is the base URL of the listener that serves a page: the
// admin one for a private page, the public one otherwise. It may be empty.
func (s *Server) listenerBase(pageKey string) string {
	if s.private(pageKey) {
		return s.env.AdminURL
	}
	return s.env.BaseURL
}

// pageURL and incidentURL are where people outside can read a page or an
// incident. They are empty when the listener's base URL is not set.
func (s *Server) pageURL(pageKey string) string {
	if b := s.listenerBase(pageKey); b != "" {
		return b + "/p/" + pageKey
	}
	return ""
}

func (s *Server) incidentURL(pageKey string, id int64) string {
	if b := s.listenerBase(pageKey); b != "" {
		return b + incidentPath(pageKey, id)
	}
	return ""
}

// announce sends one update to the channels of every page the incident
// shows on, in the background, and records each result. A channel gets it
// once, from the first of those pages that uses it, with that page's title,
// link, and components only.
func (s *Server) announce(ctx context.Context, incidentID, updateID int64) {
	in, err := s.store.Incident(ctx, incidentID)
	if err != nil {
		s.log.Error("announce: load incident failed", "incident", incidentID, "err", err)
		return
	}
	var upd *store.IncidentUpdate
	for i := range in.Updates {
		if in.Updates[i].ID == updateID {
			upd = &in.Updates[i]
		}
	}
	if upd == nil {
		return
	}
	pages, err := s.store.Pages(ctx)
	if err != nil {
		s.log.Error("announce: load pages failed", "incident", incidentID, "err", err)
		return
	}
	sent := map[string]bool{}
	for _, pageKey := range in.PageKeys() {
		i := slices.IndexFunc(pages, func(p store.PageRow) bool { return p.Key == pageKey })
		if i < 0 {
			continue
		}
		view := in.ForPage(pages[i])
		for _, key := range s.pageNotify[pageKey] {
			t, ok := s.targets[key]
			if !ok || sent[key] {
				continue
			}
			sent[key] = true
			locale := t.Locale
			if locale == "" {
				locale = s.env.DefaultLocale
			}
			m := notify.Message{
				Page: view.PageTitle.Get(locale), Title: view.Title, Stage: upd.Stage, Body: upd.Body, At: upd.At,
				URL: s.incidentURL(view.PageKey, view.ID),
			}
			if view.Maintenance() && view.ResolvedAt != nil {
				m.Body = i18n.T(locale, "maint.window") + ": " + fmtWindowRange(view.StartedAt, *view.ResolvedAt, pageLocation(pages[i])) + "\n" + m.Body
			}
			for _, c := range view.Components {
				st := c.Status
				if !view.Open() && !view.Maintenance() {
					st = judge.Operational // the declared status ended with the incident
				}
				m.Components = append(m.Components, notify.Component{Name: c.Name.Get(locale), Status: st})
			}
			s.bg.Add(1)
			go func() {
				defer s.bg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				res := s.sender.Send(ctx, t, m)
				if err := s.store.RecordNotification(ctx, store.Notification{
					UpdateID: updateID, Notifier: t.Key, OK: res.OK, Attempts: res.Attempts, Detail: res.Detail, At: time.Now(),
				}); err != nil {
					s.log.Error("record notification failed", "notifier", t.Key, "err", err)
				}
				if res.OK {
					s.log.Info("announcement sent", "notifier", t.Key, "incident", incidentID, "attempts", res.Attempts)
				} else {
					s.log.Warn("announcement failed", "notifier", t.Key, "incident", incidentID, "attempts", res.Attempts, "detail", res.Detail)
				}
			}()
		}
	}
}
