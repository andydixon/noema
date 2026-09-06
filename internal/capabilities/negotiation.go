package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/andydixon/noema/internal/security/actors"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// ScopedGrant is temporary authority an operator gave a mind: one
// capability, restricted by target, parameters and access, until an expiry
// and optionally for a number of operations. It is consulted only when policy
// says ASK; it never overrides DENY or a locked capability.
type ScopedGrant struct {
	ID               ids.ID            `json:"id"`
	MindID           ids.ID            `json:"mind_id"`
	Capability       string            `json:"capability"`
	TargetPattern    string            `json:"target_pattern"`
	ParamConstraints map[string]string `json:"param_constraints"`
	Access           Access            `json:"access"`
	MaxOperations    *int              `json:"max_operations,omitempty"`
	UsedOperations   int               `json:"used_operations"`
	ExpiresAt        time.Time         `json:"expires_at"`
	GrantedBy        string            `json:"granted_by"`
	RequestID        ids.ID            `json:"request_id,omitempty"`
	Note             string            `json:"note"`
	RevokedAt        *time.Time        `json:"revoked_at,omitempty"`
	RevokedBy        string            `json:"revoked_by,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
}

// Live reports whether the grant can still authorise an operation at now.
func (g ScopedGrant) Live(now time.Time) bool {
	return g.RevokedAt == nil && now.Before(g.ExpiresAt) && (g.MaxOperations == nil || g.UsedOperations < *g.MaxOperations)
}

// Covers reports whether the grant's scope includes a concrete request.
func (g ScopedGrant) Covers(c Capability, target string, params map[string]any) bool {
	if g.Capability != c.Name {
		return false
	}
	if g.Access == AccessRead && c.Access != AccessRead {
		return false
	}
	if g.TargetPattern != "*" {
		if ok, _ := path.Match(g.TargetPattern, target); !ok && !strings.EqualFold(g.TargetPattern, target) {
			return false
		}
	}
	for name, pattern := range g.ParamConstraints {
		v, ok := params[name]
		if !ok {
			return false
		}
		if ok, _ := path.Match(pattern, fmt.Sprint(v)); !ok {
			return false
		}
	}
	return true
}

func remaining(g ScopedGrant) string {
	if g.MaxOperations == nil {
		return ""
	}
	return fmt.Sprintf(", %d of %d operations left", *g.MaxOperations-g.UsedOperations, *g.MaxOperations)
}

const grantCols = `id, mind_id, capability, target_pattern, param_constraints, access, max_operations, used_operations, expires_at, granted_by, request_id, note, revoked_at, revoked_by, created_at`

func scanGrant(row pgx.Row) (ScopedGrant, error) {
	var g ScopedGrant
	var pc []byte
	var access string
	var req *string
	if err := row.Scan(&g.ID, &g.MindID, &g.Capability, &g.TargetPattern, &pc, &access, &g.MaxOperations, &g.UsedOperations, &g.ExpiresAt, &g.GrantedBy, &req, &g.Note, &g.RevokedAt, &g.RevokedBy, &g.CreatedAt); err != nil {
		return g, storage.Translate(err)
	}
	g.Access = Access(access)
	json.Unmarshal(pc, &g.ParamConstraints) //nolint:errcheck
	if g.ParamConstraints == nil {
		g.ParamConstraints = map[string]string{}
	}
	if req != nil {
		g.RequestID = ids.ID(*req)
	}
	return g, nil
}

// ScopedGrants lists grants for a mind (Nil = all); live only when liveOnly.
func (s *Service) ScopedGrants(ctx context.Context, mind ids.ID, liveOnly bool) ([]ScopedGrant, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+grantCols+` FROM capability_grants WHERE ($1::uuid IS NULL OR mind_id=$1)
		AND (NOT $2 OR (revoked_at IS NULL AND expires_at > $3 AND (max_operations IS NULL OR used_operations < max_operations))) ORDER BY created_at DESC LIMIT 500`, nullID(mind), liveOnly, s.now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScopedGrant
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Service) matchingGrant(ctx context.Context, mind ids.ID, c Capability, target string, params map[string]any) (ScopedGrant, bool) {
	gs, err := s.ScopedGrants(ctx, mind, true)
	if err != nil {
		return ScopedGrant{}, false
	}
	for _, g := range gs {
		if g.Capability == c.Name && g.Live(s.now()) && g.Covers(c, target, params) {
			return g, true
		}
	}
	return ScopedGrant{}, false
}

// consumeGrant uses one operation atomically; false means the grant could
// no longer authorise (exhausted, expired or revoked in the meantime).
func (s *Service) consumeGrant(ctx context.Context, id ids.ID) bool {
	tag, err := s.DB.Exec(ctx, `UPDATE capability_grants SET used_operations = used_operations + 1
		WHERE id=$1 AND revoked_at IS NULL AND expires_at > $2 AND (max_operations IS NULL OR used_operations < max_operations)`, id, s.now())
	return err == nil && tag.RowsAffected() == 1
}

// GrantInput is an operator's scoped grant, directly or in answer to a request.
type GrantInput struct {
	MindID           ids.ID
	Capability       string
	TargetPattern    string
	ParamConstraints map[string]string
	Access           Access
	Duration         time.Duration
	MaxOperations    *int
	By               string
	Note             string
	RequestID        ids.ID
}

// MaxGrantDuration bounds temporary authority.
const MaxGrantDuration = 7 * 24 * time.Hour

// ErrReservedDecider marks a decision attempted in the name of cognition.
var ErrReservedDecider = errors.New("capabilities: decisions must be made by a named operator, not by a mind or an internal process")

// ValidDecider reports whether by names a person rather than a component.
func ValidDecider(by string) bool { return actors.IsPerson(by) }

// Grant stores a scoped grant. Only operator routes call it.
func (s *Service) Grant(ctx context.Context, in GrantInput) (ScopedGrant, error) {
	if !ValidDecider(in.By) {
		return ScopedGrant{}, ErrReservedDecider
	}
	c, ok := s.Registry.Get(in.Capability)
	if !ok {
		return ScopedGrant{}, fmt.Errorf("%w: %s", ErrUnknown, in.Capability)
	}
	if c.Locked {
		return ScopedGrant{}, fmt.Errorf("%w: %s is locked and cannot be granted", ErrInvalid, c.Name)
	}
	if in.Access != AccessRead && in.Access != AccessWrite {
		in.Access = c.Access
	}
	if in.Access == AccessRead && c.Access != AccessRead {
		return ScopedGrant{}, fmt.Errorf("%w: %s changes external state; a read-only grant cannot cover it", ErrInvalid, c.Name)
	}
	if in.Duration <= 0 || in.Duration > MaxGrantDuration {
		return ScopedGrant{}, fmt.Errorf("%w: duration must be between 1 second and 7 days", ErrInvalid)
	}
	if in.MaxOperations != nil && (*in.MaxOperations <= 0 || *in.MaxOperations > 10000) {
		return ScopedGrant{}, fmt.Errorf("%w: max_operations must be 1–10000", ErrInvalid)
	}
	if in.TargetPattern == "" {
		in.TargetPattern = "*"
	}
	if _, err := path.Match(in.TargetPattern, "x"); err != nil {
		return ScopedGrant{}, fmt.Errorf("%w: bad target pattern", ErrInvalid)
	}
	if err := checkConstraints(c, in.ParamConstraints); err != nil {
		return ScopedGrant{}, err
	}
	if in.ParamConstraints == nil {
		in.ParamConstraints = map[string]string{}
	}
	g := ScopedGrant{ID: ids.New(), MindID: in.MindID, Capability: c.Name, TargetPattern: in.TargetPattern, ParamConstraints: in.ParamConstraints, Access: in.Access, MaxOperations: in.MaxOperations,
		ExpiresAt: s.now().Add(in.Duration), GrantedBy: in.By, RequestID: in.RequestID, Note: in.Note, CreatedAt: s.now()}
	pc, _ := json.Marshal(g.ParamConstraints)
	if _, err := s.DB.Exec(ctx, `INSERT INTO capability_grants (id, mind_id, capability, target_pattern, param_constraints, access, max_operations, expires_at, granted_by, request_id, note)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, g.ID, g.MindID, g.Capability, g.TargetPattern, pc, string(g.Access), g.MaxOperations, g.ExpiresAt, g.GrantedBy, nullID(g.RequestID), g.Note); err != nil {
		return ScopedGrant{}, err
	}
	s.Events.Append(ctx, events.New{MindID: g.MindID, Type: events.ToolAuthorised, Source: "operator", Summary: fmt.Sprintf("operator %s granted %s on %q (%s) until %s%s", g.GrantedBy, g.Capability, g.TargetPattern, g.Access, g.ExpiresAt.Format(time.RFC3339), remaining(g)), //nolint:errcheck
		Salience: 0.5, Confidence: 1, Metadata: map[string]any{"grant_id": string(g.ID), "request_id": string(g.RequestID), "capability": g.Capability, "actor": g.GrantedBy, "kind": "scoped_grant"}, Payload: g})
	s.selfModelChanged(ctx, g.MindID)
	return g, nil
}

