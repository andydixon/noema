package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Server) motivationRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/goals", require(auth.RoleObserver, s.goalsList))
	m.Handle("POST /api/v1/minds/{mind}/goals", require(auth.RoleOperator, s.goalsCreate))
	m.Handle("GET /api/v1/minds/{mind}/goals/{id}", require(auth.RoleObserver, s.goalGet))
	m.Handle("PATCH /api/v1/minds/{mind}/goals/{id}", require(auth.RoleOperator, s.goalPatch))
	m.Handle("GET /api/v1/minds/{mind}/predictions", require(auth.RoleObserver, s.predictionsList))
	m.Handle("POST /api/v1/minds/{mind}/predictions", require(auth.RoleOperator, s.predictionsCreate))
	m.Handle("GET /api/v1/minds/{mind}/predictions/calibration", require(auth.RoleObserver, s.predictionsCalibration))
	m.Handle("GET /api/v1/minds/{mind}/predictions/{id}", require(auth.RoleObserver, s.predictionGet))
	m.Handle("POST /api/v1/minds/{mind}/predictions/{id}/resolve", require(auth.RoleOperator, s.predictionResolve))
	m.Handle("GET /api/v1/minds/{mind}/state", require(auth.RoleObserver, s.stateGet))
	m.Handle("POST /api/v1/minds/{mind}/state/reset", require(auth.RoleOperator, s.stateReset))
	m.Handle("PUT /api/v1/minds/{mind}/state/drives/{name}", require(auth.RoleAdministrator, s.driveSet))
}

func goalErr(err error) error {
	if errors.Is(err, goals.ErrInvalid) || errors.Is(err, predictions.ErrInvalid) {
		return ValidationError(err.Error())
	}
	return err
}

func (s *Server) goalsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var statuses []goals.Status
	for _, st := range strings.Split(r.URL.Query().Get("status"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses = append(statuses, goals.Status(st))
		}
	}
	list, err := s.deps.Goals.List(r.Context(), m.ID, statuses, 0)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []goals.Goal{}
	}
	writeJSON(w, http.StatusOK, page[goals.Goal]{Items: list})
}

// CreateGoal stores a goal and emits goal_created; shared with the UI.
func (s *Server) CreateGoal(r *http.Request, n goals.New) (goals.Goal, error) {
	n.Owner = "operator"
	g, err := s.deps.Goals.Create(r.Context(), s.deps.DB, n)
	if err != nil {
		return g, goalErr(err)
	}
	s.deps.Events.Append(r.Context(), events.New{MindID: n.MindID, Type: events.GoalCreated, Source: "operator", Summary: "operator " + actor(r) + " set goal: " + g.Title, Salience: 0.5 + 0.4*g.Priority, Confidence: 1, //nolint:errcheck
		Evidence: []events.EvidenceRef{{Kind: "goal", ID: string(g.ID)}}, Metadata: map[string]any{"goal_id": string(g.ID), "kind": g.Kind, "priority": g.Priority, "actor": actor(r)}, Payload: g})
	s.deps.Audit.Log(r.Context(), "goal.create", "goal", g.ID.String(), map[string]any{"title": g.Title})
	return g, nil
}

