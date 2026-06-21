// Package beliefs manages propositions held with explicit, evidence-derived
// confidence. Confidence is arithmetic over evidence weights and source
// credibility; no model ever sets it directly (spec §10, §62).
package beliefs

import (
	"math"
	"sort"

	"github.com/andydixon/noema/pkg/concepts"
)

// Status of a belief.
type Status string

// Statuses in rough order of commitment.
const (
	StatusHypothesis Status = "hypothesis"
	StatusTentative  Status = "tentative"
	StatusLikely     Status = "likely"
	StatusAccepted   Status = "accepted"
	StatusContested  Status = "contested"
	StatusRejected   Status = "rejected"
	StatusSuperseded Status = "superseded"
)

// Valid reports whether s is known.
func (s Status) Valid() bool {
	switch s {
	case StatusHypothesis, StatusTentative, StatusLikely, StatusAccepted, StatusContested, StatusRejected, StatusSuperseded:
		return true
	}
	return false
}

// Slope converts net evidence into log-odds. 1.5 means one unit of fully
// credible evidence moves 0.5 to about 0.82.
const Slope = 1.5

// Bounds keep beliefs away from certainty unless an operator pins a fact.
const (
	MinConfidence = 0.02
	MaxConfidence = 0.98
)

// PriorFor returns the starting confidence for an epistemic kind.
func PriorFor(e concepts.Epistemic) float64 {
	switch e {
	case concepts.Hypothesis, concepts.Inference:
		return 0.25
	case concepts.Assumption, concepts.Opinion:
		return 0.4
	case concepts.Claim:
		return 0.5
	case concepts.Observation, concepts.Memory:
		return 0.6
	case concepts.Fact:
		return 0.8
	default:
		return 0.5
	}
}

// Evidence is one piece of support or opposition.
type Evidence struct {
	Direction   string  `json:"direction"` // for | against
	Kind        string  `json:"kind"`
	RefID       string  `json:"ref_id"`
	Source      string  `json:"source"`
	Weight      float64 `json:"weight"`      // how strongly it bears on the proposition, (0,1]
	Credibility float64 `json:"credibility"` // trust in the source, [0,1]
	Retracted   bool    `json:"retracted"`
}

// Breakdown explains a confidence value.
type Breakdown struct {
	Prior          float64  `json:"prior"`
	For            float64  `json:"for"`             // Σ weight×credibility, supporting
	Against        float64  `json:"against"`         // Σ weight×credibility, opposing
	SourcesFor     int      `json:"sources_for"`     // distinct sources supporting
	DiversityScale float64  `json:"diversity_scale"` // discount when support comes from one source
	LogOdds        float64  `json:"log_odds"`
	Confidence     float64  `json:"confidence"`
	Formula        string   `json:"formula"`
	Notes          []string `json:"notes,omitempty"`
}

// Compute derives confidence from prior and evidence. Support from a single
// source is discounted (T4: poisoned memory) so repetition alone cannot make
// a belief strong.
func Compute(prior float64, ev []Evidence) Breakdown {
	b := Breakdown{Prior: clampP(prior), Formula: "confidence = sigmoid(logit(prior) + slope × (for × diversity − against))"}
	sources := map[string]struct{}{}
	for _, e := range ev {
		if e.Retracted || e.Weight <= 0 {
			continue
		}
		v := clamp01(e.Weight) * clamp01(e.Credibility)
		if e.Direction == "against" {
			b.Against += v
			continue
		}
		b.For += v
		key := e.Source
		if key == "" {
			key = e.Kind + ":" + e.RefID
		}
		sources[key] = struct{}{}
	}
	b.SourcesFor = len(sources)
	// One source: 60% of face value; three or more independent sources: full.
	b.DiversityScale = 1
	if b.SourcesFor > 0 {
		b.DiversityScale = round4(0.6 + 0.4*math.Min(1, float64(b.SourcesFor-1)/2))
	}
	if b.SourcesFor == 1 && b.For > 0 {
		b.Notes = append(b.Notes, "all support comes from a single source")
	}
	logit := math.Log(b.Prior / (1 - b.Prior))
	b.LogOdds = logit + Slope*(b.For*b.DiversityScale-b.Against)
	b.Confidence = clampP(1 / (1 + math.Exp(-b.LogOdds)))
	b.For, b.Against, b.LogOdds, b.Confidence = round4(b.For), round4(b.Against), round4(b.LogOdds), round4(b.Confidence)
	return b
}

// Derive maps a breakdown to a status. Contested requires real evidence on
// both sides; rejected requires the evidence to have pushed confidence low.
func Derive(b Breakdown, epistemic concepts.Epistemic) Status {
	if b.For > 0 && b.Against > 0 && b.Against >= 0.5*b.For*b.DiversityScale && b.Confidence >= 0.2 && b.Confidence <= 0.8 {
		return StatusContested
	}
	switch {
	case b.Confidence < 0.15:
		return StatusRejected
	case b.For == 0 && epistemic == concepts.Hypothesis:
		return StatusHypothesis
	case b.Confidence < 0.5:
		if epistemic == concepts.Hypothesis {
			return StatusHypothesis
		}
		return StatusTentative
	case b.Confidence < 0.7:
		return StatusTentative
	case b.Confidence < 0.85:
		return StatusLikely
	default:
		return StatusAccepted
	}
}

func clampP(v float64) float64 {
	if math.IsNaN(v) || v < MinConfidence {
		return MinConfidence
	}
	if v > MaxConfidence {
		return MaxConfidence
	}
	return v
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// negations are the words that flip a proposition's polarity for the
// deterministic contradiction check.
var negations = map[string]bool{"not": true, "no": true, "never": true, "isn't": true, "isnt": true, "doesn't": true, "doesnt": true, "don't": true, "dont": true, "cannot": true, "can't": true, "cant": true, "wasn't": true, "didn't": true, "won't": true, "unlikely": true, "false": true}

// Polarity splits a proposition into its content tokens and whether it is negated.
func Polarity(text string) (tokens []string, negated bool) {
	for _, w := range tokenise(text) {
		if negations[w] {
			negated = !negated
			continue
		}
		tokens = append(tokens, w)
	}
	sort.Strings(tokens)
	return tokens, negated
}

// Jaccard similarity of two token lists.
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, w := range a {
		set[w] = true
	}
	inter := 0
	seenB := map[string]bool{}
	for _, w := range b {
		if seenB[w] {
			continue
		}
		seenB[w] = true
		if set[w] {
			inter++
		}
	}
	union := len(set) + len(seenB) - inter
	return float64(inter) / float64(union)
}

// Contradicts reports whether two propositions are the same content with
// opposite polarity (deterministic first pass; paraphrase judgement can be
// added by an LLM-assisted process later).
func Contradicts(a, b string) (bool, float64) {
	ta, na := Polarity(a)
	tb, nb := Polarity(b)
	sim := Jaccard(ta, tb)
	if na != nb && sim >= 0.6 {
		return true, round4(sim)
	}
	return false, round4(sim)
}

var stop = map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true, "be": true, "been": true, "of": true, "to": true, "in": true, "on": true, "at": true, "for": true, "and": true, "or": true, "that": true, "this": true, "it": true, "has": true, "have": true, "had": true, "with": true, "by": true, "as": true, "may": true, "might": true, "probably": true, "likely": true}

func tokenise(text string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) >= 2 {
			w := string(cur)
			if !stop[w] {
				out = append(out, w)
			}
		}
		cur = cur[:0]
	}
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z':
			cur = append(cur, r+32)
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '\'' || r == '-' || r == '_' || r == '.':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	return out
}
