package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Store persists plans and steps.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// ErrInvalidPlan marks a draft that failed validation.
var ErrInvalidPlan = errors.New("planning: plan failed validation")

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	v := string(id)
	return &v
}

func setID(dst *ids.ID, src *string) {
	if src != nil {
		*dst = ids.ID(*src)
	}
}

func js(v any, empty string) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return []byte(empty)
	}
	return b
}

func emit(ctx context.Context, r volition.Recorder, mind ids.ID, typ events.Type, summary string, salience float64, evidence []events.EvidenceRef, meta map[string]any, payload any) (ids.ID, error) {
	if r.Emit == nil {
		return ids.Nil, nil
	}
	ev, err := r.Emit(ctx, r.Q, events.New{MindID: mind, Type: typ, Source: firstNonEmpty(r.Source, "planning"), Summary: summary, Salience: salience, Confidence: 1, CausationID: r.Cause, CorrelationID: r.Correlation, Evidence: evidence, Metadata: meta, Payload: payload})
	if err != nil {
		return ids.Nil, err
	}
	return ev.ID, nil
}

// Create stores a draft as a plan for an intention. A valid draft becomes
// "validated"; an invalid one stays "draft" with its errors and never runs.
// When previous is set the new plan is its next revision and the previous
// plan is marked superseded, kept as history.
func (s *Store) Create(ctx context.Context, r volition.Recorder, it volition.Intention, d Draft, v Validation, previous *Plan) (Plan, error) {
	now := time.Now()
	p := Plan{ID: ids.New(), MindID: it.MindID, IntentionID: it.ID, Revision: 1, Objective: trunc(d.Objective, 500), Origin: d.Origin, Strategy: d.Strategy, Status: PlanDraft, Assumptions: d.Assumptions,
		CompletionCriteria: d.CompletionCriteria, AbortConditions: d.AbortConditions, Alternatives: d.Alternatives, Risk: trunc(d.Risk, 500), Budget: d.Budget, Validation: v, RequireApproval: d.RequireApproval || it.RequireApproval,
		MaxReplans: 3, LLMCall: d.LLMCall, CreatedAt: now, UpdatedAt: now}
	if p.Origin == "" {
		p.Origin = "template"
	}
	if previous != nil {
		p.Revision, p.Supersedes, p.MaxReplans = previous.Revision+1, previous.ID, previous.MaxReplans
	}
	if v.Valid && p.Origin != "dream" {
		p.Status = PlanValidated
	} else if !v.Valid {
		p.StatusReason = "validation failed"
	} else {
		p.StatusReason = "a dream proposal waits for an awake intention to adopt it"
	}
	for _, x := range []*[]Assumption{&p.Assumptions} {
		if *x == nil {
			*x = []Assumption{}
		}
	}
	if p.AbortConditions == nil {
		p.AbortConditions = []string{}
	}
	if p.Alternatives == nil {
		p.Alternatives = []Alternative{}
	}
	if len(d.Ignored) > 0 {
		p.Validation.Warnings = append(p.Validation.Warnings, "ignored fields of the model proposal: "+joinComma(d.Ignored))
	}
	if d.Estimate != nil {
		p.Estimate = *d.Estimate
	} else {
		p.Estimate = EstimateDraft(d, nil)
	}
	if _, err := r.Q.Exec(ctx, `INSERT INTO plans (id, mind_id, intention_id, revision, supersedes, objective, origin, strategy, status, assumptions, completion_criteria, abort_conditions, alternatives, risk, estimate, actual, budget, validation, require_approval, max_replans, llm_call, status_reason, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'{}',$16,$17,$18,$19,$20,$21,$22,$22)`,
		p.ID, p.MindID, p.IntentionID, p.Revision, nullID(p.Supersedes), p.Objective, p.Origin, p.Strategy, p.Status, js(p.Assumptions, "[]"), p.CompletionCriteria, js(p.AbortConditions, "[]"), js(p.Alternatives, "[]"), p.Risk,
		js(p.Estimate, "{}"), js(p.Budget, "{}"), js(p.Validation, "{}"), p.RequireApproval, p.MaxReplans, nullID(p.LLMCall), p.StatusReason, now); err != nil {
		return Plan{}, storage.Translate(err)
	}
	for i, st := range d.Steps {
		step := Step{ID: ids.New(), PlanID: p.ID, MindID: p.MindID, Key: st.Key, Position: i, Title: trunc(st.Title, 200), Kind: st.Kind, DependsOn: st.DependsOn, Capability: st.Capability, Target: st.Target, Params: st.Params,
			RequireApproval: st.RequireApproval, Expectation: st.Expectation, Precondition: st.Precondition, Spec: st, Status: StepPending, MaxAttempts: st.MaxAttempts, CreatedAt: now, UpdatedAt: now}
		if step.DependsOn == nil {
			step.DependsOn = []string{}
		}
		if step.MaxAttempts <= 0 {
			step.MaxAttempts = 1
		}
		if step.Params == nil {
			step.Params = map[string]any{}
		}
		spec := st
		spec.Params, spec.Expectation, spec.Precondition = nil, nil, nil
		var exp, pre []byte
		if st.Expectation != nil {
			exp = js(st.Expectation, "null")
		}
		if st.Precondition != nil {
			pre = js(st.Precondition, "null")
		}
		if _, err := r.Q.Exec(ctx, `INSERT INTO plan_steps (id, plan_id, mind_id, key, position, title, kind, depends_on, capability, target, params, require_approval, expectation, precondition, spec, status, max_attempts, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,'pending',$16,$17,$17)`,
			step.ID, p.ID, p.MindID, step.Key, step.Position, step.Title, step.Kind, step.DependsOn, step.Capability, step.Target, js(step.Params, "{}"), step.RequireApproval, exp, pre, js(spec, "{}"), step.MaxAttempts, now); err != nil {
			return Plan{}, storage.Translate(err)
		}
		p.Steps = append(p.Steps, step)
	}
	evidence := []events.EvidenceRef{{Kind: "plan", ID: string(p.ID)}, {Kind: "intention", ID: string(it.ID)}}
	if previous != nil {
		evidence = append(evidence, events.EvidenceRef{Kind: "plan", ID: string(previous.ID), Note: "previous revision"})
		if _, err := r.Q.Exec(ctx, `UPDATE plans SET status='superseded', superseded_by=$2, closed_at=coalesce(closed_at,$3), updated_at=$3 WHERE id=$1`, previous.ID, p.ID, now); err != nil {
			return Plan{}, err
		}
		if _, err := emit(ctx, r, p.MindID, events.PlanReplanned, fmt.Sprintf("replanned (revision %d, %s): %s — previous plan kept as history", p.Revision, p.Strategy, p.Objective), 0.5, evidence,
			map[string]any{"plan_id": string(p.ID), "previous_plan_id": string(previous.ID), "intention_id": string(it.ID), "revision": p.Revision, "strategy": p.Strategy}, map[string]any{"alternatives": p.Alternatives}); err != nil {
			return Plan{}, err
		}
	}
	if _, err := emit(ctx, r, p.MindID, events.PlanCreated, fmt.Sprintf("plan (%s, %d steps, %s): %s", p.Strategy, len(p.Steps), p.Origin, p.Objective), 0.4, evidence,
		map[string]any{"plan_id": string(p.ID), "intention_id": string(it.ID), "revision": p.Revision, "origin": p.Origin, "strategy": p.Strategy, "steps": len(p.Steps)}, map[string]any{"objective": p.Objective, "estimate": p.Estimate, "alternatives": p.Alternatives, "ignored": d.Ignored}); err != nil {
		return Plan{}, err
	}
	if p.Status == PlanValidated {
		if _, err := emit(ctx, r, p.MindID, events.PlanValidated, fmt.Sprintf("plan validated: %d steps, dependency order %v", len(p.Steps), v.Order), 0.3, evidence,
			map[string]any{"plan_id": string(p.ID), "warnings": len(v.Warnings)}, v); err != nil {
			return Plan{}, err
		}
	}
	return p, nil
}

