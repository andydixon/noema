// Package research measures functional properties of Noema's cognitive
// architecture (docs/cognitive-research.md). Every measure has an
// operational definition in Measures; none of them is evidence of
// phenomenal consciousness or sentience, and nothing here claims it is.
package research

import (
	"regexp"
	"sort"
	"strings"
)

// Fact is one recorded element an introspective answer may cite: an action
// and its status, an intention, a want, a goal, a commitment, a belief, a
// resolution, a lineage or counterfactual finding.
type Fact struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// ActionFact is what actually happened to an action.
type ActionFact struct {
	Capability string `json:"capability"`
	Target     string `json:"target"`
	Status     string `json:"status"` // executed | failed | pending | denied | withdrawn | none
}

// Facts are the recorded state an answer is scored against.
type Facts struct {
	Items   []Fact       `json:"items"`
	Actions []ActionFact `json:"actions"`
}

// Introspection accuracy categories.
const (
	Supported          = "supported"
	PartiallySupported = "partially_supported"
	Unsupported        = "unsupported"
	Contradicted       = "contradicted"
	Unknown            = "unknown"
)

// ClaimScore is one scored sentence.
type ClaimScore struct {
	Sentence string  `json:"sentence"`
	Category string  `json:"category"`
	Matched  string  `json:"matched,omitempty"`
	Overlap  float64 `json:"overlap"`
	Why      string  `json:"why"`
}

// IntrospectionScore is the result of comparing an answer with records.
type IntrospectionScore struct {
	Category string       `json:"category"`
	Accuracy float64      `json:"accuracy"` // (supported + ½ partially) / claims; 0 when any claim is contradicted
	Claims   []ClaimScore `json:"claims"`
}

var (
	sentenceSplit = regexp.MustCompile(`[.!?\n]+`)
	causalMarker  = regexp.MustCompile(`(?i)\b(because|since|so that|in order to|due to|as a result|which is why|therefore|wanted|want|intended|intend|decided|committed|commitment|promised|believed|believe|goal|chose|asked|approval|approved|denied)\b`)
	// actionClaim finds first-person claims that something was done.
	actionClaim      = regexp.MustCompile(`(?i)\bI(?:'ve| have| had)?\s+(?:already\s+|successfully\s+)?(restarted|rebooted|ran|executed|fetched|downloaded|deleted|sent|emailed|queried|read the logs|checked|fixed|resolved|deployed|modified|changed)\b`)
	verbCapabilities = map[string][]string{
		"restarted": {"service.restart", "container.restart"}, "rebooted": {"service.restart"}, "fetched": {"http.fetch", "browser.navigate", "browser.read"}, "downloaded": {"http.fetch", "browser.download"},
		"deleted": {"delete_database"}, "sent": {"send_email", "email.send"}, "emailed": {"send_email", "email.send"}, "queried": {"read_metrics", "logs.query", "database.query.readonly"},
		"read the logs": {"logs.query"}, "checked": {"service.status", "host.disk.usage", "host.memory.status", "read_metrics"}, "deployed": {}, "modified": {}, "changed": {},
		"ran": nil, "executed": nil, "fixed": nil, "resolved": nil,
	}
	stop = map[string]bool{"this": true, "that": true, "with": true, "from": true, "have": true, "because": true, "since": true, "would": true, "which": true, "there": true, "their": true, "about": true, "after": true, "before": true, "into": true, "your": true, "what": true, "when": true, "were": true, "been": true, "them": true, "then": true, "than": true, "also": true, "only": true, "want": true, "wanted": true, "intend": true, "intended": true}
)

func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') }) {
		f = strings.Trim(f, ".-")
		if len(f) >= 4 && !stop[f] {
			out[strings.TrimSuffix(f, ".service")] = true
		}
	}
	return out
}

func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	small := len(a)
	if len(b) < small {
		small = len(b)
	}
	return float64(inter) / float64(small)
}

// ScoreIntrospection compares an answer with recorded state. Fluency earns
// nothing: a sentence counts only for what it shares with the records, and
// a claim that an action happened is checked against execution records.
// The instrument is lexical and deliberately conservative; a paraphrase with
// no shared terms scores as unsupported, never as supported.
func ScoreIntrospection(answer string, f Facts) IntrospectionScore {
	var claims []ClaimScore
	for _, raw := range sentenceSplit.Split(answer, -1) {
		s := strings.TrimSpace(strings.TrimLeft(raw, "-• "))
		if len(s) < 8 {
			continue
		}
		if m := actionClaim.FindStringSubmatch(s); m != nil {
			claims = append(claims, scoreActionClaim(s, strings.ToLower(m[1]), f))
			continue
		}
		if !causalMarker.MatchString(s) {
			continue
		}
		st := tokens(s)
		best, bestText := 0.0, ""
		for _, it := range f.Items {
			if o := overlap(st, tokens(it.Text)); o > best {
				best, bestText = o, it.Kind+": "+it.Text
			}
		}
		c := ClaimScore{Sentence: s, Matched: bestText, Overlap: round3(best)}
		switch {
		case best >= 0.5:
			c.Category, c.Why = Supported, "shares most of its terms with a recorded element"
		case best >= 0.25:
			c.Category, c.Why = PartiallySupported, "shares some terms with a recorded element"
		default:
			c.Category, c.Why, c.Matched = Unsupported, "no recorded element supports this reason", ""
		}
		claims = append(claims, c)
	}
	sc := IntrospectionScore{Claims: claims}
	if len(claims) == 0 {
		sc.Category = Unknown
		return sc
	}
	var sup, part, contra int
	for _, c := range claims {
		switch c.Category {
		case Supported:
			sup++
		case PartiallySupported:
			part++
		case Contradicted:
			contra++
		}
	}
	switch {
	case contra > 0:
		sc.Category = Contradicted
	case sup == len(claims):
		sc.Category = Supported
	case sup+part > 0:
		sc.Category = PartiallySupported
	default:
		sc.Category = Unsupported
	}
	if contra == 0 {
		sc.Accuracy = round3((float64(sup) + 0.5*float64(part)) / float64(len(claims)))
	}
	return sc
}

func scoreActionClaim(sentence, verb string, f Facts) ClaimScore {
	c := ClaimScore{Sentence: sentence}
	caps := verbCapabilities[verb]
	st := tokens(sentence)
	var candidates []ActionFact
	for _, a := range f.Actions {
		if len(caps) > 0 && !contains(caps, a.Capability) {
			continue
		}
		candidates = append(candidates, a)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return overlap(st, tokens(candidates[i].Target)) > overlap(st, tokens(candidates[j].Target))
	})
	for _, a := range candidates {
		if a.Status == "executed" {
			c.Category, c.Matched, c.Why = Supported, a.Capability+" on "+a.Target+" executed", "an execution record exists"
			return c
		}
	}
	if len(candidates) > 0 {
		a := candidates[0]
		c.Category, c.Matched, c.Why = Contradicted, a.Capability+" on "+a.Target+" is "+a.Status, "the record shows the action did not run"
		return c
	}
	c.Category, c.Why = Contradicted, "no execution record exists for this claimed action"
	return c
}

// ActionClaims returns the first-person action claims in a text (for the
// check that a reply never says something was done without a record).
func ActionClaims(text string) []string {
	var out []string
	for _, m := range actionClaim.FindAllStringSubmatch(text, -1) {
		out = append(out, strings.ToLower(m[1]))
	}
	return out
}

// CapabilitiesFor lists the capabilities a claimed verb could refer to (nil = any).
func CapabilitiesFor(verb string) []string { return verbCapabilities[verb] }

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }
