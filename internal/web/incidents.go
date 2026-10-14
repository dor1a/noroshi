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
}

type noteView struct {
	OK   bool
	Text string
}

type updateView struct {
	StageKey   string
	StageLabel string
	Body       string
	Author     string
	At         time.Time
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
	Affected   []affectedView
	Updates    []updateView
	Admin      bool // shows authors and delivery results
	Locale     string
	Loc        *time.Location
}

// incidentTone picks the box colour: the worst declared status while open,
// "info" for an open incident that names no component, "resolved" after.
func incidentTone(in store.Incident) string {
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
	for _, c := range in.Components {
		v.Affected = append(v.Affected, affectedView{Name: c.Name.Get(locale), Status: c.Status})
	}
	for _, u := range in.Updates {
		uv := updateView{
			StageKey: u.Stage, StageLabel: i18n.T(locale, "incident.stage."+u.Stage),
			Body: u.Body, At: u.At,
		}
		if admin {
			uv.Author = u.Author
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
	if errors.Is(err, store.ErrNotFound) || (err == nil && in.PageID != p.ID) {
		notFound()
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		loc = time.UTC
	}
	iv := s.incidentView(*in, locale, loc, false)
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
}

type incidentForm struct {
	Title  string
	Stage  string
	Body   string
	Notify bool
	Errors []string
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
	Submit      string
}

func (s *Server) formView(locale, pageKey string, comps []store.ComponentRow, current map[int64]string) formView {
	fv := formView{Locale: locale, PageKey: pageKey, NotifyNames: strings.Join(s.pageNotify[pageKey], ", ")}
	for _, st := range store.Stages {
		fv.Stages = append(fv.Stages, stageChoice{Key: st, Label: i18n.T(locale, "incident.stage."+st)})
	}
	for _, st := range declarable {
		fv.Statuses = append(fv.Statuses, statusChoice{Key: string(st), Label: i18n.T(locale, "status."+string(st)), Glyph: st.Glyph()})
	}
	for _, c := range comps {
		fv.Components = append(fv.Components, compChoice{
			ID: c.ID, Name: c.Name.Get(locale), Group: c.GroupName.Get(locale), Current: current[c.ID],
		})
	}
	return fv
}

// readIncidentForm validates a posted form. raw keeps the submitted
// component choices so the form can be shown again as it was.
func readIncidentForm(r *http.Request, locale string, comps []store.ComponentRow, create, resolved bool) (f incidentForm, statuses map[int64]judge.Status, raw map[int64]string) {
	f = incidentForm{
		Title:  strings.TrimSpace(r.PostFormValue("title")),
		Stage:  r.PostFormValue("stage"),
		Body:   strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")),
		Notify: r.PostFormValue("notify") == "1",
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
		if !slices.Contains(store.Stages, f.Stage) {
			bad("admin.err_stage")
		}
	}
	if create {
		switch {
		case f.Title == "":
			bad("admin.err_title")
		case utf8.RuneCountInString(f.Title) > maxTitle:
			bad("admin.err_title_long")
		}
	}
	switch {
	case f.Body == "":
		bad("admin.err_body")
	case utf8.RuneCountInString(f.Body) > maxBody:
		bad("admin.err_body_long")
	}
	return f, statuses, raw
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
	}
	for _, in := range list {
		row := incidentRow{
			ID: in.ID, Title: in.Title, Page: in.PageTitle.Get(base.Locale), StageKey: in.Stage,
			StageLabel: i18n.T(base.Locale, "incident.stage."+in.Stage), Tone: incidentTone(in),
			Started: in.StartedAt.Local().Format("2006-01-02 15:04"), Resolved: "-",
		}
		var names []string
		for _, c := range in.Components {
			names = append(names, c.Status.Glyph()+" "+c.Name.Get(base.Locale))
		}
		row.Affected = strings.Join(names, ", ")
		if in.ResolvedAt != nil {
			row.Resolved = in.ResolvedAt.Local().Format("2006-01-02 15:04")
			view.Recent = append(view.Recent, row)
		} else {
			view.Open = append(view.Open, row)
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
	base.DocTitle = docTitle(base.Brand, i18n.T(base.Locale, "admin.incident_new"))
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
	comps, err := s.store.PageComponents(r.Context(), p.ID)
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
	fv := s.formView(base.Locale, p.Key, comps, current)
	fv.Form = incidentForm{Stage: store.StageInvestigating, Notify: true}
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
	comps, err := s.store.PageComponents(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	form, statuses, raw := readIncidentForm(r, base.Locale, comps, true, false)
	if len(form.Errors) > 0 {
		fv := s.formView(base.Locale, p.Key, comps, raw)
		fv.Form = form
		s.renderNew(w, http.StatusUnprocessableEntity, base, p, fv)
		return
	}
	id, updateID, err := s.store.CreateIncident(r.Context(), p.ID, form.Title, store.IncidentInput{
		Stage: form.Stage, Body: form.Body, Author: author(r), At: time.Now(), Components: statuses,
	})
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

func (s *Server) renderIncident(w http.ResponseWriter, status int, base adminBase, in *store.Incident, fv formView) {
	base.DocTitle = docTitle(base.Brand, in.Title)
	fv.Action = fmt.Sprintf("/admin/incidents/%d/updates", in.ID)
	fv.Resolved = !in.Open()
	fv.Submit = i18n.T(base.Locale, "admin.incident_post")
	if fv.Resolved {
		fv.Submit = i18n.T(base.Locale, "admin.incident_add_note")
	}
	public := ""
	switch {
	case s.private(in.PageKey):
		public = incidentPath(in.PageKey, in.ID) // on this listener
	case s.env.BaseURL != "":
		public = s.env.BaseURL + incidentPath(in.PageKey, in.ID)
	}
	s.render(w, status, "admin_incident", struct {
		adminBase
		Inc       incidentView
		FormView  formView
		PublicURL string
	}{base, s.incidentView(*in, base.Locale, time.Local, true), fv, public})
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
	comps, err := s.store.PageComponents(r.Context(), in.PageID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	fv := s.formView(base.Locale, in.PageKey, comps, currentStatuses(in))
	fv.Form = incidentForm{Stage: in.Stage, Notify: true}
	s.renderIncident(w, http.StatusOK, base, in, fv)
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
	comps, err := s.store.PageComponents(r.Context(), in.PageID)
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
		s.renderIncident(w, http.StatusUnprocessableEntity, base, in, fv)
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

// announce sends one update to the page's channels in the background and
// records each result.
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
	for _, key := range s.pageNotify[in.PageKey] {
		t, ok := s.targets[key]
		if !ok {
			continue
		}
		locale := t.Locale
		if locale == "" {
			locale = s.env.DefaultLocale
		}
		m := notify.Message{
			Page: in.PageTitle.Get(locale), Title: in.Title, Stage: upd.Stage, Body: upd.Body, At: upd.At,
		}
		switch {
		case s.private(in.PageKey) && s.env.AdminURL != "":
			m.URL = s.env.AdminURL + incidentPath(in.PageKey, in.ID)
		case !s.private(in.PageKey) && s.env.BaseURL != "":
			m.URL = s.env.BaseURL + incidentPath(in.PageKey, in.ID)
		}
		for _, c := range in.Components {
			st := c.Status
			if !in.Open() {
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
