package minds

import (
	"fmt"
	"strings"
	"time"
)

// CognitiveConfig holds per-mind tunables consumed by the brain. Defaults are
// conservative; every field is exposed in the UI in later phases.
type CognitiveConfig struct {
	WorkspaceCapacity     int                `json:"workspace_capacity"`
	WorkingMemoryCapacity int                `json:"working_memory_capacity"`
	AttentionWeights      map[string]float64 `json:"attention_weights"`
	CycleMinInterval      Duration           `json:"cycle_min_interval"`
	IdleCycleInterval     Duration           `json:"idle_cycle_interval"`
	MaxCyclesPerMinute    int                `json:"max_cycles_per_minute"`
	LLMCallsPerCycle      int                `json:"llm_calls_per_cycle"`
	DailyTokenBudget      int64              `json:"daily_token_budget"`
	WorkingMemoryDecay    float64            `json:"working_memory_decay"` // activation multiplier per cycle
	LoopDetectWindow      int                `json:"loop_detect_window"`
	LoopDetectThreshold   int                `json:"loop_detect_threshold"`

	// Background cognition (spec §9, §18, §39). Each subsystem has a switch,
	// a schedule and a model-call budget so idle minds cannot spend freely.
	ConsolidationEnabled     bool     `json:"consolidation_enabled"`
	ConsolidationInterval    Duration `json:"consolidation_interval"`
	ConsolidationMinEpisodes int      `json:"consolidation_min_episodes"`
	ConsolidationLLMCalls    int      `json:"consolidation_llm_calls"`
	ReflectionEnabled        bool     `json:"reflection_enabled"`
	ReflectionInterval       Duration `json:"reflection_interval"`
	ReflectionLLMCalls       int      `json:"reflection_llm_calls"`
	NarrativeLLM             bool     `json:"narrative_llm"`
	NotifyMinConfidence      float64  `json:"notify_min_confidence"`
	NotifyMinSalience        float64  `json:"notify_min_salience"`
	NotifyCooldown           Duration `json:"notify_cooldown"`

	// Dreaming (spec §19): off by default, confined to allowed hours, with its
	// own model-call and memory budgets. Dreams can never act.
	DreamingEnabled    bool     `json:"dreaming_enabled"`
	DreamHours         string   `json:"dream_hours"` // "HH-HH" local, e.g. "01-06"; empty = any hour
	DreamInterval      Duration `json:"dream_interval"`
	DreamLLMCalls      int      `json:"dream_llm_calls"`
	DreamMaxMemories   int      `json:"dream_max_memories"`
	DreamMaxHypotheses int      `json:"dream_max_hypotheses"`

	// LLMRoles overrides the daemon-wide model routing for this mind:
	// role → model id. It is what lets two forks of one snapshot run on
	// different providers while sharing everything else.
	LLMRoles map[string]string `json:"llm_roles,omitempty"`

	// Ablations disable named cognitive components in a research fork
	// (docs/cognitive-research.md). Only forks may carry them.
	Ablations []string `json:"ablations,omitempty"`
	// WorkspaceOverrides force candidates matching a text into or out of the
	// global workspace in a research fork, for broadcast experiments.
	WorkspaceOverrides []WorkspaceOverride `json:"workspace_overrides,omitempty"`
}

// WorkspaceOverride is one experimental manipulation of admission.
type WorkspaceOverride struct {
	Match string `json:"match"` // case-insensitive text in the candidate summary or its triggering event
	Fate  string `json:"fate"`  // admit | exclude
}

// Ablations is the closed vocabulary of components a research fork may
// disable, with what disabling means operationally.
var Ablations = map[string]string{
	"attention":        "every attention feature weight is 1 and drives do not modulate weights: admission no longer prioritises by learned weighting",
	"memory_retrieval": "the retrieval process proposes no recalled memories",
	"affect":           "drives and affect are neither updated by events nor used to modulate attention",
	"metacognition":    "no self-checks are proposed",
	"commitments":      "commitments do not shape formation, reconsideration or metacognition",
	"volition":         "no wants are derived and no intentions are formed",
	"global_workspace": "nothing persists in the workspace or working memory between cycles and nothing is broadcast; each admitted thought is handled in isolation",
	"curiosity":        "the curiosity process proposes no questions",
	"association":      "the association process proposes no hypotheses",
	"goals":            "the goal monitor raises no stalled or relevant goals",
	"narrative":        "the narrative is not updated",
	"notifications":    "no notifications are raised",
}

