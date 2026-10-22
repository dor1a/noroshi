package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/judge"
	"github.com/dor1a/noroshi/internal/store"
)

// The admin API lets scripts write incidents with the same rules as the
// admin pages. It takes the bearer tokens named in the config and never a
// session cookie, so a browser cannot be led into calling it.

type apiToken struct {
	key string
	sum [sha256.Size]byte
}

// SetAPITokens sets the tokens the admin API accepts, by key.
func (s *Server) SetAPITokens(tokens map[string]string) {
	s.apiTokens = nil
	for key, v := range tokens {
		s.apiTokens = append(s.apiTokens, apiToken{key: key, sum: sha256.Sum256([]byte(v))})
	}
}

type apiKeyCtx struct{}

// apiAuth lets a request through when it carries a known bearer token.
func (s *Server) apiAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := s.tokenKey(r.Header.Get("Authorization"))
		if key == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="noroshi"`)
			apiError(w, http.StatusUnauthorized, "missing or unknown bearer token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), apiKeyCtx{}, key)))
	})
}

// tokenKey returns the key of the token in an Authorization header, or "".
// It compares against every token in constant time.
func (s *Server) tokenKey(header string) string {
	scheme, tok, ok := strings.Cut(header, " ")
	tok = strings.TrimSpace(tok)
	if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(tok))
	found := ""
	for _, t := range s.apiTokens {
		if subtle.ConstantTimeCompare(sum[:], t.sum[:]) == 1 {
			found = t.key
		}
	}
	return found
}

func apiAuthor(r *http.Request) string {
	key, _ := r.Context().Value(apiKeyCtx{}).(string)
	return "api:" + key
}

type adminIncident struct {
	ID         int64            `json:"id"`
	Page       string           `json:"page"`
	Title      string           `json:"title"`
	Stage      string           `json:"stage"`
	StartedAt  time.Time        `json:"started_at"`
	ResolvedAt *time.Time       `json:"resolved_at,omitempty"`
	URL        string           `json:"url,omitempty"`
	Components []adminComponent `json:"components"`
	Updates    []adminUpdate    `json:"updates"` // newest first
}

type adminComponent struct {
	Key    string       `json:"key"`
	Status judge.Status `json:"status"`
}

type adminUpdate struct {
	ID            int64               `json:"id"`
	Stage         string              `json:"stage"`
	Body          string              `json:"body"`
	At            time.Time           `json:"at"`
	Author        string              `json:"author"`
	EditedAt      *time.Time          `json:"edited_at,omitempty"`
	EditedBy      string              `json:"edited_by,omitempty"`
	Notifications []adminNotification `json:"notifications,omitempty"`
}

type adminNotification struct {
	Notifier string `json:"notifier"`
	OK       bool   `json:"ok"`
	Attempts int    `json:"attempts"`
	Detail   string `json:"detail,omitempty"`
}

func (s *Server) adminIncidentJSON(in store.Incident) adminIncident {
	out := adminIncident{
		ID: in.ID, Page: in.PageKey, Title: in.Title, Stage: in.Stage, StartedAt: in.StartedAt,
		ResolvedAt: in.ResolvedAt, URL: s.incidentURL(in.PageKey, in.ID),
		Components: []adminComponent{}, Updates: []adminUpdate{},
	}
	for _, c := range in.Components {
		out.Components = append(out.Components, adminComponent{Key: c.Key, Status: c.Status})
	}
	for _, u := range in.Updates {
		au := adminUpdate{
			ID: u.ID, Stage: u.Stage, Body: u.Body, At: u.At, Author: u.Author,
			EditedAt: u.EditedAt, EditedBy: u.EditedBy,
		}
		for _, n := range u.Notifications {
			au.Notifications = append(au.Notifications, adminNotification{Notifier: n.Notifier, OK: n.OK, Attempts: n.Attempts, Detail: n.Detail})
		}
		out.Updates = append(out.Updates, au)
	}
	return out
}

type adminPage struct {
	Key        string       `json:"key"`
	Title      string       `json:"title"`
	Visibility string       `json:"visibility"`
	Status     judge.Status `json:"status"`
	URL        string       `json:"url,omitempty"`
	Groups     []adminGroup `json:"groups"`
}

type adminGroup struct {
	Key        string               `json:"key"`
	Name       string               `json:"name"`
	Components []adminPageComponent `json:"components"`
}

type adminPageComponent struct {
	Key    string       `json:"key"`
	Name   string       `json:"name"`
	Status judge.Status `json:"status"`
}

type incidentIn struct {
	Page       string                  `json:"page"`
	Title      string                  `json:"title"`
	Stage      string                  `json:"stage"`
	Body       string                  `json:"body"`
	Components map[string]judge.Status `json:"components"`
	Notify     bool                    `json:"notify"`
}

// updateIn leaves out stage and components to keep the incident's current
// ones, as the update form starts from them.
type updateIn struct {
	Stage      string                  `json:"stage"`
	Body       string                  `json:"body"`
	Components map[string]judge.Status `json:"components"`
	Notify     bool                    `json:"notify"`
}

type editIn struct {
	Body string `json:"body"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func apiError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{msg})
}

