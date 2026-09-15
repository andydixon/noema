// Package capabilities is the only path from cognition to the outside world
// (ADR-0005, spec §29–30). A capability is registered code with a parameter
// schema and a risk class; a policy engine decides ALLOW / ASK / DENY per
// mind, capability, target and time; ASK creates an approval request bound
// to the exact request; every step is an event.
package capabilities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Risk classifies the worst plausible consequence of a capability.
type Risk string

// Risk classes.
const (
	RiskLow      Risk = "low"      // read-only, bounded
	RiskMedium   Risk = "medium"   // writes to a sandboxed or reversible place
	RiskHigh     Risk = "high"     // visible external side effects
	RiskCritical Risk = "critical" // irreversible or security-relevant
)

// Effect is a policy outcome.
type Effect string

// Effects.
const (
	Allow Effect = "allow"
	Ask   Effect = "ask"
	Deny  Effect = "deny"
)

// Valid reports whether e is known.
func (e Effect) Valid() bool { return e == Allow || e == Ask || e == Deny }

// Param describes one parameter.
type Param struct {
	Type        string `json:"type"` // string | int | bool | url | path
	Required    bool   `json:"required"`
	Description string `json:"description"`
	MaxLen      int    `json:"max_len,omitempty"`
}

// Access says whether a capability changes anything outside Noema.
type Access string

// Access classes. Unknown access is treated as write (fail closed).
const (
	AccessRead  Access = "read"  // observes; no external state changes
	AccessWrite Access = "write" // changes external state
)

// Cost is a capability's expected resource use per invocation, used for plan
// estimates. Zero values mean "negligible", not "unknown".
type Cost struct {
	WallSeconds float64 `json:"wall_seconds,omitempty"`
	Network     int     `json:"network_operations,omitempty"`
	Money       float64 `json:"money,omitempty"`
	Tokens      int     `json:"llm_tokens,omitempty"`
}

// Invocation is a validated request to run a capability.
type Invocation struct {
	MindID      ids.ID
	Capability  string
	Target      string
	Params      map[string]any
	Reason      string
	Evidence    []EvidenceRef
	Correlation ids.ID
	ApprovalID  ids.ID
	// RequireApproval turns an ALLOW decision into ASK. Cognition sets it
	// when a commitment or a value constraint demands approval; it can only
	// tighten a decision.
	RequireApproval bool
	// Origin names the cognitive object that asked (for example
	// "plan_step:<id>"), recorded with the decision for provenance.
	Origin string
}

// EvidenceRef mirrors events.EvidenceRef without importing it (keeps this
// package free of the event store so executors can be tested alone).
type EvidenceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

