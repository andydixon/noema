package beliefs

import (
	"testing"

	"github.com/andydixon/noema/pkg/concepts"
)

func TestComputeIsMonotonicBoundedAndDiscountsSingleSource(t *testing.T) {
	none := Compute(0.5, nil)
	if none.Confidence != 0.5 {
		t.Fatalf("no evidence should keep the prior, got %v", none.Confidence)
	}
	one := Compute(0.5, []Evidence{{Direction: "for", Kind: "event", RefID: "a", Source: "metrics", Weight: 0.8, Credibility: 0.9}})
	two := Compute(0.5, []Evidence{{Direction: "for", Kind: "event", RefID: "a", Source: "metrics", Weight: 0.8, Credibility: 0.9}, {Direction: "for", Kind: "event", RefID: "b", Source: "metrics", Weight: 0.8, Credibility: 0.9}})
	diverse := Compute(0.5, []Evidence{{Direction: "for", Kind: "event", RefID: "a", Source: "metrics", Weight: 0.8, Credibility: 0.9}, {Direction: "for", Kind: "event", RefID: "b", Source: "logs", Weight: 0.8, Credibility: 0.9}, {Direction: "for", Kind: "event", RefID: "c", Source: "operator", Weight: 0.8, Credibility: 0.9}})
	if !(none.Confidence < one.Confidence && one.Confidence < two.Confidence && two.Confidence < diverse.Confidence) {
		t.Fatalf("not monotonic: %v %v %v %v", none.Confidence, one.Confidence, two.Confidence, diverse.Confidence)
	}
	if one.DiversityScale != 0.6 || len(one.Notes) == 0 || diverse.DiversityScale != 1 {
		t.Fatalf("diversity discount wrong: %+v / %+v", one, diverse)
	}
	huge := make([]Evidence, 50)
	for i := range huge {
		huge[i] = Evidence{Direction: "for", Kind: "event", RefID: string(rune('a' + i)), Source: "s" + string(rune('a'+i)), Weight: 1, Credibility: 1}
	}
	if Compute(0.5, huge).Confidence > MaxConfidence {
		t.Fatal("confidence exceeded bound")
	}
	against := Compute(0.5, []Evidence{{Direction: "against", Kind: "event", RefID: "x", Weight: 1, Credibility: 1}})
	if against.Confidence >= 0.5 {
		t.Fatal("opposing evidence should lower confidence")
	}
	retracted := Compute(0.5, []Evidence{{Direction: "for", Kind: "event", RefID: "a", Weight: 1, Credibility: 1, Retracted: true}})
	if retracted.Confidence != 0.5 {
		t.Fatal("retracted evidence must not count")
	}
}

func TestDeriveStatuses(t *testing.T) {
	cases := []struct {
		b    Breakdown
		e    concepts.Epistemic
		want Status
	}{
		{Compute(PriorFor(concepts.Hypothesis), nil), concepts.Hypothesis, StatusHypothesis},
		{Compute(0.5, []Evidence{{Direction: "for", Source: "a", Weight: 0.5, Credibility: 0.9}}), concepts.Claim, StatusTentative},
		{Compute(0.5, []Evidence{{Direction: "for", Source: "a", Weight: 1, Credibility: 1}, {Direction: "for", Source: "b", Weight: 1, Credibility: 1}, {Direction: "for", Source: "c", Weight: 1, Credibility: 1}}), concepts.Claim, StatusAccepted},
		{Compute(0.5, []Evidence{{Direction: "for", Source: "a", Weight: 0.8, Credibility: 0.9}, {Direction: "for", Source: "b", Weight: 0.8, Credibility: 0.9}, {Direction: "against", Source: "c", Weight: 0.8, Credibility: 0.9}}), concepts.Claim, StatusContested},
		{Compute(0.5, []Evidence{{Direction: "against", Source: "a", Weight: 1, Credibility: 1}, {Direction: "against", Source: "b", Weight: 1, Credibility: 1}}), concepts.Claim, StatusRejected},
	}
	for i, c := range cases {
		if got := Derive(c.b, c.e); got != c.want {
			t.Errorf("case %d: got %s want %s (conf %v for %v against %v)", i, got, c.want, c.b.Confidence, c.b.For, c.b.Against)
		}
	}
}

func TestContradicts(t *testing.T) {
	if ok, _ := Contradicts("server-01 has an intermittent storage problem", "server-01 does not have an intermittent storage problem"); !ok {
		t.Fatal("negated paraphrase not detected")
	}
	if ok, _ := Contradicts("server-01 has a storage problem", "server-02 has a network problem"); ok {
		t.Fatal("unrelated propositions flagged")
	}
	if ok, _ := Contradicts("atlas is slow", "atlas is slow"); ok {
		t.Fatal("identical propositions flagged")
	}
}

