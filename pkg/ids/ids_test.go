package ids

import (
	"sort"
	"testing"
	"time"
)

func TestNewIsOrderedUniqueAndParsable(t *testing.T) {
	const n = 5000
	seen := make(map[ID]struct{}, n)
	prev := ID("")
	for i := 0; i < n; i++ {
		id := New()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = struct{}{}
		if id <= prev {
			t.Fatalf("ids not increasing: %s after %s", id, prev)
		}
		prev = id
		if p, err := Parse(string(id)); err != nil || p != id {
			t.Fatalf("parse round trip failed for %s: %v", id, err)
		}
		if id[14] != '7' {
			t.Fatalf("not version 7: %s", id)
		}
	}
	ids := make([]string, 0, n)
	for id := range seen {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	if d := time.Since(ID(ids[0]).Time()); d < 0 || d > time.Minute {
		t.Fatalf("embedded time implausible: %v", d)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "not-a-uuid", "0192b7e4-3c7a-7c3e-8f1e-0123456789zz", "0192b7e43c7a7c3e8f1e0123456789ab"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
	if _, err := Parse("0192B7E4-3C7A-7C3E-8F1E-0123456789AB"); err != nil {
		t.Errorf("upper case should parse: %v", err)
	}
}