func (s *Server) goalsCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Kind        goals.Kind `json:"kind"`
		Priority    float64    `json:"priority"`
		ParentID    ids.ID     `json:"parent_id"`
		Subject     string     `json:"subject"`
		DueAt       *time.Time `json:"due_at"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	g, err := s.CreateGoal(r, goals.New{MindID: m.ID, ParentID: in.ParentID, Title: in.Title, Description: in.Description, Kind: in.Kind, Priority: in.Priority, Subject: in.Subject, DueAt: in.DueAt})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) goalGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	g, err := s.deps.Goals.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// UpdateGoal applies an update and emits the matching event; shared with the UI.
func (s *Server) UpdateGoal(r *http.Request, mind, id ids.ID, u goals.Update) (goals.Goal, error) {
	before, err := s.deps.Goals.Get(r.Context(), mind, id)
	if err != nil {
		return goals.Goal{}, err
	}
	g, err := s.deps.Goals.Apply(r.Context(), s.deps.DB, mind, id, u)
	if err != nil {
		return g, goalErr(err)
	}
	typ, summary := events.GoalUpdated, "goal updated: "+g.Title
	switch {
	case g.Status == goals.StatusCompleted && before.Status != goals.StatusCompleted:
		typ, summary = events.GoalCompleted, "goal completed: "+g.Title
	case g.Status == goals.StatusAbandoned && before.Status != goals.StatusAbandoned:
		typ, summary = events.GoalAbandoned, "goal abandoned: "+g.Title
	}
	s.deps.Events.Append(r.Context(), events.New{MindID: mind, Type: typ, Source: "operator", Summary: summary, Salience: 0.4 + 0.3*g.Priority, Confidence: 1, //nolint:errcheck
		Evidence: []events.EvidenceRef{{Kind: "goal", ID: string(g.ID)}}, Metadata: map[string]any{"goal_id": string(g.ID), "status": g.Status, "progress": g.Progress, "actor": actor(r)},
		Payload: map[string]any{"before": map[string]any{"status": before.Status, "progress": before.Progress}, "after": g}})
	s.deps.Audit.Log(r.Context(), "goal.update", "goal", g.ID.String(), map[string]any{"status": g.Status, "progress": g.Progress})
	return g, nil
}

func (s *Server) goalPatch(w http.ResponseWriter, r *http.Request) {
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
		Status   *goals.Status `json:"status"`
		Progress *float64      `json:"progress"`
		Priority *float64      `json:"priority"`
		Blockers *[]string     `json:"blockers"`
		Title    *string       `json:"title"`
		Desc     *string       `json:"description"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	g, err := s.UpdateGoal(r, m.ID, id, goals.Update{Status: in.Status, Progress: in.Progress, Priority: in.Priority, Blockers: in.Blockers, Title: in.Title, Desc: in.Desc})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) predictionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var statuses []predictions.Status
	for _, st := range strings.Split(r.URL.Query().Get("status"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses = append(statuses, predictions.Status(st))
		}
	}
	list, err := s.deps.Predictions.List(r.Context(), m.ID, statuses, 0)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []predictions.Prediction{}
	}
	writeJSON(w, http.StatusOK, page[predictions.Prediction]{Items: list})
}

// CreatePrediction stores an operator prediction and emits prediction_created.
func (s *Server) CreatePrediction(r *http.Request, n predictions.New) (predictions.Prediction, error) {
	n.Source = "operator"
	p, err := s.deps.Predictions.Create(r.Context(), s.deps.DB, n)
	if err != nil {
		return p, goalErr(err)
	}
	evs, _ := s.deps.Events.Append(r.Context(), events.New{MindID: n.MindID, Type: events.PredictionCreated, Source: "operator", Summary: "operator " + actor(r) + " predicted (" + trunc(p.Statement, 100) + ")", Salience: 0.4, Confidence: p.Confidence,
		Evidence: []events.EvidenceRef{{Kind: "prediction", ID: string(p.ID)}}, Metadata: map[string]any{"prediction_id": string(p.ID), "window_end": p.WindowEnd, "actor": actor(r)}, Payload: p})
	if len(evs) > 0 {
		s.deps.DB.Exec(r.Context(), `UPDATE predictions SET origin_event=$2 WHERE id=$1`, p.ID, evs[0].ID) //nolint:errcheck
	}
	if s.deps.Metrics != nil {
		s.deps.Metrics.Predictions.Inc()
	}
	s.deps.Audit.Log(r.Context(), "prediction.create", "prediction", p.ID.String(), map[string]any{"statement": p.Statement})
	return p, nil
}