func (s *Server) apiFail(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("api request failed", "path", r.URL.Path, "err", err)
	apiError(w, http.StatusInternalServerError, "internal error")
}

// decodeJSON reads one JSON object and refuses unknown fields and anything
// after the object.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON object")
	}
	return nil
}

// apiMessages turns message keys into English text for API errors.
func apiMessages(keys []string) []string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = i18n.T("en", k)
	}
	return out
}

// apiStatuses maps component keys to the IDs the store uses. Unknown keys
// and statuses an admin cannot set are reported rather than dropped.
func apiStatuses(comps []store.ComponentRow, in map[string]judge.Status) (map[int64]judge.Status, []string) {
	ids := map[string]int64{}
	for _, c := range comps {
		ids[c.Key] = c.ID
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := map[int64]judge.Status{}
	var errs []string
	for _, k := range keys {
		id, ok := ids[k]
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("unknown component %q", k))
		case !slices.Contains(declarable, in[k]):
			errs = append(errs, fmt.Sprintf("component %q: unknown status %q", k, in[k]))
		default:
			out[id] = in[k]
		}
	}
	return out, errs
}

func (s *Server) apiIncidentFromPath(w http.ResponseWriter, r *http.Request) *store.Incident {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		apiError(w, http.StatusNotFound, "incident not found")
		return nil
	}
	in, err := s.store.Incident(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "incident not found")
		return nil
	}
	if err != nil {
		s.apiFail(w, r, err)
		return nil
	}
	return in
}

func (s *Server) apiUpdateFromPath(w http.ResponseWriter, r *http.Request) (*store.Incident, *store.IncidentUpdate) {
	in := s.apiIncidentFromPath(w, r)
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
	apiError(w, http.StatusNotFound, "update not found")
	return nil, nil
}

// writeIncident answers with the incident as it is now stored.
func (s *Server) writeIncident(w http.ResponseWriter, r *http.Request, status int, id int64) {
	in, err := s.store.Incident(r.Context(), id)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	if status == http.StatusCreated {
		w.Header().Set("Location", fmt.Sprintf("/api/v1/admin/incidents/%d", id))
	}
	writeJSON(w, status, s.adminIncidentJSON(*in))
}

