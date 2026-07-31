package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) beliefRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/beliefs", require(auth.RoleObserver, s.beliefsList))
	m.Handle("POST /api/v1/minds/{mind}/beliefs", require(auth.RoleOperator, s.beliefsCreate))
	m.Handle("GET /api/v1/minds/{mind}/beliefs/stats", require(auth.RoleObserver, s.beliefsStats))
	m.Handle("GET /api/v1/minds/{mind}/beliefs/{id}", require(auth.RoleObserver, s.beliefGet))
	m.Handle("POST /api/v1/minds/{mind}/beliefs/{id}/evidence", require(auth.RoleOperator, s.beliefEvidence))
	m.Handle("POST /api/v1/minds/{mind}/beliefs/{id}/alternatives", require(auth.RoleOperator, s.beliefAlternative))
	m.Handle("POST /api/v1/minds/{mind}/beliefs/{id}/retract", require(auth.RoleOperator, s.beliefRetract))
	m.Handle("GET /api/v1/minds/{mind}/corrections", require(auth.RoleObserver, s.correctionsList))
	m.Handle("POST /api/v1/minds/{mind}/corrections", require(auth.RoleOperator, s.correctionsCreate))
}

// Emitter returns a beliefs.Emitter that appends through the event store and
// publishes after the surrounding transaction (or immediately on the pool).
func (s *Server) Emitter() beliefs.Emitter {
	return func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := s.deps.Events.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		s.deps.Events.Publish(out...)
		return out[0], nil
	}
}

func beliefErr(err error) error {
	if errors.Is(err, beliefs.ErrInvalid) || errors.Is(err, beliefs.ErrBlocked) {
		return ValidationError(err.Error())
	}
	return err
}

// ParseBeliefFilter reads the shared query vocabulary.
func ParseBeliefFilter(q map[string][]string) beliefs.Filter {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	f := beliefs.Filter{Search: get("q"), Subject: get("subject")}
	for _, st := range strings.Split(get("status"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			f.Statuses = append(f.Statuses, beliefs.Status(st))
		}
	}
	f.MinConfidence, _ = strconv.ParseFloat(get("min_confidence"), 64)
	f.MaxConfidence, _ = strconv.ParseFloat(get("max_confidence"), 64)
	f.Limit, _ = strconv.Atoi(get("limit"))
	return f
}

func (s *Server) beliefsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Beliefs.List(r.Context(), m.ID, ParseBeliefFilter(r.URL.Query()))
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []beliefs.Belief{}
	}
	writeJSON(w, http.StatusOK, page[beliefs.Belief]{Items: list})
}

