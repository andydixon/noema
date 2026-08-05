// Package epistemics keeps apart three things a careless system conflates:
// the observation that somebody said something, the assertion they made,
// and any belief about whether what they said is true.
//
// An Assertion is the durable, append-only record that an actor took a
// stance on a proposition. Its confidence is about interpretation ("how sure
// are we that Andy asserted X?"), never about X. Whether X is believed is a
// separate question answered by the belief layer from evidence, of which the
// assertion is one row with a weight that depends on stance, modality and
// how clearly it was understood. Reported speech attributes the stance to
// the person reported, hypotheticals create no belief at all, doubt counts
// against, and a possibly sarcastic remark is recorded with low
// interpretation confidence rather than guessed at.
package epistemics

import (
	"regexp"
	"strings"
)

// Stances an actor can take towards a proposition.
const (
	StanceAsserts   = "asserts"
	StanceDenies    = "denies"
	StanceDoubts    = "doubts"
	StanceQuestions = "questions"
	StanceSupposes  = "supposes"
	StanceReports   = "reports" // the speaker relays somebody else's stance
)

// Certainty the speaker expressed.
const (
	CertaintyAsserted  = "asserted"
	CertaintyHedged    = "hedged"
	CertaintyUncertain = "uncertain"
	CertaintyEmphatic  = "emphatic"
)

// Modality of the utterance.
const (
	ModalityFactual      = "factual"
	ModalityHypothetical = "hypothetical"
	ModalityReported     = "reported"
	ModalityQuoted       = "quoted"
	ModalityImperative   = "imperative"
	ModalityQuestion     = "question"
)

// Interpretation is the deterministic reading of one claim sentence.
type Interpretation struct {
	Proposition   string   `json:"proposition"`    // the proposition as attributed (reporting frame removed)
	AttributedTo  string   `json:"attributed_to"`  // whose stance this is
	Stance        string   `json:"stance"`         // stance of AttributedTo towards Proposition
	SpeakerStance string   `json:"speaker_stance"` // stance of the speaker when they report somebody else ("" = same)
	Certainty     string   `json:"certainty"`
	Modality      string   `json:"modality"`
	Negated       bool     `json:"negated"`
	Reported      bool     `json:"reported"`
	ReportedActor string   `json:"reported_actor,omitempty"`
	Quoted        bool     `json:"quoted"`
	Hypothetical  bool     `json:"hypothetical"`
	Confidence    float64  `json:"interpretation_confidence"` // how sure the reading itself is
	Notes         []string `json:"notes,omitempty"`
}

