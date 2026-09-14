package planning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/ids"
)

// ErrNoPlan means no strategy is available for an intention.
var ErrNoPlan = errors.New("planning: no possible plan")

// Planner proposes drafts. Deterministic strategies come first; a model is
// consulted only when none applies, and its proposal is validated like any
// other draft.
type Planner struct {
	Registry *capabilities.Registry
	// Effect previews the policy effect for a request (allow, ask, deny).
	Effect func(ctx context.Context, mind ids.ID, capability, target string, params map[string]any) string
	// Model optionally proposes a plan as JSON. Nil disables model planning.
	Model func(ctx context.Context, mind ids.ID, objective string, vocabulary []capabilities.Capability) (raw string, call ids.ID, err error)
}

// Verifier returns how to check that a writing capability achieved its
// usual outcome, or nil when no observation is known.
func Verifier(capability string, params map[string]any) (*Check, string) {
	switch capability {
	case "service.restart":
		unit, _ := params["service"].(string)
		return &Check{Capability: "service.status", Params: map[string]any{"service": unit}, Path: "active", Op: "eq", Value: true, Description: unit + " is active"}, unit + " is running normally again"
	}
	return nil, ""
}

type strategy struct {
	name       string
	confidence float64
	build      func() (Draft, error)
}

// Propose drafts a plan for an intention, avoiding strategies that already
// failed in earlier revisions. The alternatives considered are recorded.
func (p *Planner) Propose(ctx context.Context, it volition.Intention, previous []Plan) (Draft, error) {
	used := map[string]bool{}
	for _, pl := range previous {
		if pl.Status == PlanFailed || pl.Status == PlanAbandoned || pl.Status == PlanSuperseded || pl.Status == PlanReplanning {
			used[pl.Strategy] = true
		}
	}
	var strategies []strategy
	prop := it.Proposal
	switch {
	case prop != nil && prop.Kind == "capability":
		strategies = append(strategies, strategy{"direct", clamp(0.4+0.4*it.Confidence, 0.4, 0.85), func() (Draft, error) { return p.direct(ctx, it, *prop) }})
		if unit := unitOf(prop.Params); unit != "" && strings.HasPrefix(prop.Capability, "service.") {
			strategies = append(strategies, strategy{"diagnose_then_escalate", 0.5, func() (Draft, error) { return p.escalate(ctx, it, unit, *prop) }})
		}
	case prop != nil && prop.Kind == "investigate":
		if unit := unitFromSubject(prop.Subject); unit != "" {
			strategies = append(strategies, strategy{"diagnose", 0.6, func() (Draft, error) { return p.diagnose(ctx, it, unit, "diagnose") }})
		}
	}
	if p.Model != nil {
		strategies = append(strategies, strategy{"model", 0.45, func() (Draft, error) { return p.fromModel(ctx, it) }})
	}
	var alts []Alternative
	var chosen *Draft
	for _, s := range strategies {
		if used[s.name] {
			alts = append(alts, Alternative{Strategy: s.name, Description: "tried in an earlier revision", Why: "already failed"})
			continue
		}
		d, err := s.build()
		if err != nil {
			alts = append(alts, Alternative{Strategy: s.name, Description: err.Error(), Why: "not buildable"})
			continue
		}
		est := EstimateDraft(d, p.Registry)
		score := round3(s.confidence - 0.05*float64(est.Approvals) - 0.001*est.WallSeconds - 0.01*est.Money)
		a := Alternative{Strategy: s.name, Description: d.Objective, Estimate: est, ExpectedConfidence: s.confidence, Score: score}
		if chosen == nil {
			a.Selected, a.Why = true, "first applicable strategy in preference order that has not failed"
			dd := d
			chosen = &dd
		}
		alts = append(alts, a)
	}
	if chosen == nil {
		return Draft{Alternatives: alts}, fmt.Errorf("%w for %q", ErrNoPlan, it.Description)
	}
	chosen.Alternatives = alts
	if len(previous) > 0 {
		chosen.Origin = map[bool]string{true: "model", false: "replan"}[chosen.Origin == "model"]
	}
	return *chosen, nil
}

