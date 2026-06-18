package workspace

import (
	"testing"
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

func th(subject string, score float64) Thought {
	return Thought{ID: ids.New(), Process: "p", Kind: "attend", Subject: subject, Summary: "about " + subject, Score: score}
}

func TestAdmitRespectsCapacityRefreshesAndExplains(t *testing.T) {
	var s State
	now := time.Now()
	out, displaced := s.Admit([]Thought{th("a", 0.9), th("b", 0.5), th("c", 0.7), th("d", 0.2)}, 3, 1, now)
	if len(s.Workspace) != 3 || s.Workspace[0].Subject != "a" || s.Workspace[2].Subject != "b" || len(displaced) != 0 {
		t.Fatalf("admission wrong: %+v", s.Workspace)
	}
	var rejected int
	for _, o := range out {
		if !o.Admitted {
			rejected++
			if o.Reason == "" || o.Rank != 4 {
				t.Fatalf("rejection unexplained: %+v", o)
			}
		}
	}
	if rejected != 1 {
		t.Fatalf("expected 1 rejection, got %d", rejected)
	}
	// Decay, then a refresh of "b" and a strong newcomer displaces "c".
	s.Decay(0.5)
	out, displaced = s.Admit([]Thought{th("b", 0.6), th("e", 0.8)}, 3, 2, now)
	if len(s.Workspace) != 3 {
		t.Fatal("capacity broken")
	}
	subjects := map[string]bool{}
	for _, w := range s.Workspace {
		subjects[w.Subject] = true
	}
	if !subjects["a"] || !subjects["b"] || !subjects["e"] || subjects["c"] || len(displaced) != 1 {
		t.Fatalf("expected a,b,e with c displaced: %+v displaced=%v", subjects, displaced)
	}
	if !out[0].Admitted || !out[1].Admitted {
		t.Fatalf("outcomes wrong: %+v", out)
	}
}

func TestWorkingMemoryHoldEvictsLeastActive(t *testing.T) {
	var s State
	now := time.Now()
	for i, k := range []string{"x", "y", "z"} {
		s.Hold(Item{Key: k, Content: k}, 3, int64(i+1), now.Add(time.Duration(i)*time.Second))
	}
	s.Decay(0.5)
	s.Hold(Item{Key: "y", Content: "y again"}, 3, 4, now.Add(time.Minute)) // refreshed to 1.0
	evicted := s.Hold(Item{Key: "w", Content: "w"}, 3, 5, now.Add(2*time.Minute))
	if evicted == "" || evicted == "y" || evicted == "w" || len(s.WorkingMemory) != 3 {
		t.Fatalf("eviction wrong: %q %+v", evicted, s.WorkingMemory)
	}
	s.Decay(0.01)
	if n := s.Prune(0.05); n != 3 || len(s.WorkingMemory) != 0 {
		t.Fatalf("prune wrong: %d", n)
	}
}

func TestTokensAndOverlap(t *testing.T) {
	toks := Tokenise("Server-01 reported SMART CRC errors at 09:12!")
	set := map[string]struct{}{}
	for _, w := range toks {
		set[w] = struct{}{}
	}
	if _, ok := set["server-01"]; !ok {
		t.Fatalf("tokenise wrong: %v", toks)
	}
	if o := Overlap(Tokenise("smart errors on server-01 again"), set); o < 0.5 {
		t.Fatalf("overlap too low: %v", o)
	}
	if Overlap(nil, set) != 0 {
		t.Fatal("empty overlap")
	}
}