// Result is what an executor returns. Output is untrusted data: it becomes an
// observation with tool trust, never an instruction.
type Result struct {
	Summary string         `json:"summary"`
	Output  string         `json:"output,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Executor runs a capability.
type Executor func(ctx context.Context, inv Invocation) (Result, error)

// Capability is a registered tool.
type Capability struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Risk          Risk             `json:"risk"`
	TargetKind    string           `json:"target_kind"` // url | path | host | service | repository | domain | none
	Params        map[string]Param `json:"params"`
	DefaultEffect Effect           `json:"default_effect"` // when no policy matches
	Locked        bool             `json:"locked"`         // always DENY; cannot be relaxed
	Access        Access           `json:"access"`         // read | write
	SideEffects   string           `json:"side_effects"`   // what changes in the world when it runs
	Idempotent    bool             `json:"idempotent"`     // running it twice has the same effect as once
	Timeout       time.Duration    `json:"timeout"`        // executor deadline (default 60s)
	Estimate      Cost             `json:"estimate"`
	// TargetFrom derives the policy target from validated parameters (the
	// unit a restart acts on, the host of a URL), so a policy or grant on a
	// target always matches what the executor will touch. A caller-supplied
	// target that disagrees is refused.
	TargetFrom func(params map[string]any) (string, error) `json:"-"`
	// OutputTrust is the trust given to the observation made from the
	// output (default tool). Content fetched from the web is low trust.
	OutputTrust events.Trust `json:"output_trust"`
	Executor    Executor     `json:"-"`
}

// Registry holds capabilities. It is built in code at start-up and frozen
// before any mind runs; nothing at runtime can add one, so a mind cannot
// invent tools.
type Registry struct {
	caps   map[string]Capability
	frozen bool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{caps: map[string]Capability{}} }

// Add registers a capability. Adding to a frozen registry is a programming
// error and panics: capabilities are code, never runtime data.
func (r *Registry) Add(c Capability) *Registry {
	if r.frozen {
		panic("capabilities: registry is frozen; " + c.Name + " cannot be added at runtime")
	}
	if c.Access != AccessRead {
		c.Access = AccessWrite
	}
	if c.Timeout <= 0 {
		c.Timeout = 60 * time.Second
	}
	if !c.OutputTrust.Valid() {
		c.OutputTrust = events.TrustTool
	}
	if c.Params == nil {
		c.Params = map[string]Param{}
	}
	if !c.DefaultEffect.Valid() {
		c.DefaultEffect = Ask
	}
	if c.Locked {
		c.DefaultEffect = Deny
	}
	r.caps[c.Name] = c
	return r
}

// Freeze stops further registration.
func (r *Registry) Freeze() { r.frozen = true }

// Frozen reports whether the registry accepts no more capabilities.
func (r *Registry) Frozen() bool { return r.frozen }

// Get returns a capability.
func (r *Registry) Get(name string) (Capability, bool) {
	c, ok := r.caps[name]
	return c, ok
}

// List returns capabilities sorted by name.
func (r *Registry) List() []Capability {
	out := make([]Capability, 0, len(r.caps))
	for _, c := range r.caps {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ErrInvalid marks bad input; ErrForbiddenContext marks dreaming/replay contexts.
var (
	ErrInvalid          = errors.New("capabilities: invalid")
	ErrForbiddenContext = errors.New("capabilities: actions are not permitted in this context")
	ErrUnknown          = errors.New("capabilities: unknown capability")
)

// ValidateParams checks and normalises params against the schema. Unknown
// keys are rejected so a model cannot smuggle extra arguments.
func (c Capability) ValidateParams(params map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for k := range params {
		if _, ok := c.Params[k]; !ok {
			return nil, fmt.Errorf("%w: unknown parameter %q", ErrInvalid, k)
		}
	}
	for name, p := range c.Params {
		v, present := params[name]
		if !present {
			if p.Required {
				return nil, fmt.Errorf("%w: missing parameter %q", ErrInvalid, name)
			}
			continue
		}
		switch p.Type {
		case "string", "url", "path":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%w: %q must be a string", ErrInvalid, name)
			}
			if p.MaxLen > 0 && len(s) > p.MaxLen {
				return nil, fmt.Errorf("%w: %q too long", ErrInvalid, name)
			}
			if p.Type == "url" && !(strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")) {
				return nil, fmt.Errorf("%w: %q must be an http(s) URL", ErrInvalid, name)
			}
			if p.Type == "path" && (strings.Contains(s, "..") || strings.ContainsRune(s, 0)) {
				return nil, fmt.Errorf("%w: %q contains an unsafe path", ErrInvalid, name)
			}
			out[name] = s
		case "int":
			switch n := v.(type) {
			case float64:
				out[name] = int(n)
			case int:
				out[name] = n
			default:
				return nil, fmt.Errorf("%w: %q must be an integer", ErrInvalid, name)
			}
		case "bool":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: %q must be a boolean", ErrInvalid, name)
			}
			out[name] = b
		default:
			out[name] = v
		}
	}
	return out, nil
}

// ResolveTarget applies TargetFrom: it returns the target the request acts
// on, or an error when the caller named a different one.
func (c Capability) ResolveTarget(target string, params map[string]any) (string, error) {
	if c.TargetFrom == nil {
		return target, nil
	}
	derived, err := c.TargetFrom(params)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if target != "" && !strings.EqualFold(target, derived) {
		return "", fmt.Errorf("%w: target %q does not match the parameters (%q)", ErrInvalid, target, derived)
	}
	return derived, nil
}

// Hash binds an approval to the exact request.
func Hash(mind ids.ID, capability, target string, params map[string]any) string {
	raw, _ := json.Marshal(struct {
		M ids.ID
		C string
		T string
		P map[string]any
	}{mind, capability, target, params})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])[:32]
}

// ---- policy engine ----

// Policy is one rule.
type Policy struct {
	ID            ids.ID    `json:"id"`
	MindID        ids.ID    `json:"mind_id,omitempty"` // empty = global
	Capability    string    `json:"capability"`        // name or *
	TargetPattern string    `json:"target_pattern"`    // glob or *
	ActionKind    string    `json:"action_kind"`       // * or a capability-specific action label
	Effect        Effect    `json:"effect"`
	HoursFrom     *int      `json:"hours_from,omitempty"`
	HoursTo       *int      `json:"hours_to,omitempty"`
	Reason        string    `json:"reason"`
	CreatedBy     string    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// matches reports whether the policy applies, and its specificity.
func (p Policy) matches(mind ids.ID, capability, target, action string, now time.Time) (bool, int) {
	spec := 0
	if !p.MindID.IsZero() {
		if p.MindID != mind {
			return false, 0
		}
		spec += 8
	}
	if p.Capability != "*" {
		if p.Capability != capability {
			return false, 0
		}
		spec += 4
	}
	if p.TargetPattern != "*" {
		if ok, _ := path.Match(p.TargetPattern, target); !ok && !strings.EqualFold(p.TargetPattern, target) {
			return false, 0
		}
		spec += 2
	}
	if p.ActionKind != "*" {
		if p.ActionKind != action {
			return false, 0
		}
		spec++
	}
	if p.HoursFrom != nil && p.HoursTo != nil {
		h := now.Hour()
		from, to := *p.HoursFrom, *p.HoursTo
		inWindow := (from <= to && h >= from && h < to) || (from > to && (h >= from || h < to))
		if !inWindow {
			return false, 0
		}
		spec++
	}
	return true, spec
}

// Decision explains a policy outcome.
type Decision struct {
	Effect      Effect `json:"effect"`
	PolicyID    ids.ID `json:"policy_id,omitempty"`
	GrantID     ids.ID `json:"grant_id,omitempty"` // scoped grant that turned ASK into ALLOW
	Specificity int    `json:"specificity"`
	Reason      string `json:"reason"`
	Locked      bool   `json:"locked"`
	Considered  int    `json:"considered"`
	Tightened   bool   `json:"tightened,omitempty"` // ALLOW became ASK because the invocation required approval
}

func rank(e Effect) int {
	switch e {
	case Deny:
		return 3
	case Ask:
		return 2
	}
	return 1
}

// Evaluate picks the most specific matching policy; on ties the stricter
// effect wins (DENY > ASK > ALLOW). Locked capabilities always deny.
func Evaluate(c Capability, policies []Policy, mind ids.ID, target, action string, now time.Time) Decision {
	if c.Locked {
		return Decision{Effect: Deny, Reason: "capability is locked to DENY by design", Locked: true}
	}
	best := Decision{Effect: c.DefaultEffect, Reason: "no matching policy; capability default"}
	bestSpec := -1
	for _, p := range policies {
		ok, spec := p.matches(mind, c.Name, target, action, now)
		if !ok {
			continue
		}
		best.Considered++
		if spec > bestSpec || (spec == bestSpec && rank(p.Effect) > rank(best.Effect)) {
			bestSpec = spec
			best.Effect, best.PolicyID, best.Specificity = p.Effect, p.ID, spec
			best.Reason = fmt.Sprintf("policy %s (specificity %d): %s", shortID(p.ID), spec, firstNonEmpty(p.Reason, describe(p)))
		}
	}
	return best
}

func describe(p Policy) string {
	scope := "global"
	if !p.MindID.IsZero() {
		scope = "mind"
	}
	return fmt.Sprintf("%s rule %s on %s target %s", scope, p.Effect, p.Capability, p.TargetPattern)
}

func shortID(id ids.ID) string {
	if len(id) >= 8 {
		return string(id[:8])
	}
	return string(id)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- context guards ----

type ctxKey struct{}

// WithForbidden marks a context (dreaming, replay) in which no capability may
// run. Invoke refuses unconditionally.
func WithForbidden(ctx context.Context, why string) context.Context {
	return context.WithValue(ctx, ctxKey{}, why)
}

// Forbidden reports whether actions are refused in ctx.
func Forbidden(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(ctxKey{}).(string)
	return s, ok
}
