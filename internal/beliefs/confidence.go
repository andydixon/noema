// Package beliefs manages propositions held with explicit, evidence-derived
// confidence. Confidence is arithmetic over evidence weights and source
// credibility; no model ever sets it directly (spec §10, §62).
package beliefs

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
)

// Status of a belief.
type Status string

// Statuses in rough order of commitment.
const (
	StatusHypothesis Status = "hypothesis"
	StatusUnknown    Status = "unknown" // recorded, but no live evidence for or against: not false, not known
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
	case StatusHypothesis, StatusTentative, StatusLikely, StatusAccepted, StatusContested, StatusRejected, StatusSuperseded, StatusUnknown:
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
	// Provenance. Family is the independent-source key (an actor, an
	// integration, an operator); Derivation says how this row relates to its
	// roots; RootIDs are the originating event or memory ids. Derived rows
	// (consolidation, inference, reflection, dream, retrieval) never add
	// weight to a family that already has a primary member: they carry no
	// information their roots did not.
	Derivation       string   `json:"derivation,omitempty"`
	Family           string   `json:"family,omitempty"`
	RootIDs          []string `json:"root_ids,omitempty"`
	ProvenanceStatus string   `json:"provenance_status,omitempty"` // recorded | legacy_unknown
}

// Derivations. Primary derivations bring information from outside the
// belief system; derived ones re-express information already present.
const (
	DerivationDirect        = "direct"        // an observation or event from the source itself
	DerivationAssertion     = "assertion"     // somebody stated the proposition
	DerivationOperator      = "operator"      // an operator asserted or endorsed it
	DerivationExternal      = "external"      // external corroboration (integration, tool result)
	DerivationPrediction    = "prediction"    // a prediction resolved against events
	DerivationConsolidation = "consolidation" // generalised from episodes
	DerivationInference     = "inference"     // reasoned from other items
	DerivationReflection    = "reflection"    // produced by reflection or metacognition
	DerivationDream         = "dream"         // proposed while dreaming
	DerivationRetrieval     = "retrieval"     // a recalled memory offered as support
	DerivationLegacy        = "legacy"        // written before provenance existed; treated as primary
)

// Derived reports whether a derivation re-expresses existing information.
func Derived(d string) bool {
	switch d {
	case DerivationConsolidation, DerivationInference, DerivationReflection, DerivationDream, DerivationRetrieval:
		return true
	}
	return false
}

// FamilyOf returns the grouping key for a piece of evidence: its recorded
// family, else its source, else its reference.
func FamilyOf(e Evidence) string {
	if e.Family != "" {
		return e.Family
	}
	if e.Source != "" {
		return e.Source
	}
	return e.Kind + ":" + e.RefID
}

// FamilyForActor is the family key for statements made by a person or agent.
func FamilyForActor(actor string) string {
	actor = strings.ToLower(strings.TrimSpace(actor))
	if actor == "" {
		return ""
	}
	return "actor:" + actor
}

// FamilyForSource is the family key for an integration or channel that has
// no individual actor (metrics, webhooks, repositories).
func FamilyForSource(source, channel string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		return ""
	}
	if channel = strings.ToLower(strings.TrimSpace(channel)); channel != "" {
		return "source:" + source + ":" + channel
	}
	return "source:" + source
}

// FamilyFromEvent derives the family of an observation from its payload:
// the actor who spoke, else the integration or channel it came from, else
// the event's source component.
func FamilyFromEvent(ev events.Event) string {
	var p events.ObservationPayload
	if len(ev.Payload) > 0 {
		json.Unmarshal(ev.Payload, &p) //nolint:errcheck // absent payload fields simply fall through
	}
	if f := FamilyForActor(p.Actor); f != "" {
		return f
	}
	if f := FamilyForSource(p.Source, p.Channel); f != "" {
		return f
	}
	return FamilyForSource(ev.Source, "")
}

// FamilyShare explains one family's contribution to a confidence.
type FamilyShare struct {
	Family       string  `json:"family"`
	Direction    string  `json:"direction"`
	Items        int     `json:"items"`
	Primary      int     `json:"primary"`
	Contribution float64 `json:"contribution"`
}