var (
	reportVerbs   = `(?:says?|said|saying|thinks?|thought|thinking|believes?|believed|claims?|claimed|claiming|told (?:me|us)|telling (?:me|us)|reckons?|insists?|insisting|mentioned|reported|argues?|arguing|swears?|maintains?)`
	reReported    = regexp.MustCompile(`(?i)^(?:apparently[, ]+|well[, ]+)?([A-Z][\w'.-]*(?: [A-Z][\w'.-]*)?)\s+` + reportVerbs + `\s+(?:that\s+)?(.+)$`)
	reQuoted      = regexp.MustCompile(`(?i)^([A-Z][\w'.-]*(?: [A-Z][\w'.-]*)?)\s+` + reportVerbs + `[:,]?\s*["“](.+?)["”]\.?$`)
	reDiffuse     = regexp.MustCompile(`(?i)^(?:people|everyone|everybody|they|folks|some people|others|the team|the internet)\s+(?:keep |keeps |always |all )?` + reportVerbs + `\s+(?:that\s+)?(.+)$`)
	reHypothet    = regexp.MustCompile(`(?i)^(?:imagine|suppose|supposing|assume|assuming|let'?s say|what if|hypothetically|pretend|say)\b[,:]?\s*(?:that\s+)?(.+)$`)
	reConditional = regexp.MustCompile(`(?i)^if\s+(.+?),\s*(?:then\s+)?(.+)$`)
	reDisagree    = regexp.MustCompile(`(?i)^(.+?)[,;]?\s+(?:but|though|although)\s+(?:i|we)\s+(?:don'?t|do not|didn'?t|can'?t|cannot|never)\s+(?:believe|buy|trust|think so|agree)(?:\s+(?:her|him|them|it|that|this))?\.?$`)
	reDoubtSuffix = regexp.MustCompile(`(?i)^(.+?)[,;]?\s+(?:which|and that|but that)\s+(?:is|was|seems)\s+(?:nonsense|rubbish|wrong|false|unlikely|doubtful)\.?$`)
	reNegation    = regexp.MustCompile(`(?i)\b(?:not|no|never|isn't|isnt|aren't|wasn't|weren't|doesn't|don't|didn't|cannot|can't|won't|wouldn't|shouldn't|nobody|nothing|none)\b`)
	reEmphatic    = regexp.MustCompile(`(?i)\b(?:definitely|certainly|absolutely|without doubt|i'?m (?:sure|certain)|for sure|no doubt|clearly)\b`)
	reHedge       = regexp.MustCompile(`(?i)\b(?:i think|i believe|maybe|perhaps|possibly|might|probably|not sure|i guess|seems|i suspect|apparently|likely|sort of|kind of)\b`)
	reSarcasm     = regexp.MustCompile(`(?i)^(?:oh (?:yes|yeah|sure|right)|yeah,? right|sure,? and|of course|great,)\b|\b(?:absolute|total|utter|complete)\s+(?:genius|hero|legend|star|expert|joy|delight)\b|\b(?:as always|as usual|what a surprise|shock horror)\b`)
	reImperative  = regexp.MustCompile(`(?i)^(?:please\s+)?(?:do|make|run|go|check|tell|show|remember|note|delete|fetch|read|stop|start|send|install)\b`)
)