func (s *Server) predictionsCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Statement  string               `json:"statement"`
		Subject    string               `json:"subject"`
		Confidence float64              `json:"confidence"`
		WindowEnd  time.Time            `json:"window_end"`
		Criteria   predictions.Criteria `json:"criteria"`
		BeliefID   ids.ID               `json:"belief_id"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	p, err := s.CreatePrediction(r, predictions.New{MindID: m.ID, Statement: in.Statement, Subject: in.Subject, Confidence: in.Confidence, WindowEnd: in.WindowEnd, Criteria: in.Criteria, BeliefID: in.BeliefID})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) predictionGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.PathValue("id"))
	if err != nil {
		fail(w, r, ValidationError("bad id"))
		return
	}
	p, err := s.deps.Predictions.Get(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ResolvePrediction is the operator resolution path; shared with the UI.
func (s *Server) ResolvePrediction(r *http.Request, mind, id ids.ID, outcome predictions.Status, reason string) (predictions.Result, error) {
	p, err := s.deps.Predictions.Get(r.Context(), mind, id)
	if err != nil {
		return predictions.Result{}, err
	}
	if outcome != predictions.StatusConfirmed && outcome != predictions.StatusRefuted && outcome != predictions.StatusWithdrawn {
		return predictions.Result{}, ValidationError("outcome must be confirmed, refuted or withdrawn")
	}
	var res predictions.Result
	var emitted []events.Event
	err = txDo(r, s, func(q txQuerier) error {
		var err error
		res, emitted, err = s.deps.Resolver.ResolveIn(r.Context(), q, p, predictions.Outcome{Status: outcome, Reason: "operator " + actor(r) + ": " + reason}, "operator")
		return err
	})
	if err != nil {
		return res, goalErr(err)
	}
	s.deps.Events.Publish(emitted...)
	s.deps.Audit.Log(r.Context(), "prediction.resolve", "prediction", id.String(), map[string]any{"outcome": outcome, "reason": reason})
	return res, nil
}

func (s *Server) predictionResolve(w http.ResponseWriter, r *http.Request) {
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
		Outcome predictions.Status `json:"outcome"`
		Reason  string             `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	res, err := s.ResolvePrediction(r, m.ID, id, in.Outcome, in.Reason)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) predictionsCalibration(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	window, err := time.ParseDuration(r.URL.Query().Get("window"))
	if err != nil {
		window = 90 * 24 * time.Hour
	}
	c, err := s.deps.Predictions.Calibrate(r.Context(), m.ID, time.Now().Add(-window))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) stateGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	st, err := s.deps.Motivation.Load(r.Context(), s.deps.DB, m)
	if err != nil {
		fail(w, r, err)
		return
	}
	motivation.Decay(&st, motivation.Baselines(m.Personality), time.Since(st.Affect.UpdatedAt))
	writeJSON(w, http.StatusOK, map[string]any{"state": st, "baselines": motivation.Baselines(m.Personality), "effective_weights": motivation.Modulate(m.Config.AttentionWeights, st)})
}

// ResetState returns drives and affect to baseline; shared with the UI.
func (s *Server) ResetState(r *http.Request, mind ids.ID) (motivation.State, error) {
	m, err := s.deps.Minds.Get(r.Context(), mind)
	if err != nil {
		return motivation.State{}, err
	}
	st, err := s.deps.Motivation.Reset(r.Context(), s.deps.DB, m)
	if err != nil {
		return st, err
	}
	s.deps.Events.OperatorAction(r.Context(), mind, actor(r), "state.reset", "Operator "+actor(r)+" reset drives and affect to baseline", nil)
	s.deps.Audit.Log(r.Context(), "state.reset", "mind", mind.String(), nil)
	return st, nil
}

func (s *Server) stateReset(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	st, err := s.ResetState(r, m.ID)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) driveSet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	known := false
	for _, n := range motivation.DriveNames {
		if n == name {
			known = true
		}
	}
	if !known {
		fail(w, r, ValidationError("unknown drive; the set is fixed and excludes self-preservation by design"))
		return
	}
	var in struct {
		Value    *float64 `json:"value"`
		Min      *float64 `json:"min"`
		Max      *float64 `json:"max"`
		Baseline *float64 `json:"baseline"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	st, err := s.deps.Motivation.Load(r.Context(), s.deps.DB, m)
	if err != nil {
		fail(w, r, err)
		return
	}
	var d motivation.Drive
	for _, x := range st.Drives {
		if x.Name == name {
			d = x
		}
	}
	if in.Min != nil {
		d.Min = *in.Min
	}
	if in.Max != nil {
		d.Max = *in.Max
	}
	if in.Baseline != nil {
		d.Baseline = *in.Baseline
	}
	if in.Value != nil {
		d.Value = *in.Value
	}
	if err := s.deps.Motivation.SaveDrive(r.Context(), s.deps.DB, m.ID, d); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Events.OperatorAction(r.Context(), m.ID, actor(r), "drive.set", "Operator "+actor(r)+" adjusted drive "+name, map[string]any{"drive": d})
	s.deps.Audit.Log(r.Context(), "drive.set", "mind", m.ID.String(), map[string]any{"drive": name, "value": d.Value, "min": d.Min, "max": d.Max})
	writeJSON(w, http.StatusOK, d)
}