func checkConstraints(c Capability, pc map[string]string) error {
	for name, pattern := range pc {
		if _, ok := c.Params[name]; !ok {
			return fmt.Errorf("%w: %s has no parameter %q", ErrInvalid, c.Name, name)
		}
		if _, err := path.Match(pattern, "x"); err != nil {
			return fmt.Errorf("%w: bad pattern for %q", ErrInvalid, name)
		}
	}
	return nil
}

// RevokeScopedGrant ends a grant early.
func (s *Service) RevokeScopedGrant(ctx context.Context, id ids.ID, by string) error {
	var mind ids.ID
	err := s.DB.QueryRow(ctx, `UPDATE capability_grants SET revoked_at=$2, revoked_by=$3 WHERE id=$1 AND revoked_at IS NULL RETURNING mind_id`, id, s.now(), by).Scan(&mind)
	if err != nil {
		return storage.Translate(err)
	}
	s.selfModelChanged(ctx, mind)
	return nil
}

// SweepGrants finds grants that stopped being live since the last sweep
// (expired, used up or revoked), marks them and refreshes each affected
// mind's self-model, so a mind never believes it still holds authority that
// lapsed. It returns the minds refreshed.
func (s *Service) SweepGrants(ctx context.Context) ([]ids.ID, error) {
	rows, err := s.DB.Query(ctx, `UPDATE capability_grants SET expiry_notified_at=$1
		WHERE expiry_notified_at IS NULL AND (expires_at <= $1 OR revoked_at IS NOT NULL OR (max_operations IS NOT NULL AND used_operations >= max_operations))
		RETURNING id, mind_id, capability, target_pattern`, s.now())
	if err != nil {
		return nil, err
	}
	type lapsed struct {
		id, mind            ids.ID
		capability, pattern string
	}
	var list []lapsed
	for rows.Next() {
		var l lapsed
		if err := rows.Scan(&l.id, &l.mind, &l.capability, &l.pattern); err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, l)
	}
	rows.Close()
	seen := map[ids.ID]bool{}
	var out []ids.ID
	for _, l := range list {
		s.Events.Append(ctx, events.New{MindID: l.mind, Type: events.SelfModelUpdated, Source: "capabilities", Summary: fmt.Sprintf("temporary grant for %s on %q is no longer in force", l.capability, l.pattern), //nolint:errcheck
			Salience: 0.4, Confidence: 1, Metadata: map[string]any{"grant_id": string(l.id), "capability": l.capability, "kind": "grant_lapsed"}})
		if !seen[l.mind] {
			seen[l.mind] = true
			out = append(out, l.mind)
			s.selfModelChanged(ctx, l.mind)
		}
	}
	return out, nil
}

