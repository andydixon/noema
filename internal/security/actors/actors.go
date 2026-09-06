// Package actors distinguishes people from Noema's own components in the
// "by" and "actor" fields of decisions. Authority-bearing decisions
// (approvals, grants, capability requests) must name a person; a name that
// an internal component writes is refused, so no cognitive process can
// approve its own request by writing a plausible decider.
package actors

import "strings"

// Components are the names internal processes use as sources or actors.
var Components = []string{"mind", "brain", "planning", "volition", "cognition", "capabilities", "noema", "system", "dream", "replay", "experiment", "scheduler", "agency", "research"}

// IsPerson reports whether name can stand for a person making a decision.
func IsPerson(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" || strings.Contains(n, ":") {
		return false
	}
	for _, c := range Components {
		if n == c || strings.HasPrefix(n, c+".") {
			return false
		}
	}
	return true
}
