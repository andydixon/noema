// Package epistemics_test drives the conversation pipeline end to end and
// checks that statements become assertions first and beliefs only when
// warranted: the fact that somebody said something is certain; the truth
// of what they said is a separate, weaker matter.
package epistemics_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/epistemics"
	"github.com/andydixon/noema/pkg/ids"
	"github.com/andydixon/noema/tests/harness"
)

func TestAssertionsAreNotBeliefs(t *testing.T) {
	h := harness.Start(t)
	ctx := context.Background()
	m := h.Mind("epi")
	say := func(text string) {
		t.Helper()
		if code, body := h.Do(h.Admin, "POST", "/api/v1/minds/epi/ask", map[string]any{"text": text}); code >= 300 {
			t.Fatalf("say %q → %d %s", text, code, body)
		}
		h.Settle(m.ID)
	}
	say("Steve steals laptops.")
	say("Alice thinks the server is compromised.")
	say("People keep saying PostgreSQL corrupts data.")
	say("Imagine Bob murdered somebody.")
	say("Sarah says the network is down, but I don't believe her.")
	say(`Dave said "the backups finished at 03:00".`)
	say("Atlas is not the primary database host.")
	say("Oh yes, Dave is an absolute genius.")

	all, err := h.Deps.Assertions.List(ctx, m.ID, epistemics.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byProp := map[string]epistemics.Assertion{}
	for _, a := range all {
		byProp[strings.ToLower(a.Proposition)] = a
	}
	find := func(sub string) epistemics.Assertion {
		t.Helper()
		for k, a := range byProp {
			if strings.Contains(k, sub) {
				return a
			}
		}
		t.Fatalf("no assertion containing %q among %d: %v", sub, len(all), keys(byProp))
		return epistemics.Assertion{}
	}
	beliefFor := func(id ids.ID) (beliefs.Belief, bool) {
		if id.IsZero() {
			return beliefs.Belief{}, false
		}
		b, err := h.Deps.Beliefs.Get(ctx, nil, m.ID, id)
		return b, err == nil
	}

	// Direct assertion: attributed to the speaker, a claim belief with modest confidence.
	steve := find("steve steals laptops")
	if !strings.EqualFold(steve.AttributedTo, "admin") || steve.Stance != epistemics.StanceAsserts || steve.Reported {
		t.Fatalf("direct: %+v", steve)
	}
	if b, ok := beliefFor(steve.BeliefID); !ok || b.Epistemic != "claim" || b.Confidence > 0.75 || b.Confidence < 0.5 {
		t.Fatalf("direct claim belief: %+v ok=%v", b, ok)
	}
	// Reported belief: attributed to Alice, second-hand, weaker than the direct one.
	alice := find("server is compromised")
	if !strings.EqualFold(alice.AttributedTo, "Alice") || !alice.Reported || alice.Actor != "admin" {
		t.Fatalf("reported: %+v", alice)
	}
	ab, _ := beliefFor(alice.BeliefID)
	sb, _ := beliefFor(steve.BeliefID)
	if !(ab.Confidence < sb.Confidence) {
		t.Fatalf("reported claim (%.2f) should be weaker than direct (%.2f)", ab.Confidence, sb.Confidence)
	}
	rows, _ := h.Deps.Beliefs.EvidenceFor(ctx, nil, m.ID, ab.ID)
	if len(rows) != 1 || rows[0].Family != "actor:alice" || rows[0].Derivation != beliefs.DerivationAssertion {
		t.Fatalf("reported evidence family: %+v", rows)
	}
	// Diffuse: attributed to nobody in particular, low interpretation confidence.
	pg := find("postgresql corrupts data")
	if pg.AttributedTo != "unspecified people" || pg.Interpretation > 0.7 {
		t.Fatalf("diffuse: %+v", pg)
	}
	// Hypothetical: recorded, no belief at all.
	bob := find("bob murdered somebody")
	if !bob.Hypothetical || !bob.BeliefID.IsZero() {
		t.Fatalf("hypothetical created a belief: %+v", bob)
	}
	list, _ := h.Deps.Beliefs.List(ctx, m.ID, beliefs.Filter{Limit: 100})
	for _, b := range list {
		if strings.Contains(strings.ToLower(b.Proposition), "murdered") {
			t.Fatalf("hypothetical leaked into beliefs: %+v", b)
		}
	}
	// Disagreement: Sarah's claim recorded, speaker's doubt counts against it.
	sarah := find("network is down")
	if !strings.EqualFold(sarah.AttributedTo, "Sarah") || !strings.Contains(strings.Join(sarah.Notes, " "), "doubts") {
		t.Fatalf("disagreement: %+v", sarah)
	}
	srows, _ := h.Deps.Beliefs.EvidenceFor(ctx, nil, m.ID, sarah.BeliefID)
	var against int
	for _, r := range srows {
		if r.Direction == "against" && r.Family == "actor:admin" {
			against++
		}
	}
	if against != 1 {
		t.Fatalf("doubt not recorded as counter-evidence: %+v", srows)
	}
	sarahB, _ := beliefFor(sarah.BeliefID)
	if sarahB.Confidence >= ab.Confidence {
		t.Fatalf("doubted claim (%.2f) should be weaker than an undisputed reported one (%.2f)", sarahB.Confidence, ab.Confidence)
	}
	// Quoted speech is attributed to the quoted person.
	dave := find("backups finished at 03:00")
	if !dave.Quoted || !strings.EqualFold(dave.AttributedTo, "Dave") {
		t.Fatalf("quoted: %+v", dave)
	}
	// Negation is recorded on the assertion and the proposition kept as stated.
	atlas := find("atlas is not the primary database host")
	if !atlas.Negated {
		t.Fatalf("negation: %+v", atlas)
	}
	// Possible sarcasm: recorded with low interpretation confidence; any belief is weak.
	genius := find("dave is an absolute genius")
	if genius.Interpretation > 0.5 {
		t.Fatalf("sarcasm should lower interpretation confidence: %+v", genius)
	}
	if gb, ok := beliefFor(genius.BeliefID); ok && gb.Confidence > sb.Confidence {
		t.Fatalf("sarcastic remark believed more than a plain one: %.2f vs %.2f", gb.Confidence, sb.Confidence)
	}
	// Co-mention produces association edges, never causal ones.
	var causal int
	h.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM entity_relationships WHERE mind_id=$1 AND relation IN ('caused_by')`, m.ID).Scan(&causal) //nolint:errcheck
	var cooccur int
	h.Deps.DB.QueryRow(ctx, `SELECT count(*) FROM entity_relationships WHERE mind_id=$1 AND relation='co_occurs_with'`, m.ID).Scan(&cooccur) //nolint:errcheck
	if causal != 0 || cooccur == 0 {
		t.Fatalf("relations: caused_by=%d co_occurs_with=%d", causal, cooccur)
	}
	// Assertions are append-only.
	if _, err := h.Deps.DB.Exec(ctx, `UPDATE assertions SET proposition='x' WHERE id=$1`, steve.ID); err == nil {
		t.Fatal("assertion row was mutable")
	}
	// API surface.
	if code, body := h.Do(h.Obs, "GET", "/api/v1/minds/epi/assertions?actor=alice", nil); code != 200 || !strings.Contains(string(body), "server is compromised") {
		t.Fatalf("assertions API: %d %s", code, body)
	}
	if code, body := h.Do(h.Obs, "GET", "/api/v1/minds/epi/beliefs/"+ab.ID.String(), nil); code != 200 || !strings.Contains(string(body), `"assertions"`) {
		t.Fatalf("belief detail lacks assertions: %d", code)
	}
}

func keys(m map[string]epistemics.Assertion) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
