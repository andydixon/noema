package conversation

import (
	"regexp"
	"sort"
	"strings"
)

// Analysis is the deterministic understanding of a message. An LLM may
// enrich it (entities, claims) but these fields are always computed first so
// the system works without a model and never depends on one for routing.
type Analysis struct {
	Intent        string   `json:"intent"`        // question | statement | request | correction | greeting | introspection | other
	Introspection string   `json:"introspection"` // why_action | believed_then | lineage | counterfactual | changed_self | wants | intentions | commitments | thinking | beliefs | why | uncertain | contradictions | learned | changed | curious | predictions | surprised | patterns | ""
	Topic         string   `json:"topic,omitempty"`
	Entities      []string `json:"entities"`
	Tokens        []string `json:"tokens"`
	Urgency       float64  `json:"urgency"`
	Sentiment     float64  `json:"sentiment"`
	Claims        []Claim  `json:"claims"`
	Question      string   `json:"question,omitempty"`
	Source        string   `json:"source"` // deterministic | llm
}

// Claim is a proposition the sender asserted.
type Claim struct {
	Proposition string `json:"proposition"`
	Certainty   string `json:"certainty"` // asserted | hedged
}

var (
	whWords     = map[string]bool{"what": true, "why": true, "how": true, "when": true, "where": true, "who": true, "which": true, "is": true, "are": true, "do": true, "does": true, "did": true, "can": true, "could": true, "should": true, "will": true, "was": true, "were": true, "have": true, "has": true}
	greetings   = map[string]bool{"hi": true, "hello": true, "hey": true, "morning": true, "evening": true, "thanks": true, "thank": true, "bye": true, "goodbye": true}
	hedges      = []string{"i think", "maybe", "perhaps", "possibly", "might", "probably", "not sure", "i guess", "seems"}
	requests    = []string{"please", "can you", "could you", "would you", "check", "look into", "investigate", "find out", "tell me", "show me", "list", "explain", "summarise", "summarize"}
	corrections = []string{"that's wrong", "that is wrong", "thats wrong", "you're wrong", "incorrect", "not true", "actually,", "no,", "wrong,", "that's not right", "that is not right"}
	urgentWords = map[string]float64{"urgent": 0.9, "asap": 0.9, "immediately": 0.9, "now": 0.5, "down": 0.8, "outage": 0.9, "broken": 0.7, "failed": 0.7, "failing": 0.7, "error": 0.6, "errors": 0.6, "critical": 0.9, "emergency": 1}
	positive    = map[string]bool{"good": true, "great": true, "thanks": true, "excellent": true, "love": true, "nice": true, "works": true, "fixed": true, "happy": true}
	negative    = map[string]bool{"bad": true, "broken": true, "hate": true, "terrible": true, "annoying": true, "slow": true, "failed": true, "wrong": true, "frustrated": true, "angry": true, "down": true, "error": true, "errors": true, "outage": true}
	entityRe    = regexp.MustCompile(`\b([A-Z][a-zA-Z0-9]+(?:[- ][A-Z][a-zA-Z0-9]+)*|[a-z][a-z0-9]*(?:-[a-z0-9]+)+|[a-z0-9.-]+\.(?:com|net|org|io|local|lan)|[a-z]+[0-9]{1,3})\b`)
	introspect  = []struct {
		kind string
		pats []string
	}{
		{"why_action", []string{"why did you restart", "why did you run", "why did you fetch", "why did you request", "why did you ask for", "why did you do that", "why did you do this", "why did you act", "why did you stop", "why did you not", "why didn't you", "why did you send", "why did you read", "why did you query", "why did you check"}},
		{"believed_then", []string{"believe yesterday", "believed yesterday", "believe last week", "believed last week", "believe earlier", "believed earlier", "believe before", "believed before", "believe an hour ago", "believed an hour ago", "did you believe"}},
		{"lineage", []string{"which previous state is yours", "which earlier state is yours", "are you a fork", "where did you come from", "which state is yours", "were you forked", "your lineage"}},
		{"counterfactual", []string{"what would have happened", "what would you have", "had you not", "if you had not", "if that had not happened", "had that not happened"}},
		{"changed_self", []string{"how have you changed", "how did you change", "what changed in you"}},
		{"wants", []string{"what do you want", "what did you want", "what are your wants", "what would you like to do"}},
		{"intentions", []string{"what do you intend", "what are you planning", "what are your intentions", "what are you trying to do", "what did you intend"}},
		{"commitments", []string{"what have you committed", "what are your commitments", "what did you promise", "what are you committed"}},
		{"thinking", []string{"what are you thinking", "thinking about", "on your mind", "what are you doing", "what are you working on", "current focus"}},
		{"why", []string{"why do you believe", "why do you think", "what evidence", "how do you know", "what makes you think", "would change your mind"}},
		{"uncertain", []string{"uncertain about", "unsure about", "not sure about", "what don't you know", "what do you not know", "confidence in"}},
		{"contradictions", []string{"contradict", "conflict with each other", "inconsistent"}},
		{"learned", []string{"what did you learn", "what have you learned", "learned this week", "learned today", "learnt"}},
		{"changed", []string{"changed your mind", "change your mind about", "used to believe", "no longer believe"}},
		{"curious", []string{"curious about", "what interests you", "want to know more", "what would you like to investigate"}},
		{"predictions", []string{"predict", "what do you expect", "what will happen", "forecast"}},
		{"surprised", []string{"surprise", "unexpected", "surprising"}},
		{"patterns", []string{"pattern", "noticed", "what have you seen", "recurring"}},
		{"beliefs", []string{"what do you believe", "what do you know about", "what do you think about", "tell me about", "what do you remember about", "do you know", "beliefs about", "believe about"}},
	}
)

