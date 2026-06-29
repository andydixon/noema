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
	TargetKind    string           `json:"target_kind"` // url | path | host | service | repository | none
	Params        map[string]Param `json:"params"`
	DefaultEffect Effect           `json:"default_effect"` // when no policy matches
	Locked        bool             `json:"locked"`         // always DENY; cannot be relaxed
	Executor      Executor         `json:"-"`
}

// Registry holds capabilities. It is built in code; nothing at runtime can
// add one, so a mind cannot invent tools.
type Registry struct{ caps map[string]Capability }

// NewRegistry creates an empty registry.
func NewRegistry() *Registry { return &Registry{caps: map[string]Capability{}} }

// Add registers a capability.
func (r *Registry) Add(c Capability) *Registry {
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
	Specificity int    `json:"specificity"`
	Reason      string `json:"reason"`
	Locked      bool   `json:"locked"`
	Considered  int    `json:"considered"`
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