// ResearchSettings reports whether the configuration carries research
// manipulations that only forks may have.
func (c CognitiveConfig) ResearchSettings() bool {
	return len(c.Ablations) > 0 || len(c.WorkspaceOverrides) > 0
}

// ValidateResearch checks ablation names and override fates.
func (c CognitiveConfig) ValidateResearch() error {
	for _, a := range c.Ablations {
		if _, ok := Ablations[a]; !ok {
			return fmt.Errorf("unknown ablation %q", a)
		}
	}
	for _, o := range c.WorkspaceOverrides {
		if strings.TrimSpace(o.Match) == "" || len(o.Match) > 200 || (o.Fate != "admit" && o.Fate != "exclude") {
			return fmt.Errorf("workspace override needs a match (≤200 chars) and fate admit or exclude")
		}
	}
	return nil
}

// Ablated reports whether a component is disabled in this configuration.
func (c CognitiveConfig) Ablated(component string) bool {
	for _, a := range c.Ablations {
		if a == component {
			return true
		}
	}
	return false
}

// AttentionFeatures is the fixed set of scoring features (spec §6). Weights are
// per mind; features are computed deterministically by the attention package.
var AttentionFeatures = []string{"salience", "urgency", "novelty", "affect", "goal_relevance", "consequence", "confidence", "contradiction", "operator_priority", "context_relevance"}

// DefaultConfig returns the baseline cognitive configuration.
func DefaultConfig() CognitiveConfig {
	w := map[string]float64{"salience": 1.0, "urgency": 1.2, "novelty": 0.8, "affect": 0.6, "goal_relevance": 1.1,
		"consequence": 0.9, "confidence": 0.4, "contradiction": 1.0, "operator_priority": 1.5, "context_relevance": 0.9}
	return CognitiveConfig{WorkspaceCapacity: 7, WorkingMemoryCapacity: 9, AttentionWeights: w,
		CycleMinInterval: Duration(500 * time.Millisecond), IdleCycleInterval: Duration(60 * time.Second), MaxCyclesPerMinute: 60,
		LLMCallsPerCycle: 3, DailyTokenBudget: 500_000, WorkingMemoryDecay: 0.85, LoopDetectWindow: 20, LoopDetectThreshold: 5,
		ConsolidationEnabled: true, ConsolidationInterval: Duration(6 * time.Hour), ConsolidationMinEpisodes: 3, ConsolidationLLMCalls: 5,
		ReflectionEnabled: true, ReflectionInterval: Duration(12 * time.Hour), ReflectionLLMCalls: 2, NarrativeLLM: false,
		NotifyMinConfidence: 0.85, NotifyMinSalience: 0.7, NotifyCooldown: Duration(time.Hour),
		DreamingEnabled: false, DreamHours: "01-06", DreamInterval: Duration(24 * time.Hour), DreamLLMCalls: 2, DreamMaxMemories: 40, DreamMaxHypotheses: 5}
}