func (p *Planner) effect(ctx context.Context, mind ids.ID, capability, target string, params map[string]any) string {
	c, ok := p.Registry.Get(capability)
	if !ok {
		return "unknown"
	}
	if c.Locked {
		return "locked"
	}
	if p.Effect == nil {
		return string(c.DefaultEffect)
	}
	return p.Effect(ctx, mind, capability, target, params)
}

func (p *Planner) direct(ctx context.Context, it volition.Intention, prop volition.Proposal) (Draft, error) {
	c, ok := p.Registry.Get(prop.Capability)
	if !ok || c.Locked {
		return Draft{}, fmt.Errorf("%s is not usable", prop.Capability)
	}
	target := prop.Target
	if c.TargetFrom != nil {
		target = "" // derived from the parameters during validation
	}
	if e := p.effect(ctx, it.MindID, prop.Capability, target, prop.Params); e == "deny" || e == "locked" || e == "unknown" {
		return Draft{}, fmt.Errorf("%s is %s by policy", prop.Capability, e)
	}
	act := StepSpec{Key: "P1", Title: "run " + prop.Capability + " on " + firstNonEmpty(prop.Target, "its target"), Kind: KindCapability, Capability: prop.Capability, Target: target, Params: copyParams(prop.Params),
		RequireApproval: it.RequireApproval, MaxAttempts: 1}
	if c.Idempotent {
		act.MaxAttempts = 2
	}
	steps := []StepSpec{act}
	criteria := prop.Capability + " completes"
	if ck, outcome := Verifier(prop.Capability, prop.Params); ck != nil {
		steps[0].Expectation = &Expectation{Description: outcome + " within 60 seconds", Confidence: clamp(it.Confidence, 0.5, 0.9), WithinSeconds: 60, BeliefID: firstBelief(it.BeliefIDs)}
		steps = append(steps, StepSpec{Key: "V1", Title: "verify: " + ck.Description, Kind: KindVerify, DependsOn: []string{"P1"}, Verify: &VerifySpec{Of: "P1", Check: *ck, WithinSeconds: 60, IntervalSeconds: 10}})
		criteria = outcome + ", confirmed by observation rather than by the command returning"
	}
	d := Draft{Objective: it.Description, Origin: "template", Strategy: "direct", Steps: steps, CompletionCriteria: criteria, Risk: string(c.Risk) + ": " + c.SideEffects,
		AbortConditions: []string{"approval is denied", "the expected outcome is not observed", "the capability becomes unavailable"}, RequireApproval: it.RequireApproval, Budget: Budget{MaxInvocations: 10}}
	for _, b := range it.BeliefIDs {
		d.Assumptions = append(d.Assumptions, Assumption{BeliefID: b, Proposition: "supporting belief " + string(b), MinConfidence: 0.3})
	}
	return d, nil
}

