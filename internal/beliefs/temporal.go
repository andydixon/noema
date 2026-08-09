package beliefs

import (
	"regexp"
	"strings"
	"time"
)

// Temporal scopes. Persistent propositions are true or false without an
// interval ("atlas is the primary host", "Andy prefers Go"). Transient ones
// describe a state that can change ("the door is open", "server A is down")
// and carry a validity interval. Unknown is the honest default when the
// classifier cannot tell.
const (
	ScopePersistent = "persistent"
	ScopeTransient  = "transient"
	ScopeUnknown    = "unknown"
)

// MinStateChangeGap is the shortest interval between two observations for a
// later, conflicting observation of a transient state to count as a state
// change rather than a contradiction. Two reports of the same moment that
// disagree are a contradiction; reports minutes apart are a change.
const MinStateChangeGap = time.Minute

var (
	reTransient  = regexp.MustCompile(`(?i)\b(?:is|are|was|were|remains|seems|looks|appears|has been|have been|is currently|is now)\s+(?:not\s+|still\s+|no longer\s+)?(?:down|up|open|closed|offline|online|running|stopped|stuck|failing|degraded|full|empty|busy|idle|locked|unlocked|available|unavailable|slow|broken|healthy|unhealthy|overloaded|unreachable|reachable|responding|paused|active|inactive|pending|in progress|at capacity|on call|away|present|absent|late|early|hot|cold|warm|crossed)\b|\b(?:right now|currently|at the moment|at \d{1,2}:\d{2}|between \d|since \d|until \d|today|tonight|this morning|this afternoon|for the last|in the last)\b`)
	rePersistent = regexp.MustCompile(`(?i)\b(?:is (?:a|an|the)\b|are (?:a|an|the)\b|was born|prefers?|likes?|dislikes?|always|never|is named|is called|is the name|belongs to|owns|is made of|is located in|is based in|works at|works for|is part of|means|stands for|is written in|uses|runs on|depends on)\b`)
)

// ClassifyScope decides, deterministically, whether a proposition names a
// transient state, a persistent fact, or neither recognisably.
func ClassifyScope(proposition string) string {
	p := strings.TrimSpace(proposition)
	switch {
	case reTransient.MatchString(p):
		return ScopeTransient
	case rePersistent.MatchString(p):
		return ScopePersistent
	}
	return ScopeUnknown
}

// StateChange reports whether a newer belief should close an older,
// conflicting one as a change of state rather than contest it: both must be
// transient, and the newer observation must be later by at least the gap.
func StateChange(older, newer Belief) bool {
	if older.TemporalScope != ScopeTransient || newer.TemporalScope != ScopeTransient {
		return false
	}
	if older.ObservedAt == nil || newer.ObservedAt == nil {
		return false
	}
	return newer.ObservedAt.Sub(*older.ObservedAt) >= MinStateChangeGap
}