// Interpret reads one claim sentence spoken by speaker.
func Interpret(speaker, sentence string) Interpretation {
	s := strings.TrimSpace(sentence)
	s = strings.TrimRight(s, " .!")
	in := Interpretation{Proposition: s, AttributedTo: speaker, Stance: StanceAsserts, Certainty: CertaintyAsserted, Modality: ModalityFactual, Confidence: 0.85}
	lower := strings.ToLower(s)
	if strings.HasSuffix(sentence, "?") {
		in.Stance, in.Modality, in.Confidence = StanceQuestions, ModalityQuestion, 0.9
		return in
	}
	if reImperative.MatchString(s) {
		in.Modality, in.Stance, in.Confidence = ModalityImperative, StanceSupposes, 0.7
		in.Notes = append(in.Notes, "imperative: a request, not a claim about the world")
		return in
	}
	// Hypotheticals and conditionals create no commitment.
	if m := reHypothet.FindStringSubmatch(s); m != nil {
		in.Proposition, in.Hypothetical, in.Modality, in.Stance, in.Confidence = strings.TrimSpace(m[1]), true, ModalityHypothetical, StanceSupposes, 0.85
		in.Notes = append(in.Notes, "hypothetical framing; no commitment to the proposition")
		in.Negated = reNegation.MatchString(in.Proposition)
		return in
	}
	if m := reConditional.FindStringSubmatch(s); m != nil {
		in.Hypothetical, in.Modality, in.Stance, in.Confidence = true, ModalityHypothetical, StanceSupposes, 0.7
		in.Notes = append(in.Notes, "conditional; the consequent is asserted only under the antecedent")
		return in
	}
	// Disagreement with reported speech: "Sarah says X, but I don't believe her".
	if m := reDisagree.FindStringSubmatch(s); m != nil {
		inner := Interpret(speaker, m[1])
		inner.SpeakerStance = StanceDoubts
		inner.Notes = append(inner.Notes, "speaker distances themselves from the reported claim")
		if !inner.Reported {
			// "X, but I don't believe it": the speaker relays a claim without an author.
			inner.Reported, inner.ReportedActor, inner.AttributedTo, inner.Modality = true, "unspecified", "unspecified", ModalityReported
			inner.Confidence = minf(inner.Confidence, 0.6)
		}
		return inner
	}
	if m := reDoubtSuffix.FindStringSubmatch(s); m != nil {
		inner := Interpret(speaker, m[1])
		inner.SpeakerStance = StanceDoubts
		inner.Notes = append(inner.Notes, "speaker rejects the claim")
		return inner
	}
	// Quoted and reported speech attribute the stance to the person quoted.
	if m := reQuoted.FindStringSubmatch(s); m != nil {
		in.ReportedActor, in.Proposition = m[1], strings.TrimSpace(m[2])
		in.Reported, in.Quoted, in.Modality, in.AttributedTo, in.Confidence = true, true, ModalityQuoted, m[1], 0.8
		in.SpeakerStance = StanceReports
		in.Negated = reNegation.MatchString(in.Proposition)
		in.Certainty = certaintyOf(in.Proposition)
		return in
	}
	if m := reDiffuse.FindStringSubmatch(s); m != nil {
		in.ReportedActor, in.Proposition = "unspecified people", strings.TrimSpace(m[1])
		in.Reported, in.Modality, in.AttributedTo, in.Confidence = true, ModalityReported, "unspecified people", 0.6
		in.SpeakerStance, in.Certainty = StanceReports, CertaintyUncertain
		in.Notes = append(in.Notes, "diffuse attribution; nobody in particular stands behind it")
		in.Negated = reNegation.MatchString(in.Proposition)
		return in
	}
	if m := reReported.FindStringSubmatch(s); m != nil && !strings.EqualFold(m[1], "I") {
		in.ReportedActor, in.Proposition = m[1], strings.TrimSpace(m[2])
		in.Reported, in.Modality, in.AttributedTo, in.Confidence = true, ModalityReported, m[1], 0.75
		in.SpeakerStance = StanceReports
		in.Negated = reNegation.MatchString(in.Proposition)
		in.Certainty = certaintyOf(in.Proposition)
		if strings.Contains(strings.ToLower(m[0]), "think") || strings.Contains(strings.ToLower(m[0]), "believe") {
			in.Notes = append(in.Notes, "reports a belief, not an observation")
		}
		return in
	}
	// Plain statement by the speaker.
	in.Negated = reNegation.MatchString(lower)
	in.Certainty = certaintyOf(s)
	if reSarcasm.MatchString(s) {
		in.Confidence = 0.4
		in.Notes = append(in.Notes, "possible sarcasm or irony; literal reading uncertain")
	}
	if in.Negated && in.Stance == StanceAsserts {
		in.Notes = append(in.Notes, "negated proposition asserted as stated")
	}
	return in
}

func certaintyOf(s string) string {
	switch {
	case reEmphatic.MatchString(s):
		return CertaintyEmphatic
	case reHedge.MatchString(s):
		return CertaintyHedged
	}
	return CertaintyAsserted
}

// CreatesBelief reports whether an interpretation should feed a proposition
// belief at all. Questions, imperatives and hypotheticals never do.
func (in Interpretation) CreatesBelief() bool {
	switch in.Modality {
	case ModalityQuestion, ModalityImperative, ModalityHypothetical:
		return false
	}
	return in.Stance == StanceAsserts && strings.TrimSpace(in.Proposition) != ""
}

// EvidenceWeight is the weight the assertion contributes as evidence for
// its proposition, before source credibility: how firmly the attributed
// actor stood behind it, discounted by how surely the reading itself holds.
func (in Interpretation) EvidenceWeight() float64 {
	w := 0.5
	switch in.Certainty {
	case CertaintyEmphatic:
		w = 0.55
	case CertaintyHedged:
		w = 0.3
	case CertaintyUncertain:
		w = 0.2
	}
	if in.Reported {
		w *= 0.5 // second-hand: the mind did not hear the attributed actor
	}
	return round(w * in.Confidence)
}

// DoubtWeight is the counter-weight the speaker's expressed doubt adds.
func (in Interpretation) DoubtWeight() float64 {
	if in.SpeakerStance != StanceDoubts {
		return 0
	}
	return round(0.3 * in.Confidence)
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func round(v float64) float64 { return float64(int(v*1000+0.5)) / 1000 }
