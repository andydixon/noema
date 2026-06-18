// Package attention scores thought candidates deterministically so that the
// operator can always see why one thought won access to the workspace over
// another (ADR-0001, spec §6). There is no model call in this package.
package attention

import (
	"math"
	"sort"
)

// Features are the inputs to scoring, each in [0,1]. Processes fill what they
// know; unknown features stay 0 and therefore neither help nor hurt.
type Features struct {
	Salience         float64 `json:"salience"`
	Urgency          float64 `json:"urgency"`
	Novelty          float64 `json:"novelty"`
	Affect           float64 `json:"affect"` // emotional significance
	GoalRelevance    float64 `json:"goal_relevance"`
	Consequence      float64 `json:"consequence"` // predicted importance of acting
	Confidence       float64 `json:"confidence"`
	Contradiction    float64 `json:"contradiction"` // unresolved conflict
	OperatorPriority float64 `json:"operator_priority"`
	ContextRelevance float64 `json:"context_relevance"`
}

// Names lists feature keys in a stable order; must match minds.AttentionFeatures.
var Names = []string{"salience", "urgency", "novelty", "affect", "goal_relevance", "consequence", "confidence", "contradiction", "operator_priority", "context_relevance"}

// Map returns the features keyed by name.
func (f Features) Map() map[string]float64 {
	return map[string]float64{"salience": f.Salience, "urgency": f.Urgency, "novelty": f.Novelty, "affect": f.Affect, "goal_relevance": f.GoalRelevance,
		"consequence": f.Consequence, "confidence": f.Confidence, "contradiction": f.Contradiction, "operator_priority": f.OperatorPriority, "context_relevance": f.ContextRelevance}
}

// Clamp forces every feature into [0,1].
func (f Features) Clamp() Features {
	c := func(v float64) float64 {
		if math.IsNaN(v) || v < 0 {
			return 0
		}
		if v > 1 {
			return 1
		}
		return v
	}
	return Features{c(f.Salience), c(f.Urgency), c(f.Novelty), c(f.Affect), c(f.GoalRelevance), c(f.Consequence), c(f.Confidence), c(f.Contradiction), c(f.OperatorPriority), c(f.ContextRelevance)}
}

// Term is one weighted contribution, kept so the UI can show the arithmetic.
type Term struct {
	Feature      string  `json:"feature"`
	Value        float64 `json:"value"`
	Weight       float64 `json:"weight"`
	Contribution float64 `json:"contribution"` // value*weight / sum(weights)
}

// Breakdown is a scored candidate's explanation.
type Breakdown struct {
	Total float64 `json:"total"` // in [0,1]
	Terms []Term  `json:"terms"`
}

// Score computes the normalised weighted sum. Missing weights count as 0, so
// a mind can switch a feature off entirely. The result is rounded to 4 places
// to keep replays byte-identical across platforms.
func Score(f Features, weights map[string]float64) Breakdown {
	f = f.Clamp()
	vals := f.Map()
	var sum float64
	for _, n := range Names {
		if w := weights[n]; w > 0 {
			sum += w
		}
	}
	b := Breakdown{Terms: make([]Term, 0, len(Names))}
	if sum == 0 {
		return b
	}
	for _, n := range Names {
		w := weights[n]
		if w < 0 {
			w = 0
		}
		c := round4(vals[n] * w / sum)
		b.Terms = append(b.Terms, Term{Feature: n, Value: round4(vals[n]), Weight: w, Contribution: c})
		b.Total += c
	}
	b.Total = round4(b.Total)
	return b
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// Scored pairs an identifier with its breakdown for ranking.
type Scored struct {
	ID        string
	Breakdown Breakdown
}

// Rank sorts by total descending, breaking ties by ID so ordering is total and
// reproducible regardless of the order processes returned candidates.
func Rank(items []Scored) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Breakdown.Total != items[j].Breakdown.Total {
			return items[i].Breakdown.Total > items[j].Breakdown.Total
		}
		return items[i].ID < items[j].ID
	})
}
