package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/auth"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (u *UI) eventRoutes(m *http.ServeMux) {
	m.Handle("GET /minds/{mind}/thoughts", u.require(auth.RoleObserver, u.thoughts))
	m.Handle("POST /minds/{mind}/observe", u.require(auth.RoleOperator, u.observe))
	m.Handle("GET /events/{id}", u.require(auth.RoleObserver, u.event))
	m.Handle("GET /events/{id}/timeline", u.require(auth.RoleObserver, u.timeline))
}

var sinceWindows = map[string]string{"15m": "15 minutes", "1h": "1 hour", "24h": "24 hours", "168h": "7 days"}

func (u *UI) thoughts(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := api.ParseEventFilter(q)
	f.MindID = m.ID
	f.Limit = 100
	list, err := u.deps.Events.Query(r.Context(), f)
	if err != nil {
		u.errorPage(w, r, 500, "Could not load events.")
		return
	}
	var topSeq, nextBefore int64
	if len(list) > 0 {
		topSeq = list[0].Seq
		if len(list) == 100 {
			nextBefore = list[len(list)-1].Seq
		}
	}
	// The stream carries the same filter but never paging parameters.
	sq := url.Values{}
	for _, k := range []string{"category", "type", "source", "min_salience", "min_confidence", "q", "correlation"} {
		if v := q.Get(k); v != "" {
			sq.Set(k, v)
		}
	}
	streamQuery := ""
	if len(sq) > 0 {
		streamQuery = "?" + sq.Encode()
	}
	flat := map[string]string{}
	for k := range q {
		flat[k] = q.Get(k)
	}
	cats := []events.Category{events.CatPerception, events.CatCognition, events.CatMemory, events.CatBelief, events.CatPrediction, events.CatGoal, events.CatAffect, events.CatAction, events.CatLLM, events.CatOperator, events.CatSystem}
	u.render(w, r, "thoughts", m.DisplayName+" · Thoughts", map[string]any{"Mind": m, "Events": list, "Q": flat, "Types": events.AllTypes(), "Categories": cats,
		"Windows": sinceWindows, "StreamQuery": streamQuery, "RawQuery": sq.Encode(), "TopSeq": topSeq, "NextBefore": nextBefore})
}

func (u *UI) observe(w http.ResponseWriter, r *http.Request) {
	m, ok := u.mind(w, r)
	if !ok {
		return
	}
	text := r.PostFormValue("text")
	if len(text) == 0 || len(text) > 20000 {
		u.flash(w, r, "error", "Observation text required.", "/minds/"+m.Slug)
		return
	}
	summary := text
	if len(summary) > 160 {
		summary = summary[:157] + "..."
	}
	evs, err := u.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.Observation, Source: "operator", Summary: summary, Salience: 0.6, Confidence: 1,
		Metadata: map[string]any{"trust": events.TrustOperator, "actor": username(r)},
		Payload:  events.ObservationPayload{Text: text, ContentType: "text/plain", Source: "operator", Trust: events.TrustOperator, Method: "ui", Actor: username(r)}})
	if err != nil {
		u.flash(w, r, "error", "Could not record observation.", "/minds/"+m.Slug)
		return
	}
	u.deps.Audit.Log(r.Context(), "observation.inject", "mind", m.ID.String(), map[string]any{"event_id": evs[0].ID})
	u.flash(w, r, "ok", "Observation recorded.", "/minds/"+m.Slug+"/thoughts")
}

func (u *UI) event(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such event.")
		return
	}
	c, err := u.ctxF(r, id)
	if err != nil {
		u.errorPage(w, r, 404, "No such event.")
		return
	}
	m, err := u.deps.Minds.Get(r.Context(), c.Event.MindID)
	if err != nil {
		u.errorPage(w, r, 500, "Mind missing for event.")
		return
	}
	u.render(w, r, "event", "Event "+strconv.FormatInt(c.Event.Seq, 10), map[string]any{"Ctx": c, "Mind": m, "Payload": pretty(c.Event.Payload), "Metadata": prettyAny(c.Event.Metadata)})
}

func pretty(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func prettyAny(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// timeline renders the causal chain around an event grouped by cognitive
// cycle. Edges are the recorded links; cycle grouping is shown as such.
func (u *UI) timeline(w http.ResponseWriter, r *http.Request) {
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		u.errorPage(w, r, 404, "No such event.")
		return
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	if depth <= 0 {
		depth = 4
	}
	direction := r.URL.Query().Get("direction")
	c, err := u.deps.Events.Chain(r.Context(), id, direction, depth)
	if err != nil {
		u.errorPage(w, r, 404, "No such event.")
		return
	}
	origin, _ := u.deps.Events.Get(r.Context(), id)
	m, _ := u.deps.Minds.Get(r.Context(), origin.MindID)
	// Group nodes by cycle in sequence order; edges into a lookup for rendering.
	type group struct {
		Cycle ids.ID
		Nodes []evstore.ChainNode
	}
	var groups []*group
	byCycle := map[ids.ID]*group{}
	for _, n := range c.Nodes {
		g := byCycle[n.CycleID]
		if g == nil {
			g = &group{Cycle: n.CycleID}
			byCycle[n.CycleID] = g
			groups = append(groups, g)
		}
		g.Nodes = append(g.Nodes, n)
	}
	inbound := map[ids.ID][]evstore.ChainEdge{}
	names := map[ids.ID]string{}
	for _, n := range c.Nodes {
		names[n.Event.ID] = n.Event.Summary
	}
	for _, e := range c.Edges {
		inbound[e.To] = append(inbound[e.To], e)
	}
	u.render(w, r, "timeline", "Timeline · "+truncateStr(origin.Summary, 60), map[string]any{"Mind": m, "Origin": origin, "Chain": c, "Groups": groups, "Inbound": inbound, "Names": names, "Depth": depth, "Direction": c.Direction})
}
