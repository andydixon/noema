package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/auth"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// volitionRoutes: wants, conflicts, commitments, intentions, decisions,
// plans, forensic explanations, attribution and research measures. Reads
// are open to observers; operators may add or close commitments, abandon
// intentions, cancel plans and release blocked steps. Nothing here grants
// capability authority.
func (s *Server) volitionRoutes() {
	m := s.mux
	m.Handle("GET /api/v1/minds/{mind}/wants", require(auth.RoleObserver, s.wantsList))
	m.Handle("GET /api/v1/minds/{mind}/wants/{id}", require(auth.RoleObserver, s.wantGet))
	m.Handle("GET /api/v1/minds/{mind}/want-conflicts", require(auth.RoleObserver, s.wantConflictsList))
	m.Handle("GET /api/v1/minds/{mind}/commitments", require(auth.RoleObserver, s.commitmentsList))
	m.Handle("POST /api/v1/minds/{mind}/commitments", require(auth.RoleOperator, s.commitmentCreate))
	m.Handle("POST /api/v1/minds/{mind}/commitments/{id}/close", require(auth.RoleOperator, s.commitmentClose))
	m.Handle("GET /api/v1/minds/{mind}/intentions", require(auth.RoleObserver, s.intentionsList))
	m.Handle("GET /api/v1/minds/{mind}/intentions/{id}", require(auth.RoleObserver, s.intentionGet))
	m.Handle("POST /api/v1/minds/{mind}/intentions/{id}/abandon", require(auth.RoleOperator, s.intentionAbandon))
	m.Handle("GET /api/v1/minds/{mind}/decisions", require(auth.RoleObserver, s.decisionsList))
	m.Handle("GET /api/v1/minds/{mind}/plans", require(auth.RoleObserver, s.plansList))
	m.Handle("GET /api/v1/minds/{mind}/plans/{id}", require(auth.RoleObserver, s.planGet))
	m.Handle("POST /api/v1/minds/{mind}/plans/{id}/cancel", require(auth.RoleOperator, s.planCancel))
	m.Handle("POST /api/v1/minds/{mind}/plans/{id}/steps/{step}/retry", require(auth.RoleOperator, s.planStepRetry))
	m.Handle("GET /api/v1/minds/{mind}/why", require(auth.RoleObserver, s.why))
	m.Handle("GET /api/v1/minds/{mind}/events/{id}/attribution", require(auth.RoleObserver, s.attribution))
	m.Handle("GET /api/v1/minds/{mind}/agency/timeline", require(auth.RoleObserver, s.agencyTimeline))
	m.Handle("GET /api/v1/research/measures", require(auth.RoleObserver, s.researchMeasures))
	m.Handle("GET /api/v1/minds/{mind}/research/agency", require(auth.RoleObserver, s.researchAgency))
	m.Handle("GET /api/v1/minds/{mind}/research/calibration", require(auth.RoleObserver, s.researchCalibration))
	m.Handle("GET /api/v1/minds/{mind}/research/measurements", require(auth.RoleObserver, s.researchMeasurements))
}

func volErr(err error) error {
	if errors.Is(err, volition.ErrInvalid) || errors.Is(err, volition.ErrClosed) || errors.Is(err, volition.ErrNotRevocable) || errors.Is(err, planning.ErrInvalidPlan) {
		return ValidationError(err.Error())
	}
	return err
}

func pathID(r *http.Request, name string) (ids.ID, error) {
	id, err := ids.Parse(r.PathValue(name))
	if err != nil {
		return ids.Nil, ValidationError("bad " + name)
	}
	return id, nil
}

