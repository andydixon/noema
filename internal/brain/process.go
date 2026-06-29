// Package brain runs the cognitive cycle for each mind: it gathers new events,
// asks registered cognitive processes for thought candidates, scores them
// deterministically, admits the winners to the global workspace, dispatches
// them to executive handlers and records every step as events (ADR-0001).
package brain

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Process is a cognitive faculty. Observe must be safe to call concurrently
// with other processes, must respect ctx, and must not mutate the state it is
// given; it returns candidates and lets the cycle decide.
type Process interface {
	Name() string
	Observe(ctx context.Context, s *State) ([]Candidate, error)
}

// Candidate is a proposed thought.
type Candidate struct {
	Kind         string               // attend | hypothesis | question | respond | goal | retrieve | reflect | note | action
	Subject      string               // stable key for refresh and loop detection (entity, event id, proposition hash)
	Summary      string               // one line, human readable
	Content      map[string]any       // structured detail for handlers and the UI
	Features     attention.Features   // scoring inputs
	Confidence   float64              // epistemic confidence of the candidate itself
	Evidence     []events.EvidenceRef // what it rests on
	SourceEvents []ids.ID             // events that triggered it (first becomes causation)
	// filled by the cycle
	ID      ids.ID
	Process string
}

// Mode is the coarse cognitive mode.
type Mode string

// Modes. Later phases add reflecting, consolidating and dreaming.
const (
	ModeAwake         Mode = "awake"
	ModeFocused       Mode = "focused"
	ModeIdle          Mode = "idle"
	ModePaused        Mode = "paused"
	ModeReflecting    Mode = "reflecting"
	ModeConsolidating Mode = "consolidating"
	ModeDreaming      Mode = "dreaming"
)

// State is what processes see: a read-only view of the mind plus service
// handles. Nil services mean the capability is not available yet.
type State struct {
	Mind      minds.Mind
	SelfModel minds.SelfModel
	Mode      Mode
	Cycle     int64
	CycleID   ids.ID
	Now       time.Time
	NewEvents []events.Event  // since the last cycle, ascending
	Workspace workspace.State // snapshot before this cycle
	Services  Services
	Goals     []goals.Goal       // active goals, for relevance scoring
	Motiv     motivation.State   // drives and affect at the start of the cycle
	Weights   map[string]float64 // effective attention weights after modulation
	llmCalls  atomic.Int32       // model calls made in this cycle; processes and handlers share the cap
}

// AllowLLM reserves one model call against the mind's per-cycle cap and
// reports whether it was granted, so a chatty cycle cannot exceed the budget.
func (s *State) AllowLLM() bool {
	cap := int32(s.Mind.Config.LLMCallsPerCycle)
	if cap <= 0 || s.Services.LLM == nil {
		return false
	}
	if s.llmCalls.Add(1) > cap {
		s.llmCalls.Add(-1)
		return false
	}
	return true
}

// Services are read handles processes may use. Extended per phase.
type Services struct {
	Events       EventReader
	Memory       *memory.Store
	Beliefs      *beliefs.Store
	LLM          *router.Router
	Prompts      *prompts.Store
	Goals        *goals.Store
	Motivation   *motivation.Store
	Predictions  *predictions.Store
	Capabilities *capabilities.Service
}

// EventReader is the subset of the event store processes may call.
type EventReader interface {
	Get(ctx context.Context, id ids.ID) (events.Event, error)
}

// Handler executes an admitted thought of a given kind. Handlers run
// sequentially after admission, may write to stores and emit events, and
// receive the cycle's emitter so provenance links are consistent.
type Handler func(ctx context.Context, c *Cycle, t workspace.Thought) error

// Hook runs after the executive handlers of a cycle, with the events emitted
// so far, for cross-cutting updates such as drives and affect.
type Hook interface {
	Name() string
	AfterCycle(ctx context.Context, c *Cycle, emitted []events.Event) error
}

// Registry holds processes, handlers and hooks. It is immutable after construction.
type Registry struct {
	processes []Process
	handlers  map[string]Handler
	hooks     []Hook
}

// NewRegistry builds a registry.
func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

// AddProcess registers a process.
func (r *Registry) AddProcess(p Process) *Registry { r.processes = append(r.processes, p); return r }

// Handle registers the handler for a thought kind.
func (r *Registry) Handle(kind string, h Handler) *Registry { r.handlers[kind] = h; return r }

// AddHook registers an after-cycle hook.
func (r *Registry) AddHook(h Hook) *Registry { r.hooks = append(r.hooks, h); return r }

// Processes returns the registered processes.
func (r *Registry) Processes() []Process { return r.processes }