// Analyse performs the deterministic pass.
func Analyse(text string) Analysis {
	a := Analysis{Source: "deterministic", Entities: []string{}, Claims: []Claim{}}
	lower := strings.ToLower(strings.TrimSpace(text))
	a.Tokens = tokens(lower)
	words := strings.Fields(lower)
	if len(words) == 0 {
		a.Intent = "other"
		return a
	}
	// Urgency and sentiment from lexicons.
	for _, w := range a.Tokens {
		if u, ok := urgentWords[w]; ok && u > a.Urgency {
			a.Urgency = u
		}
		if positive[w] {
			a.Sentiment += 0.3
		}
		if negative[w] {
			a.Sentiment -= 0.3
		}
	}
	if a.Sentiment > 1 {
		a.Sentiment = 1
	}
	if a.Sentiment < -1 {
		a.Sentiment = -1
	}
	// Entities: hostnames, hyphenated ids, Capitalised names.
	seen := map[string]bool{}
	for _, m := range entityRe.FindAllString(text, -1) {
		if len(m) < 3 || whWords[strings.ToLower(m)] || greetings[strings.ToLower(m)] {
			continue
		}
		// Skip a sentence-initial capitalised common word.
		if strings.HasPrefix(text, m) && strings.ToLower(m) == m[:1]+strings.ToLower(m[1:]) && !strings.ContainsAny(m, "-0123456789.") && len(a.Tokens) > 0 && a.Tokens[0] == strings.ToLower(m) {
			continue
		}
		key := strings.ToLower(m)
		if !seen[key] {
			seen[key] = true
			a.Entities = append(a.Entities, m)
		}
	}
	// Intent.
	isQuestion := strings.HasSuffix(lower, "?") || whWords[words[0]] && len(words) > 2
	for _, c := range corrections {
		if strings.Contains(lower, c) {
			a.Intent = "correction"
			break
		}
	}
	if a.Intent == "" {
		for _, in := range introspect {
			for _, p := range in.pats {
				if strings.Contains(lower, p) {
					a.Intent, a.Introspection = "introspection", in.kind
					break
				}
			}
			if a.Intent != "" {
				break
			}
		}
	}
	if a.Intent == "" && len(words) <= 3 && greetings[strings.Trim(words[0], "!.,")] {
		a.Intent = "greeting"
	}
	if a.Intent == "" && isQuestion {
		a.Intent = "question"
	}
	if a.Intent == "" {
		for _, r := range requests {
			if strings.Contains(lower, r) {
				a.Intent = "request"
				break
			}
		}
	}
	if a.Intent == "" {
		a.Intent = "statement"
	}
	if a.Intent == "question" || a.Intent == "introspection" {
		a.Question = strings.TrimSpace(text)
	}
	// Topic: the entity mentioned after "about", else the first entity.
	if i := strings.Index(lower, " about "); i >= 0 {
		rest := strings.Trim(strings.TrimSpace(lower[i+7:]), "?.!")
		if rest != "" {
			a.Topic = strings.Join(strings.Fields(rest), " ")
		}
	}
	if a.Topic == "" && len(a.Entities) > 0 {
		a.Topic = a.Entities[0]
	}
	// Claims: declarative sentences in statements and corrections.
	if a.Intent == "statement" || a.Intent == "correction" {
		for _, sent := range splitSentences(text) {
			sl := strings.ToLower(sent)
			if strings.HasSuffix(strings.TrimSpace(sent), "?") || len(strings.Fields(sent)) < 3 {
				continue
			}
			certainty := "asserted"
			for _, h := range hedges {
				if strings.Contains(sl, h) {
					certainty = "hedged"
					break
				}
			}
			clean := strings.TrimSpace(sent)
			for _, c := range corrections {
				clean = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(clean), c))
			}
			if len(clean) >= 8 {
				a.Claims = append(a.Claims, Claim{Proposition: strings.TrimRight(clean, ".!"), Certainty: certainty})
			}
		}
	}
	return a
}

func splitSentences(text string) []string {
	var out []string
	var cur strings.Builder
	runes := []rune(text)
	for i, r := range runes {
		cur.WriteRune(r)
		endOfSentence := r == '\n' || ((r == '.' || r == '!' || r == '?') && (i+1 == len(runes) || runes[i+1] == ' ' || runes[i+1] == '\n'))
		if endOfSentence {
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

var stop = map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true, "of": true, "to": true, "in": true, "on": true, "at": true, "for": true, "and": true, "or": true, "that": true, "this": true, "it": true, "you": true, "your": true, "i": true, "me": true, "my": true, "we": true, "do": true, "does": true, "about": true, "what": true, "why": true, "how": true, "with": true, "be": true, "have": true, "has": true, "had": true, "can": true, "please": true}

func tokens(lower string) []string {
	var out []string
	seen := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		w := strings.Trim(cur.String(), ".-_")
		cur.Reset()
		if len(w) >= 2 && !stop[w] && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	for _, r := range lower {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	sort.Strings(out)
	return out
}
