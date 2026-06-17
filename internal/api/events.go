package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) eventRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/events", require(auth.RoleObserver, s.eventsList))
	m.Handle("GET /api/v1/minds/{mind}/events/stream", require(auth.RoleObserver, s.eventsStream))
	m.Handle("GET /api/v1/minds/{mind}/events/stats", require(auth.RoleObserver, s.eventsStats))
	m.Handle("POST /api/v1/minds/{mind}/observations", require(auth.RoleOperator, s.observationCreate))
	m.Handle("GET /api/v1/events/stream", require(auth.RoleObserver, s.eventsStream))
	m.Handle("GET /api/v1/events/{id}", require(auth.RoleObserver, s.eventGet))
	m.Handle("GET /api/v1/events/{id}/context", require(auth.RoleObserver, s.eventContext))
	m.Handle("GET /api/v1/events/types", require(auth.RoleObserver, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"types": events.AllTypes()})
	}))
}

// ParseEventFilter reads the shared query-string filter vocabulary used by
// the API, SSE stream and UI pages.
func ParseEventFilter(q url.Values) evstore.Filter {
	f := evstore.Filter{}
	for _, t := range q["type"] {
		for _, part := range strings.Split(t, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Types = append(f.Types, events.Type(part))
			}
		}
	}
	for _, c := range q["category"] {
		for _, part := range strings.Split(c, ",") {
			if part = strings.TrimSpace(part); part != "" {
				f.Categories = append(f.Categories, events.Category(part))
			}
		}
	}
	for _, src := range q["source"] {
		if src = strings.TrimSpace(src); src != "" {
			f.Sources = append(f.Sources, src)
		}
	}
	f.MinSalience, _ = strconv.ParseFloat(q.Get("min_salience"), 64)
	f.MinConfidence, _ = strconv.ParseFloat(q.Get("min_confidence"), 64)
	f.MaxConfidence, _ = strconv.ParseFloat(q.Get("max_confidence"), 64)
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Since = t
		} else if d, err := time.ParseDuration(v); err == nil {
			f.Since = time.Now().Add(-d)
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Until = t
		}
	}
	f.AfterSeq, _ = strconv.ParseInt(q.Get("after"), 10, 64)
	f.BeforeSeq, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	if v := q.Get("correlation"); v != "" {
		if id, err := ids.Parse(v); err == nil {
			f.CorrelationID = id
		}
	}
	f.Search = q.Get("q")
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Ascending = q.Get("order") == "asc"
	return f
}

