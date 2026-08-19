package concepts

import "testing"

func TestRelationSemantics(t *testing.T) {
	for _, r := range []Relation{CoOccursWith, PossiblyCausedBy, TemporallyPrecedes, AssertedBy, CausedBy} {
		if !r.Valid() {
			t.Fatalf("%s should be valid", r)
		}
	}
	if !CausedBy.Causal() || PossiblyCausedBy.Causal() || CoOccursWith.Causal() {
		t.Fatal("only caused_by asserts a cause")
	}
	if !PossiblyCausedBy.Hypothetical() || CausedBy.Hypothetical() {
		t.Fatal("possibly_caused_by is the hypothetical form")
	}
}