const planCols = `id, mind_id, intention_id, revision, supersedes, superseded_by, objective, origin, strategy, status, assumptions, completion_criteria, abort_conditions, alternatives, risk, estimate, actual, budget, validation, require_approval, max_replans, llm_call, status_reason, closed_at, created_at, updated_at`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	var sup, supBy, call *string
	var assumptions, aborts, alts, est, act, budget, val []byte
	if err := row.Scan(&p.ID, &p.MindID, &p.IntentionID, &p.Revision, &sup, &supBy, &p.Objective, &p.Origin, &p.Strategy, &p.Status, &assumptions, &p.CompletionCriteria, &aborts, &alts, &p.Risk, &est, &act, &budget, &val,
		&p.RequireApproval, &p.MaxReplans, &call, &p.StatusReason, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, storage.Translate(err)
	}
	setID(&p.Supersedes, sup)
	setID(&p.SupersededBy, supBy)
	setID(&p.LLMCall, call)
	json.Unmarshal(assumptions, &p.Assumptions) //nolint:errcheck
	json.Unmarshal(aborts, &p.AbortConditions)  //nolint:errcheck
	json.Unmarshal(alts, &p.Alternatives)       //nolint:errcheck
	json.Unmarshal(est, &p.Estimate)            //nolint:errcheck
	json.Unmarshal(act, &p.Actual)              //nolint:errcheck
	json.Unmarshal(budget, &p.Budget)           //nolint:errcheck
	json.Unmarshal(val, &p.Validation)          //nolint:errcheck
	return p, nil
}