// beliefsCreate records an operator-asserted belief. The operator's statement
// is the evidence; confidence is still computed, not typed in.
func (s *Server) beliefsCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Proposition  string             `json:"proposition"`
		Subject      string             `json:"subject"`
		Epistemic    concepts.Epistemic `json:"epistemic"`
		Weight       float64            `json:"weight"` // how firmly the operator asserts it
		Note         string             `json:"note"`
		Alternatives []string           `json:"alternatives"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Epistemic == "" {
		in.Epistemic = concepts.Claim
	}
	if in.Weight <= 0 {
		in.Weight = 0.7
	}
	res, err := s.deps.Beliefs.Create(r.Context(), s.deps.DB, s.Emitter(), beliefs.New{MindID: m.ID, Proposition: in.Proposition, Subject: in.Subject, Epistemic: in.Epistemic, Source: "operator",
		Evidence:     []beliefs.EvidenceInput{{Direction: "for", Kind: "operator", Source: "operator:" + actor(r), Weight: in.Weight, Credibility: events.TrustOperator.Credibility(), Note: firstNonEmpty(in.Note, "asserted by operator "+actor(r)), Derivation: beliefs.DerivationOperator, Family: "operator:" + actor(r)}},
		Alternatives: in.Alternatives})
	if err != nil {
		fail(w, r, beliefErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "belief.create", "belief", res.Belief.ID.String(), map[string]any{"mind": m.Slug, "proposition": res.Belief.Proposition})
	writeJSON(w, http.StatusCreated, res)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// BeliefDetail is the full picture of one belief.
type BeliefDetail struct {
	Belief       beliefs.Belief        `json:"belief"`
	Breakdown    beliefs.Breakdown     `json:"breakdown"`
	Evidence     []beliefs.EvidenceRow `json:"evidence"`
	Versions     []beliefs.Version     `json:"versions"`
	Alternatives []beliefs.Alternative `json:"alternatives"`
	Links        []beliefs.Link        `json:"links"`
	Events       []events.Event        `json:"events"`
	Origin       *events.Event         `json:"origin,omitempty"`
}

// BeliefDetail assembles the detail; shared with the UI.
func (s *Server) BeliefDetail(r *http.Request, mind, id ids.ID) (BeliefDetail, error) {
	ctx := r.Context()
	b, err := s.deps.Beliefs.Get(ctx, s.deps.DB, mind, id)
	if err != nil {
		return BeliefDetail{}, err
	}
	d := BeliefDetail{Belief: b}
	d.Evidence, _ = s.deps.Beliefs.EvidenceFor(ctx, s.deps.DB, mind, id)
	var ev []beliefs.Evidence
	for _, e := range d.Evidence {
		ev = append(ev, e.Evidence)
	}
	d.Breakdown = beliefs.Compute(b.Prior, ev)
	d.Versions, _ = s.deps.Beliefs.Versions(ctx, mind, id)
	d.Alternatives, _ = s.deps.Beliefs.Alternatives(ctx, mind, id)
	d.Links, _ = s.deps.Beliefs.Links(ctx, mind, id)
	d.Events, _ = s.deps.Events.DerivedFrom(ctx, "belief", string(id), 100)
	if !b.OriginEvent.IsZero() {
		if e, err := s.deps.Events.Get(ctx, b.OriginEvent); err == nil {
			d.Origin = &e
		}
	}
	if d.Evidence == nil {
		d.Evidence = []beliefs.EvidenceRow{}
	}
	if d.Versions == nil {
		d.Versions = []beliefs.Version{}
	}
	if d.Alternatives == nil {
		d.Alternatives = []beliefs.Alternative{}
	}
	if d.Links == nil {
		d.Links = []beliefs.Link{}
	}
	if d.Events == nil {
		d.Events = []events.Event{}
	}
	return d, nil
}

func (s *Server) beliefGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	d, err := s.BeliefDetail(r, m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) beliefsStats(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	st, err := s.deps.Beliefs.Stats(r.Context(), m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) beliefEvidence(w http.ResponseWriter, r *http.Request) {
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
		Direction string  `json:"direction"`
		Kind      string  `json:"kind"`
		RefID     string  `json:"ref_id"`
		Weight    float64 `json:"weight"`
		Note      string  `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Kind == "" {
		in.Kind = "operator"
	}
	res, err := s.deps.Beliefs.AddEvidence(r.Context(), s.deps.DB, s.Emitter(), m.ID, id, beliefs.EvidenceInput{Direction: in.Direction, Kind: in.Kind, RefID: in.RefID, Source: "operator:" + actor(r), Weight: in.Weight, Credibility: events.TrustOperator.Credibility(), Note: in.Note, Derivation: beliefs.DerivationOperator, Family: "operator:" + actor(r)}, "operator evidence: "+in.Note, ids.Nil, ids.Nil)
	if err != nil {
		fail(w, r, beliefErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "belief.evidence", "belief", id.String(), map[string]any{"direction": in.Direction, "note": in.Note})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) beliefAlternative(w http.ResponseWriter, r *http.Request) {
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
		Proposition string `json:"proposition"`
		Note        string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	a, err := s.deps.Beliefs.AddAlternative(r.Context(), m.ID, id, in.Proposition, in.Note)
	if err != nil {
		fail(w, r, beliefErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "belief.alternative", "belief", id.String(), map[string]any{"proposition": in.Proposition})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) beliefRetract(w http.ResponseWriter, r *http.Request) {
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
		Reason     string `json:"reason"`
		DoNotInfer bool   `json:"do_not_infer"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			fail(w, r, err)
			return
		}
	}
	res, err := s.deps.Beliefs.Retract(r.Context(), s.Emitter(), m.ID, id, in.Reason, actor(r), in.DoNotInfer)
	if err != nil {
		fail(w, r, beliefErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "belief.retract", "belief", id.String(), map[string]any{"reason": in.Reason, "do_not_infer": in.DoNotInfer})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) correctionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Beliefs.Corrections(r.Context(), m.ID, r.URL.Query().Get("kind"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []beliefs.Correction{}
	}
	writeJSON(w, http.StatusOK, page[beliefs.Correction]{Items: list})
}

// correctionsCreate records "do not infer this again" and similar statements.
func (s *Server) correctionsCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind      string `json:"kind"`
		Statement string `json:"statement"`
		Reason    string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if in.Kind != "do_not_infer" || strings.TrimSpace(in.Statement) == "" {
		fail(w, r, ValidationError("kind must be do_not_infer with a statement (entity corrections arrive with the relationship graph)"))
		return
	}
	evs, err := s.deps.Events.Append(r.Context(), events.New{MindID: m.ID, Type: events.OperatorAction, Source: "operator", Summary: "Operator " + actor(r) + " forbade inference: " + in.Statement,
		Salience: 0.6, Confidence: 1, Metadata: map[string]any{"action": "correction.do_not_infer", "actor": actor(r)}, Payload: map[string]any{"statement": in.Statement, "reason": in.Reason}})
	if err != nil {
		fail(w, r, err)
		return
	}
	c, err := s.deps.Beliefs.AddCorrection(r.Context(), s.deps.DB, m.ID, beliefs.Correction{Kind: in.Kind, Statement: in.Statement, Reason: in.Reason, CreatedBy: actor(r), EventID: evs[0].ID})
	if err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "correction.create", "mind", m.ID.String(), map[string]any{"kind": in.Kind, "statement": in.Statement})
	writeJSON(w, http.StatusCreated, c)
}
