package minds

import (
	"fmt"
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
		LLMCallsPerCycle: 3, DailyTokenBudget: 500_000, WorkingMemoryDecay: 0.85, LoopDetectWindow: 20, LoopDetectThreshold: 5}
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