const stepCols = `id, plan_id, mind_id, key, position, title, kind, depends_on, capability, target, params, require_approval, expectation, precondition, spec, status, attempts, max_attempts, lease_until, next_run_at, approval_id, request_id, execution_id, prediction_id, intention_id, result, observation, error, status_reason, started_at, finished_at, created_at, updated_at`

func scanStep(row pgx.Row) (Step, error) {
	var st Step
	var params, exp, pre, spec, result, obs []byte
	var appr, req, exec, pred, intention *string
	if err := row.Scan(&st.ID, &st.PlanID, &st.MindID, &st.Key, &st.Position, &st.Title, &st.Kind, &st.DependsOn, &st.Capability, &st.Target, &params, &st.RequireApproval, &exp, &pre, &spec, &st.Status, &st.Attempts, &st.MaxAttempts,
		&st.LeaseUntil, &st.NextRunAt, &appr, &req, &exec, &pred, &intention, &result, &obs, &st.Error, &st.StatusReason, &st.StartedAt, &st.FinishedAt, &st.CreatedAt, &st.UpdatedAt); err != nil {
		return st, storage.Translate(err)
	}
	json.Unmarshal(params, &st.Params) //nolint:errcheck
	if len(exp) > 0 && string(exp) != "null" {
		st.Expectation = &Expectation{}
		json.Unmarshal(exp, st.Expectation) //nolint:errcheck
	}
	if len(pre) > 0 && string(pre) != "null" {
		st.Precondition = &Precondition{}
		json.Unmarshal(pre, st.Precondition) //nolint:errcheck
	}
	json.Unmarshal(spec, &st.Spec) //nolint:errcheck
	st.Spec.Params, st.Spec.Expectation, st.Spec.Precondition = st.Params, st.Expectation, st.Precondition
	if len(result) > 0 {
		json.Unmarshal(result, &st.Result) //nolint:errcheck
	}
	if len(obs) > 0 {
		json.Unmarshal(obs, &st.Observation) //nolint:errcheck
	}
	setID(&st.ApprovalID, appr)
	setID(&st.RequestID, req)
	setID(&st.ExecutionID, exec)
	setID(&st.PredictionID, pred)
	setID(&st.IntentionID, intention)
	return st, nil
}

// Get fetches a plan with its steps.
func (s *Store) Get(ctx context.Context, q storage.Querier, id ids.ID) (Plan, error) {
	if q == nil {
		q = s.db
	}
	p, err := scanPlan(q.QueryRow(ctx, `SELECT `+planCols+` FROM plans WHERE id=$1`, id))
	if err != nil {
		return p, err
	}
	p.Steps, err = s.Steps(ctx, q, id)
	return p, err
}

