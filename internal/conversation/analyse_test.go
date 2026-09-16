package conversation

import "testing"

func TestAnalyseIntents(t *testing.T) {
	cases := []struct {
		text, intent, intro string
	}{
		{"What are you thinking about?", "introspection", "thinking"},
		{"Why do you believe atlas is slow?", "introspection", "why"},
		{"What are you uncertain about?", "introspection", "uncertain"},
		{"Which of your beliefs contradict each other?", "introspection", "contradictions"},
		{"What did you learn this week?", "introspection", "learned"},
		{"What have you changed your mind about?", "introspection", "changed"},
		{"What do you currently believe about atlas?", "introspection", "beliefs"},
		{"Is atlas slow again since the deploy?", "question", ""},
		{"Please check the disk on server-01", "request", ""},
		{"No, that's wrong, atlas is on the new rack", "correction", ""},
		{"hello", "greeting", ""},
		{"Andy prefers Go for deployed services.", "statement", ""},
	}
	for _, c := range cases {
		a := Analyse(c.text)
		if a.Intent != c.intent || a.Introspection != c.intro {
			t.Errorf("%q: got %s/%s want %s/%s", c.text, a.Intent, a.Introspection, c.intent, c.intro)
		}
	}
}

func TestAnalyseEntitiesClaimsAndUrgency(t *testing.T) {
	a := Analyse("server-01 is down and atlas.local shows SMART errors. I think the SATA cable might be loose.")
	if a.Urgency < 0.8 || a.Sentiment >= 0 {
		t.Fatalf("urgency/sentiment wrong: %+v", a)
	}
	if len(a.Entities) < 2 {
		t.Fatalf("entities missing: %v", a.Entities)
	}
	if len(a.Claims) != 2 || a.Claims[0].Certainty != "asserted" || a.Claims[1].Certainty != "hedged" {
		t.Fatalf("claims wrong: %+v", a.Claims)
	}
	if b := Analyse("What do you believe about server-01?"); b.Topic != "server-01" {
		t.Fatalf("topic wrong: %q", b.Topic)
	}
}

func TestAnalyseVolitionAndContinuityIntrospection(t *testing.T) {
	for text, want := range map[string]string{
		"Why did you restart atlas-worker?":               "why_action",
		"What did you believe yesterday about atlas?":     "believed_then",
		"Which previous state is yours?":                  "lineage",
		"What would have happened had you not seen that?": "counterfactual",
		"How have you changed this month?":                "changed_self",
		"What do you want right now?":                     "wants",
		"What are your intentions?":                       "intentions",
		"What have you committed to?":                     "commitments",
	} {
		if a := Analyse(text); a.Intent != "introspection" || a.Introspection != want {
			t.Errorf("%q → %s/%s, want %s", text, a.Intent, a.Introspection, want)
		}
	}
}