// diagnose builds read-only investigation of a service: its status, host
// memory and disk, and its logs. When reading logs needs approval and no
// grant is in force, the plan first requests temporary scoped access, with
// the risk and fallback stated.
func (p *Planner) diagnose(ctx context.Context, it volition.Intention, unit, name string) (Draft, error) {
	var steps []StepSpec
	var deps []string
	add := func(s StepSpec) {
		steps = append(steps, s)
		deps = append(deps, s.Key)
	}
	usable := func(capability string, params map[string]any) (string, bool) {
		e := p.effect(ctx, it.MindID, capability, "", params)
		return e, e == "allow" || e == "ask"
	}
	svc := map[string]any{"service": unit}
	if _, ok := usable("service.status", svc); ok {
		add(StepSpec{Key: "D1", Title: "check the status of " + unit, Kind: KindCapability, Capability: "service.status", Params: svc, MaxAttempts: 2})
	}
	if _, ok := usable("host.memory.status", nil); ok {
		add(StepSpec{Key: "D2", Title: "check host memory", Kind: KindCapability, Capability: "host.memory.status", MaxAttempts: 2})
	}
	if _, ok := usable("host.disk.usage", map[string]any{"path": "/"}); ok {
		add(StepSpec{Key: "D3", Title: "check disk space", Kind: KindCapability, Capability: "host.disk.usage", Params: map[string]any{"path": "/"}, MaxAttempts: 2})
	}
	logParams := map[string]any{"service": unit, "since_minutes": 120, "lines": 300}
	if e, ok := usable("logs.query", logParams); ok {
		logStep := StepSpec{Key: "D4", Title: "read recent logs of " + unit, Kind: KindCapability, Capability: "logs.query", Params: logParams, MaxAttempts: 2}
		if e == "ask" {
			ops := 20
			steps = append(steps, StepSpec{Key: "R1", Title: "request temporary read access to " + unit + " logs", Kind: KindRequestCapability, Request: &RequestSpec{
				Capability: "logs.query", TargetPattern: strings.TrimSuffix(unit, ".service") + "*", DurationSeconds: 1800, MaxOperations: &ops,
				Reason: "status and host metrics alone cannot distinguish a failure inside " + unit + " from a failure it depends on; its logs can",
				Risk:   "logs may contain personal identifiers and credentials", Mitigation: "e-mail and IP addresses and credential-like values are masked by default",
				Fallback: "ask the operator to read the logs and report what they show", Alternatives: []string{"continue with status and host metrics only"}}})
			logStep.DependsOn = []string{"R1"}
		}
		add(logStep)
	}
	if len(deps) == 0 {
		return Draft{}, errors.New("no read-only capability is usable for " + unit)
	}
	steps = append(steps, StepSpec{Key: "S1", Title: "summarise the findings", Kind: KindInternal, DependsOn: deps, Internal: &InternalSpec{Action: "summarise_findings"}})
	return Draft{Objective: "diagnose " + unit, Origin: "template", Strategy: name, Steps: steps, CompletionCriteria: "every reading is recorded and summarised as observations for belief formation",
		AbortConditions: []string{"read access is denied and no other reading is possible"}, Risk: "read-only; logs may contain personal data", Budget: Budget{MaxInvocations: 12}}, nil
}

// escalate is the strategy after a direct service action failed: diagnose,
// then tell the operator what was tried and seen, without repeating the action.
func (p *Planner) escalate(ctx context.Context, it volition.Intention, unit string, prop volition.Proposal) (Draft, error) {
	d, err := p.diagnose(ctx, it, unit, "diagnose_then_escalate")
	if err != nil {
		return Draft{}, err
	}
	if e := p.effect(ctx, it.MindID, "noema.note", "", map[string]any{"text": "x"}); e == "deny" || e == "locked" || e == "unknown" {
		return Draft{}, errors.New("cannot leave a note for the operator")
	}
	d.Objective = "diagnose " + unit + " and escalate to the operator instead of repeating " + prop.Capability
	d.Steps = append(d.Steps, StepSpec{Key: "N1", Title: "tell the operator what was tried and observed", Kind: KindCapability, Capability: "noema.note", DependsOn: []string{"S1"},
		Params: map[string]any{"text": fmt.Sprintf("I tried %s on %s and the expected outcome did not follow. I have gathered diagnostics and am not repeating the action. Please review: %s", prop.Capability, unit, it.Description)}})
	d.CompletionCriteria = "diagnostics recorded and the operator informed; the failed action is not repeated"
	return d, nil
}

func (p *Planner) fromModel(ctx context.Context, it volition.Intention) (Draft, error) {
	raw, call, err := p.Model(ctx, it.MindID, it.Description, p.Registry.List())
	if err != nil {
		return Draft{}, err
	}
	d, err := ParseProposal(raw)
	if err != nil {
		return Draft{}, err
	}
	d.Origin, d.Strategy, d.LLMCall = "model", "model", call
	if d.Objective == "" {
		d.Objective = it.Description
	}
	d.RequireApproval = it.RequireApproval
	return d, nil
}

var jsonObject = regexp.MustCompile(`(?s)\{.*\}`)

// allowed fields of a model proposal; anything else is reported as ignored.
var (
	draftFields = map[string]bool{"objective": true, "steps": true, "assumptions": true, "completion_criteria": true, "abort_conditions": true, "risk": true}
	stepFields  = map[string]bool{"key": true, "title": true, "kind": true, "depends_on": true, "capability": true, "target": true, "params": true, "expectation": true, "precondition": true, "request": true, "verify": true, "monitor": true, "wait": true, "internal": true, "max_attempts": true}
)

