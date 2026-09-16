package cognition

import (
	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/epistemics"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/narrative"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/internal/research"
)

// Stores bundles what the default processes, handlers and hooks write to.
// A nil store disables the parts that depend on it.
type Stores struct {
	Memory        *memory.Store
	Beliefs       *beliefs.Store
	Conversations *conversation.Store
	Graph         *graph.Store
	Assertions    *epistemics.Store
	Motivation    *motivation.Store
	Narrative     *narrative.Store
	Notifications *notifications.Store
	Agency        *agency.Service
	Research      *research.Store
}

// NewRegistry assembles the cognitive registry. The daemon, the test harness
// and sandbox runtimes all call it, so they run the same cognition and an
// ablation names the same components everywhere.
func NewRegistry(s Stores) *brain.Registry {
	r := brain.NewRegistry().AddProcess(Attend{}).AddProcess(Retrieve{}).AddProcess(Associate{}).AddProcess(Perceive{}).
		AddProcess(GoalMonitor{}).AddProcess(Curiosity{}).AddProcess(Metacognition{}).AddProcess(Act{}).AddProcess(Intend{})
	if s.Motivation != nil {
		r.AddHook(MotivationHook{Store: s.Motivation})
	}
	if s.Narrative != nil {
		r.AddHook(NarrativeHook{Store: s.Narrative})
	}
	if s.Notifications != nil {
		r.AddHook(NotifyHook{Store: s.Notifications})
	}
	r.AddHook(VolitionHook{})
	Handlers{Memory: s.Memory, Beliefs: s.Beliefs, Conversations: s.Conversations, Graph: s.Graph, Assertions: s.Assertions, Agency: s.Agency, Research: s.Research}.Register(r)
	return r
}
