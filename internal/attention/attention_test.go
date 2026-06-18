package attention

import (
	"math/rand"
	"testing"
)

var w = map[string]float64{"salience": 1, "urgency": 1.2, "novelty": 0.8, "affect": 0.6, "goal_relevance": 1.1, "consequence": 0.9, "confidence": 0.4, "contradiction": 1, "operator_priority": 1.5, "context_relevance": 0.9}

func TestScoreDeterministicBoundedAndExplained(t *testing.T) {
	f := Features{Salience: 0.84, Urgency: 0.91, Novelty: 0.63, GoalRelevance: 0.92, Confidence: 2 /* clamped */}
	a, b := Score(f, w), Score(f, w)
	if a.Total != b.Total || len(a.Terms) != len(Names) {
		t.Fatal("score not deterministic or incomplete")
	}
	if a.Total <= 0 || a.Total > 1 {
		t.Fatalf("total out of range: %v", a.Total)
	}
	var sum float64
	for _, term := range a.Terms {
		sum += term.Contribution
		if term.Feature == "confidence" && term.Value != 1 {
			t.Fatal("clamp failed")
		}
	}
	if d := sum - a.Total; d > 0.001 || d < -0.001 {
		t.Fatalf("terms do not add up: %v vs %v", sum, a.Total)
	}
	if Score(Features{}, w).Total != 0 || Score(f, map[string]float64{}).Total != 0 {
		t.Fatal("zero cases wrong")
	}
	low := Score(Features{Salience: 0.48, GoalRelevance: 0.32, Novelty: 0.22, Urgency: 0.08}, w)
	if low.Total >= a.Total {
		t.Fatal("weaker candidate should score lower")
	}
}

func TestRankIsTotalOrderIndependentOfInput(t *testing.T) {
	items := []Scored{{"c", Breakdown{Total: 0.5}}, {"a", Breakdown{Total: 0.5}}, {"b", Breakdown{Total: 0.9}}, {"d", Breakdown{Total: 0.1}}}
	want := []string{"b", "a", "c", "d"}
	for i := 0; i < 20; i++ {
		rand.Shuffle(len(items), func(x, y int) { items[x], items[y] = items[y], items[x] })
		Rank(items)
		for j, it := range items {
			if it.ID != want[j] {
				t.Fatalf("order %v", items)
			}
		}
	}
}