func csv(r *http.Request, key string) []string {
	var out []string
	for _, v := range strings.Split(r.URL.Query().Get(key), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sinceParam(r *http.Request, def time.Duration) time.Time {
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
		if d, err := time.ParseDuration(v); err == nil {
			return time.Now().Add(-d)
		}
	}
	return time.Now().Add(-def)
}

func (s *Server) wantsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var st []volition.WantStatus
	for _, v := range csv(r, "status") {
		st = append(st, volition.WantStatus(v))
	}
	list, err := s.deps.Volition.Wants(r.Context(), nil, m.ID, st, 500)
	if err != nil {
		fail(w, r, err)
		return
	}
	type row struct {
		volition.Want
		Effective float64 `json:"effective_strength"`
	}
	out := []row{}
	for _, x := range list {
		out = append(out, row{x, x.Effective(time.Now())})
	}
	writeJSON(w, http.StatusOK, page[row]{Items: out})
}

// WantDetail is a want with the decisions and conflicts that concern it.
type WantDetail struct {
	Want       volition.Want        `json:"want"`
	Effective  float64              `json:"effective_strength"`
	Decisions  []volition.Decision  `json:"decisions"`
	Conflicts  []volition.Conflict  `json:"conflicts"`
	Intentions []volition.Intention `json:"intentions"`
}

// WantDetail assembles a want's record; shared with the UI.
func (s *Server) WantDetail(r *http.Request, mind, id ids.ID) (WantDetail, error) {
	wt, err := s.deps.Volition.GetWant(r.Context(), nil, mind, id)
	if err != nil {
		return WantDetail{}, err
	}
	d := WantDetail{Want: wt, Effective: wt.Effective(time.Now()), Conflicts: []volition.Conflict{}, Intentions: []volition.Intention{}}
	d.Decisions, _ = s.deps.Volition.Decisions(r.Context(), mind, volition.DecisionFilter{WantID: id, Limit: 100})
	if cs, err := s.deps.Volition.Conflicts(r.Context(), mind, "", 500); err == nil {
		for _, c := range cs {
			if c.WantA == id || c.WantB == id {
				d.Conflicts = append(d.Conflicts, c)
			}
		}
	}
	if its, err := s.deps.Volition.Intentions(r.Context(), nil, mind, nil, 500); err == nil {
		for _, it := range its {
			for _, sw := range it.SourceWants {
				if sw == id {
					d.Intentions = append(d.Intentions, it)
				}
			}
		}
	}
	if d.Decisions == nil {
		d.Decisions = []volition.Decision{}
	}
	return d, nil
}

func (s *Server) wantGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	d, err := s.WantDetail(r, m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) wantConflictsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Volition.Conflicts(r.Context(), m.ID, r.URL.Query().Get("status"), 500)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []volition.Conflict{}
	}
	writeJSON(w, http.StatusOK, page[volition.Conflict]{Items: list})
}

func (s *Server) commitmentsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Volition.Commitments(r.Context(), nil, m.ID, csv(r, "status"), 500)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []volition.Commitment{}
	}
	writeJSON(w, http.StatusOK, page[volition.Commitment]{Items: list})
}

// CreateCommitment records an operator-given commitment; shared with the UI.
func (s *Server) CreateCommitment(r *http.Request, n volition.NewCommitment) (volition.Commitment, error) {
	n.CreatedBy, n.Origin = actor(r), "operator"
	rec := s.deps.Volition.Direct(s.deps.Events, "operator")
	c, err := s.deps.Volition.CreateCommitment(r.Context(), rec, n)
	if err != nil {
		return c, volErr(err)
	}
	s.deps.Audit.Log(r.Context(), "commitment.create", "commitment", c.ID.String(), map[string]any{"mind": c.MindID, "kind": c.Kind, "polarity": c.Polarity, "unless": c.Unless, "description": c.Description})
	return c, nil
}