// TestProvenanceGroupingPreventsInflation pins the family arithmetic:
// independent families add up; derived re-expressions of one source add
// nothing to it; repetition within a family is heavily discounted; legacy
// rows (family = source) behave as primary evidence.
func TestProvenanceGroupingPreventsInflation(t *testing.T) {
	ev := func(fam, deriv string, w float64) Evidence {
		return Evidence{Direction: "for", Kind: "event", RefID: fam + deriv, Source: fam, Family: fam, Derivation: deriv, Weight: w, Credibility: 1}
	}
	one := Compute(0.5, []Evidence{ev("actor:andy", DerivationAssertion, 0.6)})
	threeIndependent := Compute(0.5, []Evidence{ev("actor:andy", DerivationAssertion, 0.6), ev("source:prometheus:prod", DerivationExternal, 0.6), ev("operator:admin", DerivationOperator, 0.6)})
	threeDerived := Compute(0.5, []Evidence{
		ev("actor:andy", DerivationAssertion, 0.6),     // E1: what Andy said
		ev("actor:andy", DerivationConsolidation, 0.6), // S1: a semantic memory consolidated from it
		ev("actor:andy", DerivationInference, 0.6),     // I1: an inference drawn from S1
	})
	repeated := Compute(0.5, []Evidence{ev("actor:andy", DerivationAssertion, 0.6), ev("actor:andy", DerivationAssertion, 0.6), ev("actor:andy", DerivationAssertion, 0.6)})
	if threeDerived.Confidence != one.Confidence {
		t.Fatalf("derived copies inflated confidence: one=%.4f derived=%.4f", one.Confidence, threeDerived.Confidence)
	}
	if threeDerived.SourcesFor != 1 || threeIndependent.SourcesFor != 3 {
		t.Fatalf("family counting wrong: derived=%d independent=%d", threeDerived.SourcesFor, threeIndependent.SourcesFor)
	}
	if !(threeIndependent.Confidence > repeated.Confidence && repeated.Confidence > one.Confidence) {
		t.Fatalf("ordering wrong: independent=%.4f repeated=%.4f one=%.4f", threeIndependent.Confidence, repeated.Confidence, one.Confidence)
	}
	// Repetition is capped at 1.5× the strongest item however many times it recurs.
	many := make([]Evidence, 20)
	for i := range many {
		many[i] = ev("actor:andy", DerivationAssertion, 0.6)
	}
	if got := Compute(0.5, many).For; got > 0.6*1.5+1e-9 {
		t.Fatalf("repetition not capped: for=%.4f", got)
	}
	// Derived evidence stands in only when nothing primary exists for the family.
	onlyDerived := Compute(0.5, []Evidence{ev("actor:andy", DerivationConsolidation, 0.3), ev("actor:andy", DerivationInference, 0.2)})
	if onlyDerived.For != 0.3 {
		t.Fatalf("only-derived family should count its strongest item once: %.4f", onlyDerived.For)
	}
	// Legacy rows: family falls back to source and they count as primary.
	legacy := Compute(0.5, []Evidence{{Direction: "for", Kind: "event", RefID: "x", Source: "metrics", Weight: 0.6, Credibility: 1, Derivation: DerivationLegacy}, {Direction: "for", Kind: "event", RefID: "y", Source: "logs", Weight: 0.6, Credibility: 1}})
	if legacy.SourcesFor != 2 || len(legacy.Families) != 2 {
		t.Fatalf("legacy grouping: %+v", legacy.Families)
	}
	// Counter-evidence is grouped the same way.
	against := Compute(0.5, []Evidence{{Direction: "against", Kind: "belief", RefID: "b", Source: "contradiction", Family: "belief:b", Derivation: DerivationInference, Weight: 0.5, Credibility: 0.8},
		{Direction: "against", Kind: "belief", RefID: "b", Source: "contradiction", Family: "belief:b", Derivation: DerivationInference, Weight: 0.5, Credibility: 0.8}})
	if against.Against != 0.4 {
		t.Fatalf("against grouping: %.4f", against.Against)
	}
	// Families derive from event payloads: an actor beats a channel beats the component.
	if f := FamilyForActor(" Andy "); f != "actor:andy" {
		t.Fatal(f)
	}
	if f := FamilyForSource("Prometheus", "prod"); f != "source:prometheus:prod" {
		t.Fatal(f)
	}
}