func (s *Server) eventsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	f := ParseEventFilter(r.URL.Query())
	f.MindID = m.ID
	list, err := s.deps.Events.Query(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []events.Event{}
	}
	out := page[events.Event]{Items: list}
	if len(list) > 0 && (f.Limit == 0 && len(list) == 100 || f.Limit > 0 && len(list) == f.Limit) {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].Seq, 10)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) eventsStats(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var since time.Time
	if d, err := time.ParseDuration(r.URL.Query().Get("window")); err == nil {
		since = time.Now().Add(-d)
	}
	st, err := s.deps.Events.Stats(r.Context(), m.ID, since)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) eventGet(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	ev, err := s.deps.Events.Get(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// EventContext is the forensic view: the event, what caused it, what it
// caused, what it cites and what cites it.
type EventContext struct {
	Event       events.Event         `json:"event"`
	Cause       *events.Event        `json:"cause,omitempty"`
	Parents     []events.Event       `json:"parents"`
	Children    []events.Event       `json:"children"`
	Evidence    []events.Event       `json:"evidence_events"`
	Derived     []events.Event       `json:"derived"`
	Correlated  []events.Event       `json:"correlated"`
	EvidenceAll []events.EvidenceRef `json:"evidence"`
}

// Context assembles the forensic context for an event; shared with the UI.
func (s *Server) Context(r *http.Request, id ids.ID) (EventContext, error) {
	ctx := r.Context()
	ev, err := s.deps.Events.Get(ctx, id)
	if err != nil {
		return EventContext{}, err
	}
	out := EventContext{Event: ev, Parents: []events.Event{}, Children: []events.Event{}, Evidence: []events.Event{}, Derived: []events.Event{}, Correlated: []events.Event{}, EvidenceAll: ev.Evidence}
	if !ev.CausationID.IsZero() {
		if c, err := s.deps.Events.Get(ctx, ev.CausationID); err == nil {
			out.Cause = &c
		}
	}
	if len(ev.ParentIDs) > 0 {
		out.Parents, _ = s.deps.Events.GetMany(ctx, ev.ParentIDs)
	}
	out.Children, _ = s.deps.Events.Children(ctx, ev.ID, 50)
	var evIDs []ids.ID
	for _, ref := range ev.Evidence {
		if ref.Kind == "event" {
			if eid, err := ids.Parse(ref.ID); err == nil {
				evIDs = append(evIDs, eid)
			}
		}
	}
	if len(evIDs) > 0 {
		out.Evidence, _ = s.deps.Events.GetMany(ctx, evIDs)
	}
	out.Derived, _ = s.deps.Events.DerivedFrom(ctx, "event", string(ev.ID), 50)
	if !ev.CorrelationID.IsZero() {
		out.Correlated, _ = s.deps.Events.Query(ctx, evstore.Filter{MindID: ev.MindID, CorrelationID: ev.CorrelationID, Ascending: true, Limit: 200})
	}
	for _, l := range []*[]events.Event{&out.Parents, &out.Children, &out.Evidence, &out.Derived, &out.Correlated} {
		if *l == nil {
			*l = []events.Event{}
		}
	}
	return out, nil
}

func (s *Server) eventContext(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	out, err := s.Context(r, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// observationCreate lets an operator inject an observation directly. It is
// tagged with operator trust and used by demos, tests and the CLI.
func (s *Server) observationCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Text      string  `json:"text"`
		Source    string  `json:"source"`
		Reference string  `json:"reference"`
		Salience  float64 `json:"salience"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > 20000 {
		fail(w, r, ValidationError("text required (max 20000 chars)"))
		return
	}
	if in.Source == "" {
		in.Source = "operator"
	}
	if in.Salience == 0 {
		in.Salience = 0.5
	}
	summary := in.Text
	if len(summary) > 160 {
		summary = summary[:157] + "..."
	}
	evs, err := s.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.Observation, Source: in.Source, Summary: summary,
		Salience: in.Salience, Confidence: 1, Metadata: map[string]any{"trust": events.TrustOperator, "actor": actor(r)},
		Payload: events.ObservationPayload{Text: in.Text, ContentType: "text/plain", Source: in.Source, Trust: events.TrustOperator, Method: "api", Reference: in.Reference, Actor: actor(r)}})
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "observation.inject", "mind", m.ID.String(), map[string]any{"event_id": evs[0].ID, "source": in.Source})
	writeJSON(w, http.StatusCreated, evs[0])
}

// eventsStream is the Server-Sent Events feed. It subscribes before replaying
// history after Last-Event-ID so no event is missed, then relays live events
// that match the filter. Heartbeats keep proxies from closing idle streams.
func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	f := ParseEventFilter(r.URL.Query())
	if r.PathValue("mind") != "" {
		m, ok := s.mind(w, r)
		if !ok {
			return
		}
		f.MindID = m.ID
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, r, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	ctx := r.Context()
	ch, err := s.deps.Events.Bus().Subscribe(ctx, f.MindID, 256)
	if err != nil {
		var tooMany evstore.ErrTooManySubscribers
		if errors.As(err, &tooMany) {
			writeError(w, r, http.StatusServiceUnavailable, "busy", "too many live streams")
			return
		}
		fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	var lastSeq int64
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		lastSeq, _ = strconv.ParseInt(v, 10, 64)
	} else if f.AfterSeq > 0 {
		lastSeq = f.AfterSeq
	}
	send := func(ev events.Event) bool {
		if ev.Seq <= lastSeq {
			return true
		}
		lastSeq = ev.Seq
		data, _ := json.Marshal(ev)
		if _, err := fmt.Fprintf(w, "id: %d\nevent: cognitive_event\ndata: %s\n\n", ev.Seq, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if lastSeq > 0 {
		replay := f
		replay.AfterSeq, replay.Ascending, replay.Limit = lastSeq, true, 500
		if hist, err := s.deps.Events.Query(ctx, replay); err == nil {
			for _, ev := range hist {
				if !send(ev) {
					return
				}
			}
		}
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if !f.Matches(ev) {
				continue
			}
			if !send(ev) {
				return
			}
		}
	}
}