func (s *Server) commitmentCreate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var in struct {
		Subject     string           `json:"subject"`
		Description string           `json:"description"`
		Kind        string           `json:"kind"`
		Polarity    string           `json:"polarity"`
		Matcher     volition.Matcher `json:"matcher"`
		Unless      string           `json:"unless"`
		Beneficiary string           `json:"beneficiary"`
		Strength    float64          `json:"strength"`
		Priority    float64          `json:"priority"`
		Conditions  string           `json:"conditions"`
		DueAt       *time.Time       `json:"due_at"`
		ExpiresAt   *time.Time       `json:"expires_at"`
		Revocable   *bool            `json:"revocable"`
		GoalID      ids.ID           `json:"goal_id"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	c, err := s.CreateCommitment(r, volition.NewCommitment{MindID: m.ID, Subject: in.Subject, Description: in.Description, Kind: volition.CommitmentKind(in.Kind), Polarity: in.Polarity, Matcher: in.Matcher, Unless: in.Unless,
		Beneficiary: in.Beneficiary, Strength: in.Strength, Priority: in.Priority, Conditions: in.Conditions, DueAt: in.DueAt, ExpiresAt: in.ExpiresAt, Revocable: in.Revocable, GoalID: in.GoalID})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// CloseCommitment ends a commitment as an operator; shared with the UI.
func (s *Server) CloseCommitment(r *http.Request, mind, id ids.ID, status, reason string) (volition.Commitment, error) {
	rec := s.deps.Volition.Direct(s.deps.Events, "operator")
	c, err := s.deps.Volition.CloseCommitment(r.Context(), rec, mind, id, status, firstNonEmpty(reason, "closed by operator "+actor(r)), true)
	if err != nil {
		return c, volErr(err)
	}
	s.deps.Audit.Log(r.Context(), "commitment.close", "commitment", id.String(), map[string]any{"mind": mind, "status": status, "reason": reason})
	return c, nil
}

func (s *Server) commitmentClose(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	c, err := s.CloseCommitment(r, m.ID, id, in.Status, in.Reason)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) intentionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	var st []volition.IntentionStatus
	for _, v := range csv(r, "status") {
		st = append(st, volition.IntentionStatus(v))
	}
	list, err := s.deps.Volition.Intentions(r.Context(), nil, m.ID, st, 500)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []volition.Intention{}
	}
	writeJSON(w, http.StatusOK, page[volition.Intention]{Items: list})
}

// IntentionDetail is an intention with its plans, subordinate intentions,
// decisions and source wants.
type IntentionDetail struct {
	Intention    volition.Intention   `json:"intention"`
	Wants        []volition.Want      `json:"wants"`
	Plans        []planning.Plan      `json:"plans"`
	Subordinates []volition.Intention `json:"subordinates"`
	Decisions    []volition.Decision  `json:"decisions"`
}

// IntentionDetail assembles an intention's record; shared with the UI.
func (s *Server) IntentionDetail(r *http.Request, mind, id ids.ID) (IntentionDetail, error) {
	ctx := r.Context()
	it, err := s.deps.Volition.GetIntention(ctx, nil, mind, id)
	if err != nil {
		return IntentionDetail{}, err
	}
	d := IntentionDetail{Intention: it, Wants: []volition.Want{}, Plans: []planning.Plan{}, Subordinates: []volition.Intention{}}
	for _, wid := range it.SourceWants {
		if wt, err := s.deps.Volition.GetWant(ctx, nil, mind, wid); err == nil {
			d.Wants = append(d.Wants, wt)
		}
	}
	if ps, err := s.deps.Plans.ForIntention(ctx, nil, id); err == nil {
		for _, p := range ps {
			if full, err := s.deps.Plans.Get(ctx, nil, p.ID); err == nil {
				d.Plans = append(d.Plans, full)
			}
		}
	}
	if all, err := s.deps.Volition.Intentions(ctx, nil, mind, nil, 500); err == nil {
		for _, x := range all {
			if x.ParentID == id {
				d.Subordinates = append(d.Subordinates, x)
			}
		}
	}
	d.Decisions, _ = s.deps.Volition.Decisions(ctx, mind, volition.DecisionFilter{IntentionID: id, Limit: 200})
	if d.Decisions == nil {
		d.Decisions = []volition.Decision{}
	}
	return d, nil
}

func (s *Server) intentionGet(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	d, err := s.IntentionDetail(r, m.ID, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// AbandonIntention is an operator correction: the intention is abandoned,
// its open plan cancelled and pending requests withdrawn. Shared with the UI.
func (s *Server) AbandonIntention(r *http.Request, mind, id ids.ID, reason string) (volition.Intention, error) {
	ctx := r.Context()
	reason = firstNonEmpty(reason, "no reason given")
	it, err := s.deps.Volition.GetIntention(ctx, nil, mind, id)
	if err != nil {
		return it, err
	}
	if !it.PlanID.IsZero() {
		if err := s.deps.Executor.Cancel(ctx, it.PlanID, "operator "+actor(r)+" abandoned the intention: "+reason); err != nil {
			return it, err
		}
	}
	rec := s.deps.Volition.Direct(s.deps.Events, "operator")
	out, err := s.deps.Volition.Apply(ctx, rec, mind, id, volition.Reconsideration{Outcome: volition.Abandon, Reason: "operator " + actor(r) + ": " + reason}, volition.Trigger{Kind: volition.TriggerOperator, Detail: reason})
	if err != nil {
		return out, volErr(err)
	}
	s.deps.Audit.Log(ctx, "intention.abandon", "intention", id.String(), map[string]any{"mind": mind, "reason": reason})
	return out, nil
}

func (s *Server) intentionAbandon(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		decode(r, &in) //nolint:errcheck
	}
	out, err := s.AbandonIntention(r, m.ID, id, in.Reason)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) decisionsList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := volition.DecisionFilter{Kind: q.Get("kind"), Limit: 300}
	f.WantID, _ = ids.Parse(q.Get("want"))
	f.IntentionID, _ = ids.Parse(q.Get("intention"))
	f.CommitmentID, _ = ids.Parse(q.Get("commitment"))
	list, err := s.deps.Volition.Decisions(r.Context(), m.ID, f)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []volition.Decision{}
	}
	writeJSON(w, http.StatusOK, page[volition.Decision]{Items: list})
}

func (s *Server) plansList(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Plans.List(r.Context(), m.ID, csv(r, "status"), 200)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []planning.Plan{}
	}
	writeJSON(w, http.StatusOK, page[planning.Plan]{Items: list})
}

func (s *Server) planByPath(w http.ResponseWriter, r *http.Request) (planning.Plan, bool) {
	m, ok := s.mind(w, r)
	if !ok {
		return planning.Plan{}, false
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return planning.Plan{}, false
	}
	p, err := s.deps.Plans.Get(r.Context(), nil, id)
	if err != nil || p.MindID != m.ID {
		fail(w, r, storage.ErrNotFound)
		return planning.Plan{}, false
	}
	return p, true
}

func (s *Server) planGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planByPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) planCancel(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planByPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if r.ContentLength != 0 {
		decode(r, &in) //nolint:errcheck
	}
	if err := s.deps.Executor.Cancel(r.Context(), p.ID, "cancelled by operator "+actor(r)+": "+firstNonEmpty(in.Reason, "no reason given")); err != nil {
		fail(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), "plan.cancel", "plan", p.ID.String(), map[string]any{"mind": p.MindID, "reason": in.Reason})
	p, _ = s.deps.Plans.Get(r.Context(), nil, p.ID)
	writeJSON(w, http.StatusOK, p)
}

// RetryStep releases a blocked plan step as an operator; shared with the UI.
func (s *Server) RetryStep(r *http.Request, p planning.Plan, stepKey string) error {
	for _, st := range p.Steps {
		if st.Key == stepKey || string(st.ID) == stepKey {
			if err := s.deps.Executor.RetryStep(r.Context(), st.ID, actor(r)); err != nil {
				return volErr(err)
			}
			s.deps.Audit.Log(r.Context(), "plan.step.retry", "plan_step", st.ID.String(), map[string]any{"plan": p.ID, "key": st.Key})
			return nil
		}
	}
	return storage.ErrNotFound
}

func (s *Server) planStepRetry(w http.ResponseWriter, r *http.Request) {
	p, ok := s.planByPath(w, r)
	if !ok {
		return
	}
	if err := s.RetryStep(r, p, r.PathValue("step")); err != nil {
		fail(w, r, err)
		return
	}
	p, _ = s.deps.Plans.Get(r.Context(), nil, p.ID)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) why(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := ids.Parse(r.URL.Query().Get("id"))
	if err != nil {
		fail(w, r, ValidationError("id is required"))
		return
	}
	x, err := s.deps.Agency.Explain(r.Context(), m.ID, agency.Ref{Kind: firstNonEmpty(r.URL.Query().Get("kind"), "event"), ID: id})
	if err != nil {
		fail(w, r, ValidationError(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"explanation": x, "points": x.Points()})
}

func (s *Server) attribution(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	a, err := s.deps.Agency.Attribute(r.Context(), m.ID, id)
	if err != nil {
		fail(w, r, ValidationError(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// AgencyTimelineTypes are the event types of the agency timeline: wanted,
// intended, committed, planned, requested, approved, acted, observed,
// reconsidered.
var AgencyTimelineTypes = []events.Type{events.WantCreated, events.WantSuppressed, events.WantConflictDetected, events.IntentionFormed, events.IntentionBlocked, events.IntentionSuspended,
	events.IntentionResumed, events.IntentionReconsidered, events.IntentionAbandoned, events.IntentionFulfilled, events.IntentionSuperseded, events.CommitmentCreated, events.CommitmentDue,
	events.CommitmentConflict, events.CommitmentFulfilled, events.CommitmentBroken, events.CommitmentRevoked, events.PlanCreated, events.PlanReplanned, events.PlanStepStarted, events.PlanStepCompleted,
	events.PlanStepFailed, events.PlanCompleted, events.PlanAbandoned, events.ToolRequested, events.ToolAuthorised, events.ToolDenied, events.ToolExecuted, events.ToolFailed,
	events.PredictionCreated, events.PredictionResolved, events.PredictionError}

// AgencyTimeline lists agency events, newest first; shared with the UI.
func (s *Server) AgencyTimeline(r *http.Request, mind ids.ID, limit int, correlation ids.ID) ([]events.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return s.deps.Events.Query(r.Context(), evstore.Filter{MindID: mind, Types: AgencyTimelineTypes, CorrelationID: correlation, Limit: limit})
}

func (s *Server) agencyTimeline(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	corr, _ := ids.Parse(r.URL.Query().Get("intention"))
	evs, err := s.AgencyTimeline(r, m.ID, limit, corr)
	if err != nil {
		fail(w, r, err)
		return
	}
	if evs == nil {
		evs = []events.Event{}
	}
	writeJSON(w, http.StatusOK, page[events.Event]{Items: evs})
}

func (s *Server) researchMeasures(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"disclaimer": research.Disclaimer, "items": research.Measures})
}

func (s *Server) researchAgency(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	out, err := s.deps.Research.Agency(r.Context(), m.ID, sinceParam(r, 30*24*time.Hour))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disclaimer": research.Disclaimer, "metrics": out})
}

func (s *Server) researchCalibration(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	out, err := s.deps.Research.MetacognitiveCalibration(r.Context(), m.ID, sinceParam(r, 90*24*time.Hour))
	if err != nil {
		fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"disclaimer": research.Disclaimer, "calibration": out})
}

func (s *Server) researchMeasurements(w http.ResponseWriter, r *http.Request) {
	m, ok := s.mind(w, r)
	if !ok {
		return
	}
	list, err := s.deps.Research.List(r.Context(), m.ID, r.URL.Query().Get("measure"), sinceParam(r, 30*24*time.Hour), 1000)
	if err != nil {
		fail(w, r, err)
		return
	}
	if list == nil {
		list = []research.Measurement{}
	}
	writeJSON(w, http.StatusOK, page[research.Measurement]{Items: list})
}
