package actors

import "testing"

func TestIsPerson(t *testing.T) {
	for _, n := range []string{"", " ", "system", "System", "mind", "mind:abc", "brain.act", "planning", "volition.form", "user:andy", "noema"} {
		if IsPerson(n) {
			t.Errorf("%q accepted as a person", n)
		}
	}
	for _, n := range []string{"andy", "operator-1", "a.dixon", "brainstorm"} {
		if !IsPerson(n) {
			t.Errorf("%q refused", n)
		}
	}
}
