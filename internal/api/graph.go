package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) graphRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/graph", require(auth.RoleObserver, s.graphGet))
	m.Handle("GET /api/v1/minds/{mind}/entities", require(auth.RoleObserver, s.entitiesList))
	m.Handle("GET /api/v1/minds/{mind}/entities/{id}", require(auth.RoleObserver, s.entityGet))
	m.Handle("POST /api/v1/minds/{mind}/entities/{id}/merge", require(auth.RoleOperator, s.entityMerge))
	m.Handle("POST /api/v1/minds/{mind}/entities/{id}/distinct", require(auth.RoleOperator, s.entityDistinct))
	m.Handle("PATCH /api/v1/minds/{mind}/entities/{id}", require(auth.RoleOperator, s.entityPatch))
	m.Handle("POST /api/v1/minds/{mind}/entities/{id}/relationships", require(auth.RoleOperator, s.entityRelate))
}

func graphErr(err error) error {
	if errors.Is(err, graph.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) graphGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var focus ids.ID
	if v := q.Get("focus"); v != "" {
		focus, _ = ids.Parse(v)
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	sg, err := s.deps.Graph.Subgraph(r.Context(), m.ID, focus, graph.Kind(q.Get("kind")), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if sg.Nodes == nil {
		sg.Nodes = []graph.Entity{}
	}
	writeJSON(w, http.StatusOK, sg)
}

func (s *Server) entitiesList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.deps.Graph.Search(r.Context(), m.ID, r.URL.Query().Get("q"), graph.Kind(r.URL.Query().Get("kind")), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []graph.Entity{}
	}
	writeJSON(w, http.StatusOK, page[graph.Entity]{Items: list})
}

// EntityDetail is an entity with its edges and links; shared with the UI.
type EntityDetail struct {
	Entity graph.Entity `json:"entity"`
	Edges  []graph.Edge `json:"edges"`
	Links  []graph.Link `json:"links"`
}

// EntityDetail assembles the detail view.
func (s *Server) EntityDetail(r *http.Request, mind, id ids.ID) (EntityDetail, error) {
	e, err := s.deps.Graph.Get(r.Context(), mind, id)
	if err != nil {
		return EntityDetail{}, err
	}
	d := EntityDetail{Entity: e}
	d.Edges, _ = s.deps.Graph.Neighbourhood(r.Context(), mind, e.ID, 100)
	d.Links, _ = s.deps.Graph.Links(r.Context(), mind, e.ID, 100)
	if d.Edges == nil {
		d.Edges = []graph.Edge{}
	}
	if d.Links == nil {
		d.Links = []graph.Link{}
	}
	return d, nil
}

func (s *Server) entityGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	d, err := s.EntityDetail(r, m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// resolveEntity accepts an id or a name.
func (s *Server) resolveEntity(r *http.Request, mind ids.ID, ref string) (graph.Entity, error) {
	if id, err := ids.Parse(ref); err == nil {
		return s.deps.Graph.Get(r.Context(), mind, id)
	}
	list, err := s.deps.Graph.Search(r.Context(), mind, ref, "", 5)
	if err != nil {
		return graph.Entity{}, err
	}
	for _, e := range list {
		if e.Canonical == graph.Canonical(ref) {
			return e, nil
		}
	}
	if len(list) == 1 {
		return list[0], nil
	}
	return graph.Entity{}, ValidationError("no unique entity matches " + ref)
}

// MergeEntities is the "these two are the same" correction; shared with the UI.
func (s *Server) MergeEntities(r *http.Request, mind ids.ID, survivor graph.Entity, otherRef string) error {
	loser, err := s.resolveEntity(r, mind, otherRef)
	if err != nil {
		return err
	}
	if err := s.deps.Graph.Merge(r.Context(), mind, survivor.ID, loser.ID); err != nil {
		return graphErr(err)
	}
	evs, _ := s.deps.Events.Append(r.Context(), events.New{MindID: mind, Type: events.OperatorAction, Source: "operator", Summary: "Operator " + actor(r) + " merged entities: " + loser.Name + " is the same as " + survivor.Name,
		Salience: 0.4, Confidence: 1, Metadata: map[string]any{"action": "entities.same", "actor": actor(r)}, Payload: map[string]any{"survivor": survivor.ID, "loser": loser.ID}})
	if len(evs) > 0 {
		s.deps.Beliefs.AddCorrection(r.Context(), s.deps.DB, mind, beliefs.Correction{Kind: "entities_same", TargetKind: "entity", TargetID: survivor.ID.String(), Statement: loser.Name + " = " + survivor.Name, CreatedBy: actor(r), EventID: evs[0].ID}) //nolint:errcheck
	}
	s.deps.Audit.Log(r.Context(), "entity.merge", "entity", survivor.ID.String(), map[string]any{"loser": loser.ID})
	return nil
}

// DistinctEntities records "these are different"; shared with the UI.
func (s *Server) DistinctEntities(r *http.Request, mind ids.ID, e graph.Entity, otherRef string) error {
	evs, err := s.deps.Events.Append(r.Context(), events.New{MindID: mind, Type: events.OperatorAction, Source: "operator", Summary: "Operator " + actor(r) + " stated that " + e.Name + " and " + otherRef + " are different entities",
		Salience: 0.3, Confidence: 1, Metadata: map[string]any{"action": "entities.different", "actor": actor(r)}, Payload: map[string]any{"entity": e.ID, "other": otherRef}})
	if err != nil {
		return err
	}
	_, err = s.deps.Beliefs.AddCorrection(r.Context(), s.deps.DB, mind, beliefs.Correction{Kind: "entities_different", TargetKind: "entity", TargetID: e.ID.String(), Statement: e.Name + " ≠ " + otherRef, CreatedBy: actor(r), EventID: evs[0].ID})
	s.deps.Audit.Log(r.Context(), "entity.distinct", "entity", e.ID.String(), map[string]any{"other": otherRef})
	return err
}

func (s *Server) entityMerge(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	e, err := s.deps.Graph.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in struct {
		Other string `json:"other"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.MergeEntities(r, m.ID, e, in.Other); err != nil {
		fail(w, r, err)
		return
	}
	d, _ := s.EntityDetail(r, m.ID, e.ID)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) entityDistinct(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	e, err := s.deps.Graph.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in struct {
		Other string `json:"other"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.DistinctEntities(r, m.ID, e, in.Other); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) entityPatch(w http.ResponseWriter, r *http.Request) {
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
		Kind        graph.Kind `json:"kind"`
		Description string     `json:"description"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	if err := s.deps.Graph.Update(r.Context(), m.ID, id, in.Kind, in.Description); err != nil {
		fail(w, r, graphErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "entity.update", "entity", id.String(), map[string]any{"kind": in.Kind})
	d, _ := s.EntityDetail(r, m.ID, id)
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) entityRelate(w http.ResponseWriter, r *http.Request) {
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
		To         string            `json:"to"`
		Relation   concepts.Relation `json:"relation"`
		Confidence float64           `json:"confidence"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	to, err := s.resolveEntity(r, m.ID, in.To)
	if err != nil {
		fail(w, r, err)
		return
	}
	if in.Confidence <= 0 {
		in.Confidence = 0.8
	}
	rel, err := s.deps.Graph.Relate(r.Context(), s.deps.DB, m.ID, id, to.ID, in.Relation, in.Confidence, "operator:"+actor(r), ids.Nil)
	if err != nil {
		fail(w, r, graphErr(err))
		return
	}
	s.deps.Audit.Log(r.Context(), "entity.relate", "entity", id.String(), map[string]any{"to": to.ID, "relation": in.Relation})
	writeJSON(w, http.StatusCreated, rel)
}