// Steps lists a plan's steps in position order.
func (s *Store) Steps(ctx context.Context, q storage.Querier, plan ids.ID) ([]Step, error) {
	if q == nil {
		q = s.db
	}
	rows, err := q.Query(ctx, `SELECT `+stepCols+` FROM plan_steps WHERE plan_id=$1 ORDER BY position`, plan)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Step
	for rows.Next() {
		st, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// GetStep fetches one step.
func (s *Store) GetStep(ctx context.Context, id ids.ID) (Step, error) {
	return scanStep(s.db.QueryRow(ctx, `SELECT `+stepCols+` FROM plan_steps WHERE id=$1`, id))
}

// ForIntention lists an intention's plans, oldest revision first.
func (s *Store) ForIntention(ctx context.Context, q storage.Querier, intention ids.ID) ([]Plan, error) {
	if q == nil {
		q = s.db
	}
	rows, err := q.Query(ctx, `SELECT `+planCols+` FROM plans WHERE intention_id=$1 ORDER BY revision`, intention)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// List lists a mind's plans by status (empty = all), newest first.
func (s *Store) List(ctx context.Context, mind ids.ID, statuses []string, limit int) ([]Plan, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if statuses == nil {
		statuses = []string{}
	}
	rows, err := s.db.Query(ctx, `SELECT `+planCols+` FROM plans WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status=ANY($2)) ORDER BY created_at DESC LIMIT $3`, mind, statuses, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActiveMinds lists minds with open plans.
func (s *Store) ActiveMinds(ctx context.Context) ([]ids.ID, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT p.mind_id FROM plans p JOIN minds m ON m.id=p.mind_id WHERE m.status='active' AND p.status IN ('validated','waiting','executing','blocked','awaiting_approval','replanning')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.ID
	for rows.Next() {
		var id ids.ID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// OpenPlans lists a mind's plans that may still make progress.
func (s *Store) OpenPlans(ctx context.Context, mind ids.ID) ([]Plan, error) {
	return s.List(ctx, mind, []string{PlanValidated, PlanWaiting, PlanExecuting, PlanBlocked, PlanAwaitingApproval, PlanReplanning}, 200)
}

// Claim moves a step from one status to another only if it is still in the
// expected status, so two executors cannot both act on it. It returns
// whether this caller won.
func (s *Store) Claim(ctx context.Context, st *Step, from, to string, lease time.Duration) (bool, error) {
	now := time.Now()
	var leaseUntil *time.Time
	if lease > 0 {
		l := now.Add(lease)
		leaseUntil = &l
	}
	attempts := 0
	if to == StepExecuting {
		attempts = 1
	}
	tag, err := s.db.Exec(ctx, `UPDATE plan_steps SET status=$3, attempts=attempts+$4, lease_until=$5, started_at=coalesce(started_at,$6), updated_at=$6 WHERE id=$1 AND status=$2`, st.ID, from, to, attempts, leaseUntil, now)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	st.Status, st.Attempts, st.LeaseUntil = to, st.Attempts+attempts, leaseUntil
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	return true, nil
}

// Save writes a step's mutable fields.
func (s *Store) Save(ctx context.Context, q storage.Querier, st Step) error {
	if q == nil {
		q = s.db
	}
	now := time.Now()
	var finished *time.Time
	if StepTerminal(st.Status) {
		finished = &now
	}
	_, err := q.Exec(ctx, `UPDATE plan_steps SET status=$2, lease_until=$3, next_run_at=$4, approval_id=$5, request_id=$6, execution_id=$7, prediction_id=$8, intention_id=$9, result=$10, observation=$11, error=$12, status_reason=$13,
		finished_at=coalesce($14, finished_at), updated_at=$15 WHERE id=$1`,
		st.ID, st.Status, st.LeaseUntil, st.NextRunAt, nullID(st.ApprovalID), nullID(st.RequestID), nullID(st.ExecutionID), nullID(st.PredictionID), nullID(st.IntentionID), js(st.Result, "null"), js(st.Observation, "null"),
		trunc(st.Error, 2000), trunc(st.StatusReason, 500), finished, now)
	return err
}

// SaveIf writes a step only if its stored status is still expected, so a
// caller holding a stale copy can never move a step backwards (for example
// resetting a step another executor is running to "ready"). It reports
// whether the write happened.
func (s *Store) SaveIf(ctx context.Context, st Step, expected string) (bool, error) {
	now := time.Now()
	var finished *time.Time
	if StepTerminal(st.Status) {
		finished = &now
	}
	tag, err := s.db.Exec(ctx, `UPDATE plan_steps SET status=$2, status_reason=$3, finished_at=coalesce($4, finished_at), updated_at=$5 WHERE id=$1 AND status=$6`,
		st.ID, st.Status, trunc(st.StatusReason, 500), finished, now, expected)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// SetStatus writes a plan's status and reason.
func (s *Store) SetStatus(ctx context.Context, q storage.Querier, id ids.ID, status, reason string) error {
	if q == nil {
		q = s.db
	}
	now := time.Now()
	var closed *time.Time
	if !PlanOpen(status) && status != PlanDraft {
		closed = &now
	}
	_, err := q.Exec(ctx, `UPDATE plans SET status=$2, status_reason=$3, closed_at=coalesce($4, closed_at), updated_at=$5 WHERE id=$1`, id, status, trunc(reason, 500), closed, now)
	return err
}

// BeginReplan moves an open plan into replanning only if no one else has,
// so two reconsiderations holding the same plan cannot both replan it. It
// reports whether this caller won.
func (s *Store) BeginReplan(ctx context.Context, id ids.ID, reason string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE plans SET status=$2, status_reason=$3, updated_at=now() WHERE id=$1 AND status = ANY($4)`,
		id, PlanReplanning, trunc(reason, 500), []string{PlanValidated, PlanWaiting, PlanExecuting, PlanBlocked, PlanAwaitingApproval})
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// AddActual adds consumed resources to a plan and returns the new totals.
func (s *Store) AddActual(ctx context.Context, id ids.ID, delta Resources) (Resources, error) {
	var raw []byte
	err := storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT actual FROM plans WHERE id=$1 FOR UPDATE`, id).Scan(&raw); err != nil {
			return err
		}
		var cur Resources
		json.Unmarshal(raw, &cur) //nolint:errcheck
		cur.Invocations += delta.Invocations
		cur.Approvals += delta.Approvals
		cur.WallSeconds += delta.WallSeconds
		cur.Network += delta.Network
		cur.Money += delta.Money
		cur.Tokens += delta.Tokens
		raw = js(cur, "{}")
		_, err := tx.Exec(ctx, `UPDATE plans SET actual=$2, updated_at=now() WHERE id=$1`, id, raw)
		return err
	})
	var out Resources
	json.Unmarshal(raw, &out) //nolint:errcheck
	return out, err
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
