package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) memoryRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/memories", require(auth.RoleObserver, s.memoriesList))
	m.Handle("POST /api/v1/minds/{mind}/memories", require(auth.RoleOperator, s.memoriesCreate))
	m.Handle("GET /api/v1/minds/{mind}/memories/stats", require(auth.RoleObserver, s.memoriesStats))
	m.Handle("GET /api/v1/minds/{mind}/memories/{id}", require(auth.RoleObserver, s.memoryGet))
	m.Handle("POST /api/v1/minds/{mind}/memories/{id}/retract", require(auth.RoleOperator, s.memoryRetract))
	m.Handle("POST /api/v1/minds/{mind}/memories/{id}/relationships", require(auth.RoleOperator, s.memoryRelate))
}

func memErr(err error) error {
	if errors.Is(err, memory.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

// ParseMemoryFilter reads the shared memory query vocabulary.
func ParseMemoryFilter(q map[string][]string) memory.Filter {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	f := memory.Filter{Search: get("q"), Source: get("source")}
	for _, k := range strings.Split(get("kind"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			f.Kinds = append(f.Kinds, concepts.MemoryKind(k))
		}
	}
	for _, k := range strings.Split(get("epistemic"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			f.Epistemic = append(f.Epistemic, concepts.Epistemic(k))
		}
	}
	for _, k := range strings.Split(get("status"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			f.Statuses = append(f.Statuses, memory.Status(k))
		}
	}
	f.MinConfidence, _ = strconv.ParseFloat(get("min_confidence"), 64)
	f.MinImportance, _ = strconv.ParseFloat(get("min_importance"), 64)
	if t, err := time.Parse(time.RFC3339Nano, get("before")); err == nil {
		f.Before = t
	}
	f.Limit, _ = strconv.Atoi(get("limit"))
	return f
}

func (s *Server) memoriesList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	f := ParseMemoryFilter(r.URL.Query())
	if f.Search != "" && r.URL.Query().Get("mode") != "list" {
		hits, err := s.deps.Memory.Retrieve(r.Context(), m.ID, memory.Query{Text: f.Search, Kinds: f.Kinds, Limit: f.Limit, Semantic: r.URL.Query().Get("semantic") != "0", Strict: r.URL.Query().Get("strict") == "1"})
		if err != nil {
			fail(w, r, err)
			return
		}
		if hits == nil {
			hits = []memory.Retrieval{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": hits, "mode": "retrieval"})
		return
	}
	list, err := s.deps.Memory.List(r.Context(), m.ID, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []memory.Memory{}
	}
	out := page[memory.Memory]{Items: list}
	if n := len(list); n > 0 && (f.Limit == 0 && n == 100 || f.Limit > 0 && n == f.Limit) {
		out.NextCursor = list[n-1].OccurredAt.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) memoriesCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind       concepts.MemoryKind `json:"kind"`
		Epistemic  concepts.Epistemic  `json:"epistemic"`
		Content    string              `json:"content"`
		Confidence float64             `json:"confidence"`
		Importance float64             `json:"importance"`
		Detail     map[string]any      `json:"detail"`
		OccurredAt *time.Time          `json:"occurred_at"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Kind == "" {
		in.Kind = concepts.Semantic
	}
	if in.Epistemic == "" {
		in.Epistemic = concepts.Claim
	}
	if in.Confidence == 0 {
		in.Confidence = 0.7
	}
	if in.Importance == 0 {
		in.Importance = 0.5
	}
	n := memory.New{MindID: m.ID, Kind: in.Kind, Epistemic: in.Epistemic, Content: in.Content, Source: "operator", Trust: events.TrustOperator, Confidence: in.Confidence, Importance: in.Importance, Salience: 0.5, Detail: in.Detail}
	if in.OccurredAt != nil {
		n.OccurredAt = *in.OccurredAt
	}
	mem, err := s.deps.Memory.Create(r.Context(), s.deps.DB, n)
	if err != nil {
		fail(w, r, memErr(err))
		return
	}
	s.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.MemoryCreated, Source: "operator", Summary: "operator added " + string(mem.Kind) + " memory: " + trunc(mem.Content, 120), //nolint:errcheck
		Salience: 0.5, Confidence: mem.Confidence, Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(mem.ID)}},
		Metadata: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "actor": actor(r)}, Payload: map[string]any{"memory_id": mem.ID, "kind": mem.Kind, "epistemic": mem.Epistemic, "content": mem.Content}})
	s.deps.Audit.Log(r.Context(), "memory.create", "memory", mem.ID.String(), map[string]any{"mind": m.Slug, "kind": mem.Kind})
	writeJSON(w, http.StatusCreated, mem)
}

// MemoryDetail is a memory with its relationships and the events that cite it.
type MemoryDetail struct {
	Memory        memory.Memory    `json:"memory"`
	Relationships []memory.Related `json:"relationships"`
	Events        []events.Event   `json:"events"`
	Origin        *events.Event    `json:"origin,omitempty"`
}

// MemoryDetail assembles the detail view; shared with the UI.
func (s *Server) MemoryDetail(r *http.Request, m ids.ID, id ids.ID) (MemoryDetail, error) {
	mem, err := s.deps.Memory.Get(r.Context(), m, id)
	if err != nil {
		return MemoryDetail{}, err
	}
	d := MemoryDetail{Memory: mem}
	d.Relationships, _ = s.deps.Memory.Relationships(r.Context(), m, id)
	d.Events, _ = s.deps.Events.DerivedFrom(r.Context(), "memory", string(id), 100)
	if !mem.OriginEvent.IsZero() {
		if ev, err := s.deps.Events.Get(r.Context(), mem.OriginEvent); err == nil {
			d.Origin = &ev
		}
	}
	if d.Relationships == nil {
		d.Relationships = []memory.Related{}
	}
	if d.Events == nil {
		d.Events = []events.Event{}
	}
	return d, nil
}

func (s *Server) memoryGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	d, err := s.MemoryDetail(r, m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) memoriesStats(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	st, err := s.deps.Memory.Stats(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// memoryRetract marks a memory inaccurate. The row stays; an operator_action
// event records the correction (spec §40).
func (s *Server) memoryRetract(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	if err := s.deps.Memory.SetStatus(r.Context(), m.ID, id, memory.StatusRetracted); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "memory.retract", "Operator "+actor(r)+" marked a memory inaccurate: "+in.Reason, map[string]any{"memory_id": id, "reason": in.Reason})
	s.deps.Audit.Log(r.Context(), "memory.retract", "memory", id.String(), map[string]any{"reason": in.Reason})
	s.propagateMemoryRetraction(r, m.ID, id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) memoryRelate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	var in struct {
		To         ids.ID            `json:"to"`
		Relation   concepts.Relation `json:"relation"`
		Confidence float64           `json:"confidence"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Confidence == 0 {
		in.Confidence = 0.8
	}
	rel, err := s.deps.Memory.Relate(r.Context(), s.deps.DB, m.ID, id, in.To, in.Relation, in.Confidence, "operator")
	if err != nil {
		fail(w, r, memErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "memory.relate", "memory", id.String(), map[string]any{"to": in.To, "relation": in.Relation})
	writeJSON(w, http.StatusCreated, rel)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// propagateMemoryRetraction stops a retracted memory counting as belief
// evidence and re-evaluates the beliefs that cited it.
func (s *Server) propagateMemoryRetraction(r *http.Request, mind, memoryID ids.ID) {
	affected, err := s.deps.Beliefs.RetractEvidence(r.Context(), s.deps.DB, mind, "memory", string(memoryID))
	if err != nil {
		logger(r).Warn("retract evidence failed", "err", err)
		return
	}
	for _, bid := range affected {
		res, err := s.deps.Beliefs.Reevaluate(r.Context(), s.deps.DB, s.Emitter(), mind, bid, "supporting memory retracted by operator")
		if err != nil {
			logger(r).Warn("re-evaluate failed", "belief", bid, "err", err)
			continue
		}
		s.deps.Events.Publish(res.Events...)
	}
}