// Normalise fills gaps from defaults and clamps ranges.
func (c *CognitiveConfig) Normalise() {
	d := DefaultConfig()
	if c.WorkspaceCapacity < 1 || c.WorkspaceCapacity > 32 {
		c.WorkspaceCapacity = d.WorkspaceCapacity
	}
	if c.WorkingMemoryCapacity < 1 || c.WorkingMemoryCapacity > 64 {
		c.WorkingMemoryCapacity = d.WorkingMemoryCapacity
	}
	if c.AttentionWeights == nil {
		c.AttentionWeights = map[string]float64{}
	}
	for _, f := range AttentionFeatures {
		v, ok := c.AttentionWeights[f]
		if !ok || v < 0 || v > 5 {
			c.AttentionWeights[f] = d.AttentionWeights[f]
		}
	}
	for k := range c.AttentionWeights {
		if !contains(AttentionFeatures, k) {
			delete(c.AttentionWeights, k)
		}
	}
	if c.CycleMinInterval <= 0 {
		c.CycleMinInterval = d.CycleMinInterval
	}
	if c.IdleCycleInterval <= 0 {
		c.IdleCycleInterval = d.IdleCycleInterval
	}
	if c.MaxCyclesPerMinute < 1 || c.MaxCyclesPerMinute > 600 {
		c.MaxCyclesPerMinute = d.MaxCyclesPerMinute
	}
	if c.LLMCallsPerCycle < 0 || c.LLMCallsPerCycle > 20 {
		c.LLMCallsPerCycle = d.LLMCallsPerCycle
	}
	if c.DailyTokenBudget <= 0 {
		c.DailyTokenBudget = d.DailyTokenBudget
	}
	if c.WorkingMemoryDecay <= 0 || c.WorkingMemoryDecay >= 1 {
		c.WorkingMemoryDecay = d.WorkingMemoryDecay
	}
	if c.LoopDetectWindow < 5 {
		c.LoopDetectWindow = d.LoopDetectWindow
	}
	if c.LoopDetectThreshold < 2 {
		c.LoopDetectThreshold = d.LoopDetectThreshold
	}
	if c.ConsolidationInterval < Duration(10*time.Minute) {
		c.ConsolidationInterval = d.ConsolidationInterval
	}
	if c.ConsolidationMinEpisodes < 2 {
		c.ConsolidationMinEpisodes = d.ConsolidationMinEpisodes
	}
	if c.ConsolidationLLMCalls < 0 || c.ConsolidationLLMCalls > 50 {
		c.ConsolidationLLMCalls = d.ConsolidationLLMCalls
	}
	if c.DreamInterval < Duration(30*time.Minute) {
		c.DreamInterval = d.DreamInterval
	}
	if c.DreamLLMCalls < 0 || c.DreamLLMCalls > 20 {
		c.DreamLLMCalls = d.DreamLLMCalls
	}
	if c.DreamMaxMemories < 4 || c.DreamMaxMemories > 200 {
		c.DreamMaxMemories = d.DreamMaxMemories
	}
	if c.DreamMaxHypotheses < 1 || c.DreamMaxHypotheses > 20 {
		c.DreamMaxHypotheses = d.DreamMaxHypotheses
	}
	if len(c.LLMRoles) > 0 {
		clean := map[string]string{}
		for role, model := range c.LLMRoles {
			if validRole(role) && looksLikeID(model) {
				clean[role] = model
			}
		}
		c.LLMRoles = clean
	}
	if c.ReflectionInterval < Duration(10*time.Minute) {
		c.ReflectionInterval = d.ReflectionInterval
	}
	if c.ReflectionLLMCalls < 0 || c.ReflectionLLMCalls > 20 {
		c.ReflectionLLMCalls = d.ReflectionLLMCalls
	}
	if c.NotifyMinConfidence <= 0 || c.NotifyMinConfidence > 1 {
		c.NotifyMinConfidence = d.NotifyMinConfidence
	}
	if c.NotifyMinSalience <= 0 || c.NotifyMinSalience > 1 {
		c.NotifyMinSalience = d.NotifyMinSalience
	}
	if c.NotifyCooldown < Duration(time.Minute) {
		c.NotifyCooldown = d.NotifyCooldown
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Duration marshals as a Go duration string in JSON ("500ms") for readability.
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", time.Duration(d).String())), nil
}

// UnmarshalJSON accepts a duration string or a number of nanoseconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		v, err := time.ParseDuration(string(b[1 : len(b)-1]))
		if err != nil {
			return err
		}
		*d = Duration(v)
		return nil
	}
	var n int64
	if _, err := fmt.Sscan(string(b), &n); err != nil {
		return err
	}
	*d = Duration(n)
	return nil
}

// D returns the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// validRole accepts the model roles a mind may override.
func validRole(r string) bool {
	switch r {
	case "perceive", "respond", "hypothesise", "summarise", "judge", "narrate", "embed":
		return true
	}
	return false
}

// looksLikeID accepts a UUID-shaped model id; anything else is dropped.
func looksLikeID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, ch := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if ch != '-' {
				return false
			}
		case (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'):
		default:
			return false
		}
	}
	return true
}