// handleAPIPages lists every page, private ones included, with the
// component keys an incident can name and the status each shows now.
func (s *Server) handleAPIPages(w http.ResponseWriter, r *http.Request) {
	pages, err := s.store.Pages(r.Context())
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	locale := s.env.DefaultLocale
	out := struct {
		Pages []adminPage `json:"pages"`
	}{[]adminPage{}}
	for _, p := range pages {
		d, err := s.loadPage(r.Context(), p)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		ap := adminPage{
			Key: p.Key, Title: p.Title.Get(locale), Visibility: "public", Status: d.Overall,
			URL: s.pageURL(p.Key), Groups: []adminGroup{},
		}
		if s.private(p.Key) {
			ap.Visibility = "private"
		}
		for _, g := range d.Groups {
			ag := adminGroup{Key: g.Key, Name: g.Name.Get(locale), Components: []adminPageComponent{}}
			for _, c := range g.Components {
				ag.Components = append(ag.Components, adminPageComponent{Key: c.Key, Name: c.Name.Get(locale), Status: c.Status})
			}
			ap.Groups = append(ap.Groups, ag)
		}
		out.Pages = append(out.Pages, ap)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIIncidents(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.AllIncidents(r.Context(), time.Now().AddDate(0, 0, -adminRecentDays))
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	page := r.URL.Query().Get("page")
	out := struct {
		Incidents []adminIncident `json:"incidents"`
	}{[]adminIncident{}}
	for _, in := range list {
		if page == "" || in.PageKey == page {
			out.Incidents = append(out.Incidents, s.adminIncidentJSON(in))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAPIIncident(w http.ResponseWriter, r *http.Request) {
	if in := s.apiIncidentFromPath(w, r); in != nil {
		writeJSON(w, http.StatusOK, s.adminIncidentJSON(*in))
	}
}

func (s *Server) handleAPIIncidentCreate(w http.ResponseWriter, r *http.Request) {
	var in incidentIn
	if err := decodeJSON(w, r, &in); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	p, err := s.pageByKey(r.Context(), in.Page)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	if p == nil {
		msg := fmt.Sprintf("unknown page %q", in.Page)
		if in.Page == "" {
			msg = "page is required when there is more than one page"
		}
		apiError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	comps, err := s.store.PageComponents(r.Context(), p.ID)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	statuses, errs := apiStatuses(comps, in.Components)
	title, body, stage := strings.TrimSpace(in.Title), normBody(in.Body), in.Stage
	if stage == "" {
		stage = store.StageInvestigating
	}
	errs = append(errs, apiMessages(incidentErrors(stage, title, body, true, false))...)
	if len(errs) > 0 {
		apiError(w, http.StatusUnprocessableEntity, strings.Join(errs, "; "))
		return
	}
	id, updateID, err := s.store.CreateIncident(r.Context(), p.ID, title, store.IncidentInput{
		Stage: stage, Body: body, Author: apiAuthor(r), At: time.Now(), Components: statuses,
	})
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident created", "incident", id, "page", p.Key, "stage", stage, "by", apiAuthor(r))
	if in.Notify {
		s.announce(r.Context(), id, updateID)
	}
	s.writeIncident(w, r, http.StatusCreated, id)
}

func (s *Server) handleAPIIncidentUpdate(w http.ResponseWriter, r *http.Request) {
	var in updateIn
	if err := decodeJSON(w, r, &in); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	inc := s.apiIncidentFromPath(w, r)
	if inc == nil {
		return
	}
	body, stage := normBody(in.Body), in.Stage
	statuses := map[int64]judge.Status{}
	var errs []string
	switch {
	case !inc.Open():
		// A resolved incident takes notes, as on the pages.
		if stage != "" && stage != store.StageResolved {
			errs = append(errs, "the incident is resolved; new updates are notes and keep the resolved stage")
		}
		if in.Components != nil {
			errs = append(errs, "components cannot change on a resolved incident")
		}
		stage = store.StageResolved
	case in.Components == nil:
		for _, c := range inc.Components {
			statuses[c.ComponentID] = c.Status
		}
	default:
		comps, err := s.store.PageComponents(r.Context(), inc.PageID)
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		statuses, errs = apiStatuses(comps, in.Components)
	}
	if stage == "" {
		stage = inc.Stage
	}
	errs = append(errs, apiMessages(incidentErrors(stage, "", body, false, !inc.Open()))...)
	if len(errs) > 0 {
		apiError(w, http.StatusUnprocessableEntity, strings.Join(errs, "; "))
		return
	}
	updateID, err := s.store.UpdateIncident(r.Context(), inc.ID, store.IncidentInput{
		Stage: stage, Body: body, Author: apiAuthor(r), At: time.Now(), Components: statuses,
	})
	if errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "incident not found")
		return
	}
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Info("incident updated", "incident", inc.ID, "stage", stage, "by", apiAuthor(r))
	if in.Notify {
		s.announce(r.Context(), inc.ID, updateID)
	}
	s.writeIncident(w, r, http.StatusCreated, inc.ID)
}

// handleAPIUpdateEdit corrects an update's text. It is not announced again.
func (s *Server) handleAPIUpdateEdit(w http.ResponseWriter, r *http.Request) {
	var in editIn
	if err := decodeJSON(w, r, &in); err != nil {
		apiError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	inc, u := s.apiUpdateFromPath(w, r)
	if u == nil {
		return
	}
	body := normBody(in.Body)
	if key := checkBody(body); key != "" {
		apiError(w, http.StatusUnprocessableEntity, i18n.T("en", key))
		return
	}
	if body != u.Body {
		err := s.store.EditUpdate(r.Context(), inc.ID, u.ID, body, apiAuthor(r), time.Now())
		if errors.Is(err, store.ErrNotFound) {
			apiError(w, http.StatusNotFound, "update not found")
			return
		}
		if err != nil {
			s.apiFail(w, r, err)
			return
		}
		s.invalidate()
		s.log.Info("incident update edited", "incident", inc.ID, "update", u.ID, "by", apiAuthor(r))
	}
	s.writeIncident(w, r, http.StatusOK, inc.ID)
}

func (s *Server) handleAPIUpdateDelete(w http.ResponseWriter, r *http.Request) {
	inc, u := s.apiUpdateFromPath(w, r)
	if u == nil {
		return
	}
	switch err := s.store.DeleteUpdate(r.Context(), inc.ID, u.ID); {
	case errors.Is(err, store.ErrLatestUpdate):
		apiError(w, http.StatusConflict, "the newest update carries the incident's stage and cannot be deleted")
		return
	case errors.Is(err, store.ErrNotFound):
		apiError(w, http.StatusNotFound, "update not found")
		return
	case err != nil:
		s.apiFail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Warn("incident update deleted", "incident", inc.ID, "update", u.ID, "stage", u.Stage, "at", u.At, "by", apiAuthor(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAPIIncidentDelete(w http.ResponseWriter, r *http.Request) {
	inc := s.apiIncidentFromPath(w, r)
	if inc == nil {
		return
	}
	if err := s.store.DeleteIncident(r.Context(), inc.ID); errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "incident not found")
		return
	} else if err != nil {
		s.apiFail(w, r, err)
		return
	}
	s.invalidate()
	s.log.Warn("incident deleted", "incident", inc.ID, "title", inc.Title, "by", apiAuthor(r))
	w.WriteHeader(http.StatusNoContent)
}