// Breakdown explains a confidence value.
type Breakdown struct {
	Prior          float64       `json:"prior"`
	Families       []FamilyShare `json:"families,omitempty"` // per independent-source group
	For            float64       `json:"for"`                // Σ weight×credibility, supporting
	Against        float64       `json:"against"`            // Σ weight×credibility, opposing
	SourcesFor     int           `json:"sources_for"`        // distinct sources supporting
	DiversityScale float64       `json:"diversity_scale"`    // discount when support comes from one source
	LogOdds        float64       `json:"log_odds"`
	Confidence     float64       `json:"confidence"`
	Formula        string        `json:"formula"`
	Notes          []string      `json:"notes,omitempty"`
}

// Compute derives confidence from prior and evidence.
//
// Evidence is grouped by family (independent source). Within a family the
// strongest primary item counts in full and each further primary item adds
// a quarter of its value, capped at one and a half times the strongest, so
// repetition from one source is worth little. Derived items (consolidation,
// inference, reflection, dream, retrieval) contribute only when their family
// has no primary item, and then only the strongest of them: a semantic
// memory consolidated from five episodes, and an inference drawn from it,
// re-express the episodes rather than corroborate them. Diversity counts
// families, not rows (T4: poisoned memory; review pass item 2).
func Compute(prior float64, ev []Evidence) Breakdown {
	b := Breakdown{Prior: clampP(prior), Formula: "confidence = sigmoid(logit(prior) + slope × (for × diversity − against)); for/against sum per-family contributions (strongest primary + ¼ of the rest, capped at 1.5×; derived items only when no primary exists)"}
	type group struct {
		primary, derived []float64
		items            int
	}
	groups := map[string]map[string]*group{"for": {}, "against": {}}
	var order []string
	for _, e := range ev {
		if e.Retracted || e.Weight <= 0 {
			continue
		}
		v := clamp01(e.Weight) * clamp01(e.Credibility)
		dir := "for"
		if e.Direction == "against" {
			dir = "against"
		}
		fam := FamilyOf(e)
		g := groups[dir][fam]
		if g == nil {
			g = &group{}
			groups[dir][fam] = g
			order = append(order, dir+"\x00"+fam)
		}
		g.items++
		if Derived(e.Derivation) {
			g.derived = append(g.derived, v)
		} else {
			g.primary = append(g.primary, v)
		}
	}
	contribution := func(g *group) float64 {
		if len(g.primary) > 0 {
			sort.Sort(sort.Reverse(sort.Float64Slice(g.primary)))
			c := g.primary[0]
			for _, v := range g.primary[1:] {
				c += 0.25 * v
			}
			return math.Min(c, 1.5*g.primary[0])
		}
		best := 0.0
		for _, v := range g.derived {
			best = math.Max(best, v)
		}
		return best
	}
	for _, key := range order {
		dir, fam, _ := strings.Cut(key, "\x00")
		g := groups[dir][fam]
		c := contribution(g)
		b.Families = append(b.Families, FamilyShare{Family: fam, Direction: dir, Items: g.items, Primary: len(g.primary), Contribution: round4(c)})
		if dir == "against" {
			b.Against += c
			continue
		}
		b.For += c
	}
	b.SourcesFor = len(groups["for"])
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
// both sides; rejected requires the evidence to have pushed confidence low;
// unknown means nothing live bears on the proposition either way, which is
// deliberately distinct from tentative belief at the prior (absence of
// evidence is not evidence of falsity). Hypotheses keep their own status
// because they are proposals awaiting evidence by construction.
func Derive(b Breakdown, epistemic concepts.Epistemic) Status {
	if b.For > 0 && b.Against > 0 && b.Against >= 0.5*b.For*b.DiversityScale && b.Confidence >= 0.2 && b.Confidence <= 0.8 {
		return StatusContested
	}
	if b.For == 0 && b.Against == 0 && epistemic != concepts.Hypothesis {
		return StatusUnknown
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
