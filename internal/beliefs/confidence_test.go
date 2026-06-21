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
