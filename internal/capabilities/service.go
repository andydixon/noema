package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Approval is a pending or decided request.
type Approval struct {
	ID             ids.ID          `json:"id"`
	MindID         ids.ID          `json:"mind_id"`
	Capability     string          `json:"capability"`
	Target         string          `json:"target"`
	Params         map[string]any  `json:"params"`
	RequestHash    string          `json:"request_hash"`
	Reason         string          `json:"reason"`
	Risk           Risk            `json:"risk"`
	Evidence       []EvidenceRef   `json:"evidence"`
	Status         string          `json:"status"`
	DecisionBy     string          `json:"decision_by"`
	DecisionNote   string          `json:"decision_note"`
	DecidedAt      *time.Time      `json:"decided_at,omitempty"`
	SessionGrant   bool            `json:"session_grant"`
	ModifiedParams map[string]any  `json:"modified_params,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          string          `json:"error"`
	EventID        ids.ID          `json:"event_id,omitempty"`
	CorrelationID  ids.ID          `json:"correlation_id,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Execution is a recorded run.
type Execution struct {
	ID         ids.ID          `json:"id"`
	MindID     ids.ID          `json:"mind_id"`
	Capability string          `json:"capability"`
	Target     string          `json:"target"`
	Params     map[string]any  `json:"params"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error"`
	DurationMS int             `json:"duration_ms"`
	ApprovalID ids.ID          `json:"approval_id,omitempty"`
	EventID    ids.ID          `json:"event_id,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Grant is an approve-for-session exception.
type Grant struct {
	ID            ids.ID    `json:"id"`
	MindID        ids.ID    `json:"mind_id"`
	Capability    string    `json:"capability"`
	TargetPattern string    `json:"target_pattern"`
	GrantedBy     string    `json:"granted_by"`
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// Outcome is what Invoke returns.
type Outcome struct {
	Status     string   `json:"status"` // executed | pending | denied | failed
	Decision   Decision `json:"decision"`
	Result     *Result  `json:"result,omitempty"`
	Error      string   `json:"error,omitempty"`
	ApprovalID ids.ID   `json:"approval_id,omitempty"`
	EventID    ids.ID   `json:"event_id,omitempty"`
	Execution  ids.ID   `json:"execution_id,omitempty"`
}

// Service wires the registry, policies, approvals and executions together.
type Service struct {
	Registry    *Registry
	DB          *pgxpool.Pool
	Events      *evstore.Store
	Log         *slog.Logger
	Metrics     *observability.Metrics
	ApprovalTTL time.Duration
	SessionTTL  time.Duration
	Now         func() time.Time
	OnSelfModel func(ctx context.Context, mind ids.ID) // called when effective policies may have changed
	// MaxPendingRequests caps open capability requests per mind so operator
	// attention is not spent on a stream of prompts (default 5).
	MaxPendingRequests int
	// RequestTTL is how long an undecided capability request stays open (default 24h).
	RequestTTL time.Duration
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ---- policies ----

const policyCols = `id, mind_id, capability, target_pattern, action_kind, effect, hours_from, hours_to, reason, created_by, created_at, updated_at`

func scanPolicy(row pgx.Row) (Policy, error) {
	var p Policy
	var mind *string
	var effect string
	if err := row.Scan(&p.ID, &mind, &p.Capability, &p.TargetPattern, &p.ActionKind, &effect, &p.HoursFrom, &p.HoursTo, &p.Reason, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, storage.Translate(err)
	}
	p.Effect = Effect(effect)
	if mind != nil {
		p.MindID = ids.ID(*mind)
	}
	return p, nil
}

// Policies lists rules applying to a mind (global plus its own), or all when mind is Nil.
func (s *Service) Policies(ctx context.Context, mind ids.ID) ([]Policy, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+policyCols+` FROM policies WHERE mind_id IS NULL OR $1::uuid IS NULL OR mind_id=$1 ORDER BY mind_id NULLS FIRST, capability, target_pattern`, nullID(mind))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPolicy inserts a rule. Locked capabilities cannot be relaxed.
func (s *Service) SetPolicy(ctx context.Context, p Policy) (Policy, error) {
	if !p.Effect.Valid() {
		return Policy{}, fmt.Errorf("%w: effect must be allow, ask or deny", ErrInvalid)
	}
	if p.Capability == "" {
		p.Capability = "*"
	}
	if p.TargetPattern == "" {
		p.TargetPattern = "*"
	}
	if p.ActionKind == "" {
		p.ActionKind = "*"
	}
	if p.Capability != "*" {
		c, ok := s.Registry.Get(p.Capability)
		if !ok {
			return Policy{}, fmt.Errorf("%w: unknown capability %q", ErrUnknown, p.Capability)
		}
		if c.Locked && p.Effect != Deny {
			return Policy{}, fmt.Errorf("%w: %s is locked to DENY and cannot be relaxed", ErrInvalid, c.Name)
		}
	}
	if _, err := path.Match(p.TargetPattern, "x"); err != nil {
		return Policy{}, fmt.Errorf("%w: bad target pattern", ErrInvalid)
	}
	if (p.HoursFrom == nil) != (p.HoursTo == nil) {
		return Policy{}, fmt.Errorf("%w: both hours_from and hours_to are required", ErrInvalid)
	}
	p.ID, p.CreatedAt, p.UpdatedAt = ids.New(), s.now(), s.now()
	_, err := s.DB.Exec(ctx, `INSERT INTO policies (id, mind_id, capability, target_pattern, action_kind, effect, hours_from, hours_to, reason, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		p.ID, nullID(p.MindID), p.Capability, p.TargetPattern, p.ActionKind, string(p.Effect), p.HoursFrom, p.HoursTo, p.Reason, p.CreatedBy)
	if err != nil {
		return Policy{}, err
	}
	s.selfModelChanged(ctx, p.MindID)
	return p, nil
}

// DeletePolicy removes a rule.
func (s *Service) DeletePolicy(ctx context.Context, id ids.ID) error {
	var mind *string
	err := s.DB.QueryRow(ctx, `DELETE FROM policies WHERE id=$1 RETURNING mind_id`, id).Scan(&mind)
	if err != nil {
		return storage.Translate(err)
	}
	if mind != nil {
		s.selfModelChanged(ctx, ids.ID(*mind))
	} else {
		s.selfModelChanged(ctx, ids.Nil)
	}
	return nil
}

func (s *Service) selfModelChanged(ctx context.Context, mind ids.ID) {
	if s.OnSelfModel != nil {
		s.OnSelfModel(ctx, mind)
	}
}

// Decide evaluates policy and session grants for a request without executing.
func (s *Service) Decide(ctx context.Context, mind ids.ID, c Capability, target, action string) (Decision, error) {
	pols, err := s.Policies(ctx, mind)
	if err != nil {
		return Decision{}, err
	}
	d := Evaluate(c, pols, mind, target, action, s.now())
	if d.Effect == Ask && !c.Locked {
		if g, ok := s.activeGrant(ctx, mind, c.Name, target); ok {
			d.Effect, d.Reason = Allow, fmt.Sprintf("session grant by %s until %s", g.GrantedBy, g.ExpiresAt.Format(time.Kitchen))
		}
	}
	return d, nil
}

func (s *Service) activeGrant(ctx context.Context, mind ids.ID, capability, target string) (Grant, bool) {
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, capability, target_pattern, granted_by, expires_at, created_at FROM session_grants WHERE mind_id=$1 AND capability=$2 AND revoked_at IS NULL AND expires_at > $3`, mind, capability, s.now())
	if err != nil {
		return Grant{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.MindID, &g.Capability, &g.TargetPattern, &g.GrantedBy, &g.ExpiresAt, &g.CreatedAt); err != nil {
			return Grant{}, false
		}
		if ok, _ := path.Match(g.TargetPattern, target); ok || g.TargetPattern == "*" || strings.EqualFold(g.TargetPattern, target) {
			return g, true
		}
	}
	return Grant{}, false
}

// Inventory lists every capability with the effect a mind would get for a
// generic target now; it feeds the self-model.
type InventoryItem struct {
	Capability Capability `json:"capability"`
	Effect     Effect     `json:"effect"`
	Reason     string     `json:"reason"`
}

// Inventory computes effective effects for a mind.
func (s *Service) Inventory(ctx context.Context, mind ids.ID) ([]InventoryItem, error) {
	pols, err := s.Policies(ctx, mind)
	if err != nil {
		return nil, err
	}
	var out []InventoryItem
	for _, c := range s.Registry.List() {
		d := Evaluate(c, pols, mind, "*", "*", s.now())
		out = append(out, InventoryItem{Capability: c, Effect: d.Effect, Reason: d.Reason})
	}
	return out, nil
}

// ---- invocation ----

// Invoke is the single entry point for actions.
func (s *Service) Invoke(ctx context.Context, inv Invocation) (Outcome, error) {
	if why, forbidden := Forbidden(ctx); forbidden {
		return Outcome{Status: "denied", Decision: Decision{Effect: Deny, Reason: "forbidden context: " + why}}, ErrForbiddenContext
	}
	c, ok := s.Registry.Get(inv.Capability)
	if !ok {
		return Outcome{}, fmt.Errorf("%w: %s", ErrUnknown, inv.Capability)
	}
	params, err := c.ValidateParams(inv.Params)
	if err != nil {
		return Outcome{}, err
	}
	inv.Params = params
	if inv.Target, err = c.ResolveTarget(inv.Target, params); err != nil {
		return Outcome{}, err
	}
	action, _ := params["action"].(string)
	if action == "" {
		action = "*"
	}
	d, err := s.decideInvocation(ctx, inv, c, action)
	if err != nil {
		return Outcome{}, err
	}
	if s.Metrics != nil {
		s.Metrics.ToolRequests.WithLabelValues(c.Name).Inc()
	}
	hash := Hash(inv.MindID, c.Name, inv.Target, params)
	base := map[string]any{"capability": c.Name, "target": inv.Target, "params": params, "risk": c.Risk, "access": c.Access, "reason": inv.Reason, "hash": hash, "decision": d, "origin": inv.Origin}
	evs, err := s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.PolicyDecision, Source: "capabilities", Summary: fmt.Sprintf("policy %s for %s on %q: %s", strings.ToUpper(string(d.Effect)), c.Name, inv.Target, d.Reason),
		Salience: 0.3, Confidence: 1, CorrelationID: inv.Correlation, Evidence: toEvidence(inv.Evidence), Metadata: map[string]any{"capability": c.Name, "effect": d.Effect, "policy_id": string(d.PolicyID), "grant_id": string(d.GrantID), "origin": inv.Origin}, Payload: base})
	if err != nil {
		return Outcome{}, err
	}
	decisionEv := evs[0].ID
	switch d.Effect {
	case Deny:
		if s.Metrics != nil {
			s.Metrics.ToolDenied.WithLabelValues(c.Name).Inc()
		}
		ev, _ := s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.ToolDenied, Source: "capabilities", Summary: fmt.Sprintf("denied: %s on %q (%s)", c.Name, inv.Target, d.Reason),
			Salience: 0.5, Confidence: 1, CausationID: decisionEv, CorrelationID: inv.Correlation, Metadata: map[string]any{"capability": c.Name, "target": inv.Target}, Payload: base})
		return Outcome{Status: "denied", Decision: d, EventID: firstID(ev)}, nil
	case Ask:
		a, err := s.createApproval(ctx, inv, c, hash, decisionEv)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Status: "pending", Decision: d, ApprovalID: a.ID, EventID: a.EventID}, nil
	}
	if !d.GrantID.IsZero() && !s.consumeGrant(ctx, d.GrantID) {
		// Another invocation used the last operation, or the grant lapsed
		// between decision and execution: fall back to asking.
		d.Effect, d.Reason, d.GrantID = Ask, "scoped grant exhausted or expired before execution", ids.Nil
		a, err := s.createApproval(ctx, inv, c, hash, decisionEv)
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Status: "pending", Decision: d, ApprovalID: a.ID, EventID: a.EventID}, nil
	}
	return s.execute(ctx, inv, c, d, decisionEv, ids.Nil)
}

// decideInvocation is Decide for a concrete request: scoped grants may match
// on parameters and are consulted only when policy says ASK, and a request
// that requires approval can never be allowed without one.
func (s *Service) decideInvocation(ctx context.Context, inv Invocation, c Capability, action string) (Decision, error) {
	d, err := s.Decide(ctx, inv.MindID, c, inv.Target, action)
	if err != nil {
		return d, err
	}
	if d.Effect == Ask && !c.Locked && !inv.RequireApproval {
		if g, ok := s.matchingGrant(ctx, inv.MindID, c, inv.Target, inv.Params); ok {
			d.Effect, d.GrantID = Allow, g.ID
			d.Reason = fmt.Sprintf("scoped grant by %s until %s%s", g.GrantedBy, g.ExpiresAt.Format(time.RFC3339), remaining(g))
		}
	}
	if d.Effect == Allow && inv.RequireApproval {
		d.Effect, d.Tightened, d.GrantID = Ask, true, ids.Nil
		d.Reason = "approval required by a commitment (policy alone would allow: " + d.Reason + ")"
	}
	return d, nil
}

func (s *Service) createApproval(ctx context.Context, inv Invocation, c Capability, hash string, cause ids.ID) (Approval, error) {
	ttl := s.ApprovalTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	a := Approval{ID: ids.New(), MindID: inv.MindID, Capability: c.Name, Target: inv.Target, Params: inv.Params, RequestHash: hash, Reason: inv.Reason, Risk: c.Risk, Evidence: inv.Evidence, Status: "pending", CorrelationID: inv.Correlation, ExpiresAt: s.now().Add(ttl), CreatedAt: s.now()}
	if a.Evidence == nil {
		a.Evidence = []EvidenceRef{}
	}
	ev, err := s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.ToolRequested, Source: "capabilities", Summary: fmt.Sprintf("approval requested: %s on %q — %s", c.Name, inv.Target, firstNonEmpty(inv.Reason, "no reason given")),
		Salience: 0.6, Confidence: 1, CausationID: cause, CorrelationID: inv.Correlation, Evidence: toEvidence(inv.Evidence),
		Metadata: map[string]any{"capability": c.Name, "target": inv.Target, "approval_id": string(a.ID), "risk": c.Risk}, Payload: map[string]any{"approval_id": a.ID, "capability": c.Name, "target": inv.Target, "params": inv.Params, "risk": c.Risk, "reason": inv.Reason, "expires_at": a.ExpiresAt}})
	if err != nil {
		return Approval{}, err
	}
	a.EventID = ev[0].ID
	params, _ := json.Marshal(inv.Params)
	evid, _ := json.Marshal(a.Evidence)
	_, err = s.DB.Exec(ctx, `INSERT INTO approval_requests (id, mind_id, capability, target, params, request_hash, reason, risk, evidence, status, event_id, correlation_id, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10,$11,$12)`, a.ID, a.MindID, a.Capability, a.Target, params, a.RequestHash, a.Reason, string(a.Risk), evid, string(a.EventID), nullID(a.CorrelationID), a.ExpiresAt)
	return a, err
}

func (s *Service) execute(ctx context.Context, inv Invocation, c Capability, d Decision, cause ids.ID, approvalID ids.ID) (Outcome, error) {
	start := s.now()
	var res Result
	var execErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				execErr = fmt.Errorf("executor panicked: %v", r)
			}
		}()
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		ectx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		res, execErr = c.Executor(ectx, inv)
	}()
	dur := time.Since(start)
	exec := Execution{ID: ids.New(), MindID: inv.MindID, Capability: c.Name, Target: inv.Target, Params: inv.Params, DurationMS: int(dur.Milliseconds()), ApprovalID: approvalID, CreatedAt: s.now()}
	payload := map[string]any{"capability": c.Name, "target": inv.Target, "params": inv.Params, "approval_id": approvalID, "duration_ms": exec.DurationMS}
	var ev []events.Event
	if execErr != nil {
		exec.Error = execErr.Error()
		payload["error"] = exec.Error
		ev, _ = s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.ToolFailed, Source: "capabilities", Summary: fmt.Sprintf("%s on %q failed: %s", c.Name, inv.Target, truncate(exec.Error, 160)),
			Salience: 0.6, Confidence: 1, CausationID: cause, CorrelationID: inv.Correlation, Metadata: map[string]any{"capability": c.Name, "target": inv.Target, "execution_id": string(exec.ID)}, Payload: payload})
	} else {
		rj, _ := json.Marshal(res)
		exec.Result = rj
		payload["result"] = res
		ev, _ = s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.ToolExecuted, Source: "capabilities", Summary: fmt.Sprintf("%s on %q: %s", c.Name, inv.Target, truncate(res.Summary, 160)),
			Salience: 0.5, Confidence: 1, CausationID: cause, CorrelationID: inv.Correlation, Evidence: toEvidence(inv.Evidence), Metadata: map[string]any{"capability": c.Name, "target": inv.Target, "execution_id": string(exec.ID), "trust": c.OutputTrust}, Payload: payload})
		// Tool output re-enters cognition as an observation with tool trust.
		if strings.TrimSpace(res.Output) != "" {
			s.Events.Append(ctx, events.New{MindID: inv.MindID, Type: events.Observation, Source: "tool:" + c.Name, Summary: "tool output from " + c.Name + ": " + truncate(oneLine(res.Output), 140), Salience: 0.6, Confidence: 0.9, CausationID: firstID(ev), CorrelationID: inv.Correlation, //nolint:errcheck
				Metadata: map[string]any{"trust": c.OutputTrust, "capability": c.Name, "execution_id": string(exec.ID)},
				Payload:  events.ObservationPayload{Text: truncate(res.Output, 20000), ContentType: "text/plain", Source: "tool:" + c.Name, Trust: c.OutputTrust, Method: "capability", Reference: string(exec.ID), Channel: inv.Target}})
		}
	}
	exec.EventID = firstID(ev)
	params, _ := json.Marshal(inv.Params)
	_, err := s.DB.Exec(ctx, `INSERT INTO tool_executions (id, mind_id, capability, target, params, result, error, duration_ms, approval_id, event_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		exec.ID, exec.MindID, exec.Capability, exec.Target, params, nullRaw(exec.Result), exec.Error, exec.DurationMS, nullID(approvalID), nullID(exec.EventID))
	if err != nil {
		s.Log.Error("record execution failed", "err", err)
	}
	if execErr != nil {
		return Outcome{Status: "failed", Decision: d, Error: exec.Error, EventID: exec.EventID, Execution: exec.ID, ApprovalID: approvalID}, nil
	}
	return Outcome{Status: "executed", Decision: d, Result: &res, EventID: exec.EventID, Execution: exec.ID, ApprovalID: approvalID}, nil
}

// ---- approvals ----

const approvalCols = `id, mind_id, capability, target, params, request_hash, reason, risk, evidence, status, decision_by, decision_note, decided_at, session_grant, modified_params, result, error, event_id, correlation_id, expires_at, created_at`

func scanApproval(row pgx.Row) (Approval, error) {
	var a Approval
	var params, evid []byte
	var modified, result *json.RawMessage
	var risk string
	var ev, corr *string
	if err := row.Scan(&a.ID, &a.MindID, &a.Capability, &a.Target, &params, &a.RequestHash, &a.Reason, &risk, &evid, &a.Status, &a.DecisionBy, &a.DecisionNote, &a.DecidedAt, &a.SessionGrant, &modified, &result, &a.Error, &ev, &corr, &a.ExpiresAt, &a.CreatedAt); err != nil {
		return a, storage.Translate(err)
	}
	a.Risk = Risk(risk)
	json.Unmarshal(params, &a.Params) //nolint:errcheck
	json.Unmarshal(evid, &a.Evidence) //nolint:errcheck
	if a.Params == nil {
		a.Params = map[string]any{}
	}
	if a.Evidence == nil {
		a.Evidence = []EvidenceRef{}
	}
	if modified != nil {
		json.Unmarshal(*modified, &a.ModifiedParams) //nolint:errcheck
	}
	if result != nil {
		a.Result = *result
	}
	if ev != nil {
		a.EventID = ids.ID(*ev)
	}
	if corr != nil {
		a.CorrelationID = ids.ID(*corr)
	}
	return a, nil
}

// Approvals lists requests by status (empty = all), newest first; mind Nil = all minds.
func (s *Service) Approvals(ctx context.Context, mind ids.ID, status string, limit int) ([]Approval, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT `+approvalCols+` FROM approval_requests WHERE ($1::uuid IS NULL OR mind_id=$1) AND ($2='' OR status=$2) ORDER BY created_at DESC LIMIT $3`, nullID(mind), status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Approval
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetApproval fetches one request.
func (s *Service) GetApproval(ctx context.Context, id ids.ID) (Approval, error) {
	return scanApproval(s.DB.QueryRow(ctx, `SELECT `+approvalCols+` FROM approval_requests WHERE id=$1`, id))
}

// Approve executes the request now. Modified params are re-validated and
// re-hashed; forSession additionally grants matching requests until SessionTTL.
func (s *Service) Approve(ctx context.Context, id ids.ID, by, note string, forSession bool, modified map[string]any) (Outcome, error) {
	if !ValidDecider(by) {
		return Outcome{}, ErrReservedDecider
	}
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	if a.Status != "pending" {
		return Outcome{}, fmt.Errorf("%w: request is %s", ErrInvalid, a.Status)
	}
	if s.now().After(a.ExpiresAt) {
		s.DB.Exec(ctx, `UPDATE approval_requests SET status='expired' WHERE id=$1`, id) //nolint:errcheck
		return Outcome{}, fmt.Errorf("%w: request expired", ErrInvalid)
	}
	c, ok := s.Registry.Get(a.Capability)
	if !ok || c.Locked {
		return Outcome{}, fmt.Errorf("%w: capability unavailable or locked", ErrInvalid)
	}
	params := a.Params
	if modified != nil {
		if params, err = c.ValidateParams(modified); err != nil {
			return Outcome{}, err
		}
		if _, err := c.ResolveTarget(a.Target, params); err != nil {
			return Outcome{}, fmt.Errorf("%w: modified parameters change what the request acts on; deny it and let the mind ask again", ErrInvalid)
		}
	}
	hash := Hash(a.MindID, a.Capability, a.Target, params)
	details := map[string]any{"for_session": forSession, "modified": modified != nil, "hash": hash, "original_hash": a.RequestHash}
	if err := s.recordDecision(ctx, a.ID, "approved", by, note, details); err != nil {
		return Outcome{}, err
	}
	mod, _ := json.Marshal(modified)
	if _, err := s.DB.Exec(ctx, `UPDATE approval_requests SET status='approved', decision_by=$2, decision_note=$3, decided_at=$4, session_grant=$5, modified_params=$6 WHERE id=$1`, id, by, note, s.now(), forSession, nullRawIf(mod, modified != nil)); err != nil {
		return Outcome{}, err
	}
	if forSession {
		ttl := s.SessionTTL
		if ttl <= 0 {
			ttl = 8 * time.Hour
		}
		s.DB.Exec(ctx, `INSERT INTO session_grants (id, mind_id, capability, target_pattern, granted_by, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`, ids.New(), a.MindID, a.Capability, a.Target, by, s.now().Add(ttl)) //nolint:errcheck
	}
	ev, _ := s.Events.Append(ctx, events.New{MindID: a.MindID, Type: events.ToolAuthorised, Source: "operator", Summary: fmt.Sprintf("operator %s approved %s on %q%s", by, a.Capability, a.Target, map[bool]string{true: " for this session", false: ""}[forSession]),
		Salience: 0.5, Confidence: 1, CausationID: a.EventID, CorrelationID: a.CorrelationID, Metadata: map[string]any{"approval_id": string(a.ID), "actor": by, "capability": a.Capability, "for_session": forSession}, Payload: details})
	inv := Invocation{MindID: a.MindID, Capability: a.Capability, Target: a.Target, Params: params, Reason: a.Reason, Evidence: a.Evidence, Correlation: a.CorrelationID, ApprovalID: a.ID}
	out, err := s.execute(ctx, inv, c, Decision{Effect: Allow, Reason: "approved by " + by}, firstID(ev), a.ID)
	if err != nil {
		return out, err
	}
	status := "executed"
	if out.Status == "failed" {
		status = "failed"
	}
	rj, _ := json.Marshal(out.Result)
	s.DB.Exec(ctx, `UPDATE approval_requests SET status=$2, result=$3, error=$4 WHERE id=$1`, id, status, nullRawIf(rj, out.Result != nil), out.Error) //nolint:errcheck
	s.selfModelChanged(ctx, a.MindID)
	return out, nil
}

// Deny refuses a request with an explanation the mind will see.
func (s *Service) Deny(ctx context.Context, id ids.ID, by, note string) (Approval, error) {
	if !ValidDecider(by) {
		return Approval{}, ErrReservedDecider
	}
	a, err := s.GetApproval(ctx, id)
	if err != nil {
		return Approval{}, err
	}
	if a.Status != "pending" {
		return Approval{}, fmt.Errorf("%w: request is %s", ErrInvalid, a.Status)
	}
	if err := s.recordDecision(ctx, a.ID, "denied", by, note, nil); err != nil {
		return Approval{}, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE approval_requests SET status='denied', decision_by=$2, decision_note=$3, decided_at=$4 WHERE id=$1`, id, by, note, s.now()); err != nil {
		return Approval{}, err
	}
	if s.Metrics != nil {
		s.Metrics.ToolDenied.WithLabelValues(a.Capability).Inc()
	}
	s.Events.Append(ctx, events.New{MindID: a.MindID, Type: events.ToolDenied, Source: "operator", Summary: fmt.Sprintf("operator %s denied %s on %q: %s", by, a.Capability, a.Target, firstNonEmpty(note, "no reason given")), //nolint:errcheck
		Salience: 0.5, Confidence: 1, CausationID: a.EventID, CorrelationID: a.CorrelationID, Metadata: map[string]any{"approval_id": string(a.ID), "actor": by, "capability": a.Capability, "target": a.Target}, Payload: map[string]any{"note": note, "params": a.Params}})
	return s.GetApproval(ctx, id)
}

func (s *Service) recordDecision(ctx context.Context, request ids.ID, decision, by, note string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	raw, _ := json.Marshal(details)
	_, err := s.DB.Exec(ctx, `INSERT INTO approval_decisions (id, request_id, decision, decided_by, note, details) VALUES ($1,$2,$3,$4,$5,$6)`, ids.New(), request, decision, by, note, raw)
	return err
}

// ExpirePending marks overdue requests expired.
func (s *Service) ExpirePending(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE approval_requests SET status='expired' WHERE status='pending' AND expires_at < $1`, s.now())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Grants lists live session grants for a mind.
func (s *Service) Grants(ctx context.Context, mind ids.ID) ([]Grant, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, capability, target_pattern, granted_by, expires_at, created_at FROM session_grants WHERE ($1::uuid IS NULL OR mind_id=$1) AND revoked_at IS NULL AND expires_at > $2 ORDER BY created_at DESC`, nullID(mind), s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.ID, &g.MindID, &g.Capability, &g.TargetPattern, &g.GrantedBy, &g.ExpiresAt, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// RevokeGrant ends a session grant early.
func (s *Service) RevokeGrant(ctx context.Context, id ids.ID) error {
	tag, err := s.DB.Exec(ctx, `UPDATE session_grants SET revoked_at=$2 WHERE id=$1 AND revoked_at IS NULL`, id, s.now())
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// Executions lists recent runs.
func (s *Service) Executions(ctx context.Context, mind ids.ID, limit int) ([]Execution, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, capability, target, params, result, error, duration_ms, approval_id, event_id, created_at FROM tool_executions WHERE ($1::uuid IS NULL OR mind_id=$1) ORDER BY created_at DESC LIMIT $2`, nullID(mind), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		var params []byte
		var result *json.RawMessage
		var appr, ev *string
		if err := rows.Scan(&e.ID, &e.MindID, &e.Capability, &e.Target, &params, &result, &e.Error, &e.DurationMS, &appr, &ev, &e.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(params, &e.Params) //nolint:errcheck
		if result != nil {
			e.Result = *result
		}
		if appr != nil {
			e.ApprovalID = ids.ID(*appr)
		}
		if ev != nil {
			e.EventID = ids.ID(*ev)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- helpers ----

func toEvidence(in []EvidenceRef) []events.EvidenceRef {
	out := make([]events.EvidenceRef, 0, len(in))
	for _, e := range in {
		out = append(out, events.EvidenceRef{Kind: e.Kind, ID: e.ID, Note: e.Note})
	}
	return out
}

func firstID(evs []events.Event) ids.ID {
	if len(evs) > 0 {
		return evs[0].ID
	}
	return ids.Nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

func nullRaw(r json.RawMessage) *json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	return &r
}

func nullRawIf(r json.RawMessage, cond bool) *json.RawMessage {
	if !cond || len(r) == 0 || string(r) == "null" {
		return nil
	}
	return &r
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
