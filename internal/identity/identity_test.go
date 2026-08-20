package identity

import (
	"testing"

	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/version"
)

func TestConfigDigestIgnoresSecrets(t *testing.T) {
	a := config.Defaults()
	a.MasterKey, a.DatabaseURL, a.BootstrapAdminPassword = "k1", "postgres://u:p1@h/db", "pw1"
	b := a
	b.MasterKey, b.DatabaseURL, b.BootstrapAdminPassword = "k2", "postgres://u:p2@other/db", "pw2"
	if a.Digest() != b.Digest() {
		t.Fatal("digest must not change when only secrets or credentials change")
	}
	c := a
	c.MaxWorkers++
	if c.Digest() == a.Digest() {
		t.Fatal("digest must change when behaviour-affecting configuration changes")
	}
	for _, d := range []string{a.Digest(), b.Digest()} {
		for _, secret := range []string{"k1", "k2", "p1", "p2", "pw1", "pw2"} {
			if len(secret) > 0 && contains(d, secret) {
				t.Fatalf("digest leaks %q", secret)
			}
		}
	}
}

func TestCompareLabels(t *testing.T) {
	base := Identity{SchemaVersion: 22, Build: version.Build{Commit: "abcdef1234567890", GoVersion: "go1.26"}, ConfigDigest: "sha256:x", Routing: map[string]string{"respond": "openai/gpt"}, PromptVersions: map[string]int{"respond": 1}, Deterministic: Deterministic}
	if c, d := Compare(base, base); c != CompatExactCandidate || len(d) != 0 {
		t.Fatalf("identical: %s %v", c, d)
	}
	other := base
	other.Build = version.Build{Commit: "0123456789abcdef", GoVersion: "go1.26"}
	if c, d := Compare(base, other); c != CompatDifferentBuild || len(d) != 1 {
		t.Fatalf("different build: %s %v", c, d)
	}
	rerouted := base
	rerouted.Routing = map[string]string{"respond": "anthropic/claude"}
	if c, d := Compare(base, rerouted); c != CompatDifferentBuild || len(d) != 1 {
		t.Fatalf("rerouted: %s %v", c, d)
	}
	older := base
	older.SchemaVersion = 21
	if c, _ := Compare(older, base); c != CompatIncompatible {
		t.Fatalf("schema change must be incompatible: %s", c)
	}
	// An unrecorded identity (pre-0.2 snapshot) is named as such rather than
	// compared blank against blank or treated as an exact candidate.
	if c, d := Compare(Identity{}, base); c != CompatDifferentBuild || len(d) != 1 {
		t.Fatalf("unrecorded: %s %v", c, d)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