// ---- capability requests ----

// CapabilityRequest is a mind's structured request for temporary authority.
// It carries its own justification so the operator can decide without
// reconstructing context: scope, duration, operation cap, reason, evidence,
// risk, mitigation, fallback and alternatives.
type CapabilityRequest struct {
	ID               ids.ID            `json:"id"`
	MindID           ids.ID            `json:"mind_id"`
	Capability       string            `json:"capability"`
	TargetPattern    string            `json:"target_pattern"`
	ParamConstraints map[string]string `json:"param_constraints"`
	Access           Access            `json:"access"`
	DurationSeconds  int               `json:"duration_seconds"`
	MaxOperations    *int              `json:"max_operations,omitempty"`
	Reason           string            `json:"reason"`
	Evidence         []EvidenceRef     `json:"evidence"`
	Risk             string            `json:"risk"`
	Mitigation       string            `json:"mitigation"`
	Fallback         string            `json:"fallback"`
	Alternatives     []string          `json:"alternatives"`
	History          map[string]int    `json:"history"` // this mind's earlier explicit decisions on the capability
	IntentionID      ids.ID            `json:"intention_id,omitempty"`
	PlanID           ids.ID            `json:"plan_id,omitempty"`
	StepID           ids.ID            `json:"step_id,omitempty"`
	BatchKey         string            `json:"batch_key"`
	Status           string            `json:"status"` // pending | granted | denied | expired | withdrawn
	DecidedBy        string            `json:"decided_by"`
	DecisionNote     string            `json:"decision_note"`
	DecidedAt        *time.Time        `json:"decided_at,omitempty"`
	GrantID          ids.ID            `json:"grant_id,omitempty"`
	EventID          ids.ID            `json:"event_id,omitempty"`
	ExpiresAt        time.Time         `json:"expires_at"`
	CreatedAt        time.Time         `json:"created_at"`
}

