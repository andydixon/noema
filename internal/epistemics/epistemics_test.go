package epistemics

import "testing"

func TestInterpretSpeechPatterns(t *testing.T) {
	cases := []struct {
		name, sentence               string
		wantAttributed, wantStance   string
		wantModality                 string
		wantReported, wantHypothet   bool
		wantNegated, wantBelief      bool
		wantSpeakerStance            string
		maxConfidence, minConfidence float64
	}{
		{"direct factual", "Steve steals laptops.", "andy", StanceAsserts, ModalityFactual, false, false, false, true, "", 1, 0.8},
		{"reported belief", "Alice thinks the server is compromised.", "Alice", StanceAsserts, ModalityReported, true, false, false, true, StanceReports, 1, 0.6},
		{"diffuse report", "People keep saying PostgreSQL corrupts data.", "unspecified people", StanceAsserts, ModalityReported, true, false, false, true, StanceReports, 0.7, 0.4},
		{"hypothetical", "Imagine Bob murdered somebody.", "andy", StanceSupposes, ModalityHypothetical, false, true, false, false, "", 1, 0.5},
		{"conditional", "If the disk fills up, the database stops accepting writes.", "andy", StanceSupposes, ModalityHypothetical, false, true, false, false, "", 1, 0.5},
		{"disagreement", "Sarah says the network is down, but I don't believe her.", "Sarah", StanceAsserts, ModalityReported, true, false, false, true, StanceDoubts, 1, 0.5},
		{"quoted", `Dave said "the backups finished at 03:00".`, "Dave", StanceAsserts, ModalityQuoted, true, false, false, true, StanceReports, 1, 0.6},
		{"negated", "Atlas is not the primary database host.", "andy", StanceAsserts, ModalityFactual, false, false, true, true, "", 1, 0.8},
		{"sarcasm suspected", "Oh yes, Dave is an absolute genius.", "andy", StanceAsserts, ModalityFactual, false, false, false, true, "", 0.5, 0},
		{"question", "Is atlas the primary host?", "andy", StanceQuestions, ModalityQuestion, false, false, false, false, "", 1, 0},
		{"imperative", "Please check the disk on server-01.", "andy", StanceSupposes, ModalityImperative, false, false, false, false, "", 1, 0},
		{"hedged", "I think the migration caused the outage.", "andy", StanceAsserts, ModalityFactual, false, false, false, true, "", 1, 0.8},
		{"first person not reported", "I said the migration was fine.", "andy", StanceAsserts, ModalityFactual, false, false, false, true, "", 1, 0.8},
	}
	for _, c := range cases {
		in := Interpret("andy", c.sentence)
		if in.AttributedTo != c.wantAttributed || in.Stance != c.wantStance || in.Modality != c.wantModality || in.Reported != c.wantReported || in.Hypothetical != c.wantHypothet || in.Negated != c.wantNegated {
			t.Errorf("%s: %+v", c.name, in)
		}
		if in.CreatesBelief() != c.wantBelief {
			t.Errorf("%s: CreatesBelief=%v want %v", c.name, in.CreatesBelief(), c.wantBelief)
		}
		if in.SpeakerStance != c.wantSpeakerStance {
			t.Errorf("%s: speaker stance %q want %q", c.name, in.SpeakerStance, c.wantSpeakerStance)
		}
		if in.Confidence > c.maxConfidence || in.Confidence < c.minConfidence {
			t.Errorf("%s: interpretation confidence %.2f outside [%.2f, %.2f]", c.name, in.Confidence, c.minConfidence, c.maxConfidence)
		}
	}
	// Weights: direct > hedged > reported; sarcasm discounts; doubt counts against.
	direct, hedged, reported := Interpret("a", "The server is down."), Interpret("a", "I think the server is down."), Interpret("a", "Alice says the server is down.")
	if !(direct.EvidenceWeight() > hedged.EvidenceWeight() && hedged.EvidenceWeight() > reported.EvidenceWeight()*1.2) {
		t.Fatalf("weights: direct=%.3f hedged=%.3f reported=%.3f", direct.EvidenceWeight(), hedged.EvidenceWeight(), reported.EvidenceWeight())
	}
	if s := Interpret("a", "Oh yes, Dave is an absolute genius."); s.EvidenceWeight() >= direct.EvidenceWeight()/2 {
		t.Fatalf("sarcasm not discounted: %.3f", s.EvidenceWeight())
	}
	if d := Interpret("a", "Sarah says the network is down, but I don't believe her."); d.DoubtWeight() == 0 || d.EvidenceWeight() == 0 {
		t.Fatalf("disagreement should record both the reported claim and the doubt: %+v", d)
	}
	if h := Interpret("a", "Imagine Bob murdered somebody."); h.EvidenceWeight() != 0.5*0.85 && !h.Hypothetical {
		t.Fatal("hypothetical weight irrelevant but flag must be set")
	}
	// Reported speech keeps the proposition, not the frame.
	if r := Interpret("a", "Alice thinks the server is compromised."); r.Proposition != "the server is compromised" {
		t.Fatalf("proposition should drop the reporting frame: %q", r.Proposition)
	}
}