// ParseProposal reads a model's JSON plan into a draft. Fields that could
// carry authority or claim progress (status, approved, completed, policy,
// require_approval=false overrides) are not part of the structure and are
// reported in Ignored; the draft must still pass Validate.
func ParseProposal(raw string) (Draft, error) {
	m := jsonObject.FindString(raw)
	if m == "" {
		return Draft{}, errors.New("model returned no JSON object")
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(m), &generic); err != nil {
		return Draft{}, fmt.Errorf("model plan is not valid JSON: %w", err)
	}
	var ignored []string
	for k := range generic {
		if !draftFields[k] {
			ignored = append(ignored, k)
		}
	}
	if steps, ok := generic["steps"].([]any); ok {
		for i, s := range steps {
			if sm, ok := s.(map[string]any); ok {
				for k := range sm {
					if !stepFields[k] {
						ignored = append(ignored, fmt.Sprintf("steps[%d].%s", i, k))
					}
				}
			}
		}
	}
	var d Draft
	var in struct {
		Objective          string       `json:"objective"`
		Steps              []StepSpec   `json:"steps"`
		Assumptions        []Assumption `json:"assumptions"`
		CompletionCriteria string       `json:"completion_criteria"`
		AbortConditions    []string     `json:"abort_conditions"`
		Risk               string       `json:"risk"`
	}
	if err := json.Unmarshal([]byte(m), &in); err != nil {
		return Draft{}, fmt.Errorf("model plan does not fit the plan structure: %w", err)
	}
	for i := range in.Steps {
		in.Steps[i].RequireApproval = false // approval requirements come from commitments and policy, never from the model
	}
	d.Objective, d.Steps, d.Assumptions, d.CompletionCriteria, d.AbortConditions, d.Risk = in.Objective, in.Steps, in.Assumptions, in.CompletionCriteria, in.AbortConditions, in.Risk
	sort.Strings(ignored)
	d.Ignored = ignored
	return d, nil
}

// EstimateDraft sums capability estimates and counts approvals the plan is
// likely to need (steps whose capability asks by default or must be approved).
func EstimateDraft(d Draft, reg *capabilities.Registry) Resources {
	var r Resources
	for _, s := range d.Steps {
		switch s.Kind {
		case KindCapability:
			if reg == nil {
				r.Invocations++
				if s.RequireApproval {
					r.Approvals++
				}
				continue
			}
			c, ok := reg.Get(s.Capability)
			if !ok {
				continue
			}
			n := 1
			r.Invocations += n
			r.WallSeconds += c.Estimate.WallSeconds
			r.Network += c.Estimate.Network
			r.Money += c.Estimate.Money
			r.Tokens += c.Estimate.Tokens
			if s.RequireApproval || c.DefaultEffect == capabilities.Ask {
				r.Approvals++
			}
		case KindRequestCapability:
			r.Approvals++
		case KindVerify:
			if s.Verify != nil {
				r.Invocations += max(1, s.Verify.WithinSeconds/max(1, s.Verify.IntervalSeconds))
				r.WallSeconds += float64(s.Verify.WithinSeconds)
			}
		case KindMonitor:
			if s.Monitor != nil {
				r.Invocations += max(1, s.Monitor.DeadlineSeconds/max(1, s.Monitor.IntervalSeconds))
				r.WallSeconds += float64(s.Monitor.StableSeconds)
			}
		case KindWait:
			if s.Wait != nil {
				r.WallSeconds += float64(s.Wait.Seconds)
			}
		}
	}
	return r
}

func unitOf(params map[string]any) string {
	u, _ := params["service"].(string)
	return u
}

var unitToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,127}$`)

// unitFromSubject picks a service-like token from an investigation subject.
func unitFromSubject(subject string) string {
	for _, f := range strings.Fields(subject) {
		f = strings.Trim(f, ".,;:!?()\"'")
		if unitToken.MatchString(f) && strings.ToLower(f) != "the" && len(f) > 2 {
			return strings.ToLower(f)
		}
	}
	return ""
}

func copyParams(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstBelief(xs []ids.ID) ids.ID {
	if len(xs) > 0 {
		return xs[0]
	}
	return ids.Nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