// ErrAttentionBudget is returned when a mind already has as many open
// requests as the operator's attention budget allows.
var ErrAttentionBudget = errors.New("capabilities: too many open capability requests for this mind; wait for decisions")

// ErrNotRequestable marks a capability that cannot be requested at all.
var ErrNotRequestable = errors.New("capabilities: this capability cannot be requested")

// RequestCapability records a request and raises it to operators. It grants
// nothing. Requests from one intention share a batch key so they can be
// decided together.
func (s *Service) RequestCapability(ctx context.Context, r CapabilityRequest) (CapabilityRequest, error) {
	if why, forbidden := Forbidden(ctx); forbidden {
		return CapabilityRequest{}, fmt.Errorf("%w: %s", ErrForbiddenContext, why)
	}
	c, ok := s.Registry.Get(r.Capability)
	if !ok {
		return CapabilityRequest{}, fmt.Errorf("%w: %s", ErrUnknown, r.Capability)
	}
	if c.Locked {
		return CapabilityRequest{}, fmt.Errorf("%w: %s is locked", ErrNotRequestable, c.Name)
	}
	r.Reason = strings.TrimSpace(r.Reason)
	if r.MindID.IsZero() || r.Reason == "" || len(r.Reason) > 2000 {
		return CapabilityRequest{}, fmt.Errorf("%w: mind and a reason (max 2000 chars) are required", ErrInvalid)
	}
	if r.DurationSeconds <= 0 || time.Duration(r.DurationSeconds)*time.Second > MaxGrantDuration {
		return CapabilityRequest{}, fmt.Errorf("%w: duration must be between 1 second and 7 days", ErrInvalid)
	}
	if r.MaxOperations != nil && (*r.MaxOperations <= 0 || *r.MaxOperations > 10000) {
		return CapabilityRequest{}, fmt.Errorf("%w: max_operations must be 1–10000", ErrInvalid)
	}
	if r.TargetPattern == "" {
		r.TargetPattern = "*"
	}
	if _, err := path.Match(r.TargetPattern, "x"); err != nil {
		return CapabilityRequest{}, fmt.Errorf("%w: bad target pattern", ErrInvalid)
	}
	if err := checkConstraints(c, r.ParamConstraints); err != nil {
		return CapabilityRequest{}, err
	}
	if r.Access != AccessRead {
		r.Access = c.Access
	}
	if r.Access == AccessRead && c.Access != AccessRead {
		r.Access = AccessWrite
	}
	for _, f := range []*string{&r.Risk, &r.Mitigation, &r.Fallback} {
		if len(*f) > 2000 {
			*f = (*f)[:2000]
		}
	}
	limit := s.MaxPendingRequests
	if limit <= 0 {
		limit = 5
	}
	var open int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM capability_requests WHERE mind_id=$1 AND status='pending'`, r.MindID).Scan(&open); err != nil {
		return CapabilityRequest{}, err
	}
	if open >= limit {
		return CapabilityRequest{}, ErrAttentionBudget
	}
	// Explicit history only: counts of earlier operator decisions. It informs
	// the operator; it never decides anything.
	r.History = map[string]int{}
	if rows, err := s.DB.Query(ctx, `SELECT status, count(*) FROM capability_requests WHERE mind_id=$1 AND capability=$2 AND status IN ('granted','denied') GROUP BY status`, r.MindID, r.Capability); err == nil {
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				r.History[st] = n
			}
		}
		rows.Close()
	}
	switch {
	case !r.IntentionID.IsZero():
		r.BatchKey = "intention:" + string(r.IntentionID)
	case !r.PlanID.IsZero():
		r.BatchKey = "plan:" + string(r.PlanID)
	default:
		r.BatchKey = "mind:" + string(r.MindID) + ":" + s.now().UTC().Format("2006-01-02T15")
	}
	ttl := s.RequestTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	r.ID, r.Status, r.CreatedAt, r.ExpiresAt = ids.New(), "pending", s.now(), s.now().Add(ttl)
	if r.Evidence == nil {
		r.Evidence = []EvidenceRef{}
	}
	if r.Alternatives == nil {
		r.Alternatives = []string{}
	}
	if r.ParamConstraints == nil {
		r.ParamConstraints = map[string]string{}
	}
	ev, err := s.Events.Append(ctx, events.New{MindID: r.MindID, Type: events.ToolRequested, Source: "capabilities", Summary: fmt.Sprintf("capability requested: %s on %q for %s — %s", c.Name, r.TargetPattern, time.Duration(r.DurationSeconds)*time.Second, truncate(r.Reason, 140)),
		Salience: 0.6, Confidence: 1, CorrelationID: r.IntentionID, Evidence: toEvidence(r.Evidence),
		Metadata: map[string]any{"kind": "capability_request", "request_id": string(r.ID), "capability": c.Name, "target": r.TargetPattern, "intention_id": string(r.IntentionID), "plan_id": string(r.PlanID), "step_id": string(r.StepID)}, Payload: r})
	if err != nil {
		return CapabilityRequest{}, err
	}
	r.EventID = ev[0].ID
	pc, _ := json.Marshal(r.ParamConstraints)
	evid, _ := json.Marshal(r.Evidence)
	alts, _ := json.Marshal(r.Alternatives)
	hist, _ := json.Marshal(r.History)
	_, err = s.DB.Exec(ctx, `INSERT INTO capability_requests (id, mind_id, capability, target_pattern, param_constraints, access, duration_seconds, max_operations, reason, evidence, risk, mitigation, fallback, alternatives, history, intention_id, plan_id, step_id, batch_key, status, event_id, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'pending',$20,$21,$22)`,
		r.ID, r.MindID, r.Capability, r.TargetPattern, pc, string(r.Access), r.DurationSeconds, r.MaxOperations, r.Reason, evid, r.Risk, r.Mitigation, r.Fallback, alts, hist, nullID(r.IntentionID), nullID(r.PlanID), nullID(r.StepID), r.BatchKey, r.EventID, r.ExpiresAt, r.CreatedAt)
	return r, err
}

const requestCols = `id, mind_id, capability, target_pattern, param_constraints, access, duration_seconds, max_operations, reason, evidence, risk, mitigation, fallback, alternatives, history, intention_id, plan_id, step_id, batch_key, status, decided_by, decision_note, decided_at, grant_id, event_id, expires_at, created_at`

func scanRequest(row pgx.Row) (CapabilityRequest, error) {
	var r CapabilityRequest
	var pc, evid, alts, hist []byte
	var access string
	var intention, plan, step, grant, ev *string
	if err := row.Scan(&r.ID, &r.MindID, &r.Capability, &r.TargetPattern, &pc, &access, &r.DurationSeconds, &r.MaxOperations, &r.Reason, &evid, &r.Risk, &r.Mitigation, &r.Fallback, &alts, &hist,
		&intention, &plan, &step, &r.BatchKey, &r.Status, &r.DecidedBy, &r.DecisionNote, &r.DecidedAt, &grant, &ev, &r.ExpiresAt, &r.CreatedAt); err != nil {
		return r, storage.Translate(err)
	}
	r.Access = Access(access)
	json.Unmarshal(pc, &r.ParamConstraints) //nolint:errcheck
	json.Unmarshal(evid, &r.Evidence)       //nolint:errcheck
	json.Unmarshal(alts, &r.Alternatives)   //nolint:errcheck
	json.Unmarshal(hist, &r.History)        //nolint:errcheck
	for _, x := range []struct {
		src *string
		dst *ids.ID
	}{{intention, &r.IntentionID}, {plan, &r.PlanID}, {step, &r.StepID}, {grant, &r.GrantID}, {ev, &r.EventID}} {
		if x.src != nil {
			*x.dst = ids.ID(*x.src)
		}
	}
	return r, nil
}

// CapabilityRequests lists requests (mind Nil = all, status empty = all), newest first.
func (s *Service) CapabilityRequests(ctx context.Context, mind ids.ID, status string, limit int) ([]CapabilityRequest, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT `+requestCols+` FROM capability_requests WHERE ($1::uuid IS NULL OR mind_id=$1) AND ($2='' OR status=$2) ORDER BY batch_key, created_at DESC LIMIT $3`, nullID(mind), status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CapabilityRequest
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetCapabilityRequest fetches one request.
func (s *Service) GetCapabilityRequest(ctx context.Context, id ids.ID) (CapabilityRequest, error) {
	return scanRequest(s.DB.QueryRow(ctx, `SELECT `+requestCols+` FROM capability_requests WHERE id=$1`, id))
}

// Narrowing lets an operator grant less than was asked.
type Narrowing struct {
	DurationSeconds int
	MaxOperations   *int
	TargetPattern   string
}

// DecideCapabilityRequest grants (as a scoped grant, possibly narrowed) or
// denies a pending request. The decider must be a named person: internal
// component names are refused, so cognition cannot approve its own request.
func (s *Service) DecideCapabilityRequest(ctx context.Context, id ids.ID, by string, grant bool, note string, n Narrowing) (CapabilityRequest, error) {
	if !ValidDecider(by) {
		return CapabilityRequest{}, ErrReservedDecider
	}
	r, err := s.GetCapabilityRequest(ctx, id)
	if err != nil {
		return r, err
	}
	if r.Status != "pending" {
		return r, fmt.Errorf("%w: request is %s", ErrInvalid, r.Status)
	}
	if s.now().After(r.ExpiresAt) {
		s.DB.Exec(ctx, `UPDATE capability_requests SET status='expired' WHERE id=$1 AND status='pending'`, id) //nolint:errcheck
		return r, fmt.Errorf("%w: request expired", ErrInvalid)
	}
	if !grant {
		tag, err := s.DB.Exec(ctx, `UPDATE capability_requests SET status='denied', decided_by=$2, decision_note=$3, decided_at=$4 WHERE id=$1 AND status='pending'`, id, by, note, s.now())
		if err != nil {
			return r, err
		}
		if tag.RowsAffected() == 0 {
			return r, fmt.Errorf("%w: request already decided", ErrInvalid)
		}
		s.Events.Append(ctx, events.New{MindID: r.MindID, Type: events.ToolDenied, Source: "operator", Summary: fmt.Sprintf("operator %s denied the request for %s on %q: %s", by, r.Capability, r.TargetPattern, firstNonEmpty(note, "no reason given")), //nolint:errcheck
			Salience: 0.5, Confidence: 1, CausationID: r.EventID, CorrelationID: r.IntentionID, Metadata: map[string]any{"kind": "capability_request", "request_id": string(r.ID), "actor": by, "capability": r.Capability, "intention_id": string(r.IntentionID), "plan_id": string(r.PlanID), "step_id": string(r.StepID)}, Payload: map[string]any{"note": note}})
		return s.GetCapabilityRequest(ctx, id)
	}
	dur, ops, target := r.DurationSeconds, r.MaxOperations, r.TargetPattern
	if n.DurationSeconds > 0 && n.DurationSeconds < dur {
		dur = n.DurationSeconds
	}
	if n.MaxOperations != nil && (ops == nil || *n.MaxOperations < *ops) {
		ops = n.MaxOperations
	}
	if n.TargetPattern != "" {
		target = n.TargetPattern
	}
	// Claim the request first so two operators cannot both grant it.
	tag, err := s.DB.Exec(ctx, `UPDATE capability_requests SET status='granted', decided_by=$2, decision_note=$3, decided_at=$4 WHERE id=$1 AND status='pending'`, id, by, note, s.now())
	if err != nil {
		return r, err
	}
	if tag.RowsAffected() == 0 {
		return r, fmt.Errorf("%w: request already decided", ErrInvalid)
	}
	g, err := s.Grant(ctx, GrantInput{MindID: r.MindID, Capability: r.Capability, TargetPattern: target, ParamConstraints: r.ParamConstraints, Access: r.Access, Duration: time.Duration(dur) * time.Second, MaxOperations: ops, By: by, Note: note, RequestID: r.ID})
	if err != nil {
		s.DB.Exec(ctx, `UPDATE capability_requests SET status='pending', decided_by='', decision_note='', decided_at=NULL WHERE id=$1`, id) //nolint:errcheck
		return r, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE capability_requests SET grant_id=$2 WHERE id=$1`, id, g.ID); err != nil {
		return r, err
	}
	return s.GetCapabilityRequest(ctx, id)
}

// WithdrawCapabilityRequest lets the requesting mind drop a request it no
// longer needs. Withdrawing never grants anything.
func (s *Service) WithdrawCapabilityRequest(ctx context.Context, mind, id ids.ID, reason string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE capability_requests SET status='withdrawn', decision_note=$3, decided_at=$4 WHERE id=$1 AND mind_id=$2 AND status='pending'`, id, mind, reason, s.now())
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// ExpireCapabilityRequests marks overdue requests expired.
func (s *Service) ExpireCapabilityRequests(ctx context.Context) (int64, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE capability_requests SET status='expired' WHERE status='pending' AND expires_at < $1`, s.now())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
