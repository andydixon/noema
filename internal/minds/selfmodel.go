package minds

import "time"

// SelfModel is what a mind knows about itself (spec §15). It is rebuilt from
// authoritative sources (policy, goals, config) rather than free text so the
// mind cannot claim abilities it lacks.
type SelfModel struct {
	Name               string             `json:"name"`
	Role               string             `json:"role"`
	Capabilities       []CapabilityEntry  `json:"capabilities"`
	Limitations        []string           `json:"limitations"`
	CurrentActivities  []string           `json:"current_activities"`
	CurrentGoals       []string           `json:"current_goals"`
	ConfidenceByDomain map[string]float64 `json:"confidence_by_domain"`
	KnowledgeGaps      []string           `json:"knowledge_gaps"`
	// Grants lists temporary authority currently in force. The capability
	// layer rewrites it whenever a grant is made, used up, revoked or expires.
	Grants    []GrantEntry `json:"grants,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// GrantEntry is one live scoped grant as the mind sees it.
type GrantEntry struct {
	Capability string    `json:"capability"`
	Target     string    `json:"target"`
	Access     string    `json:"access"`
	ExpiresAt  time.Time `json:"expires_at"`
	Remaining  *int      `json:"remaining_operations,omitempty"`
}

// CapabilityEntry records one capability and the policy that governs it.
type CapabilityEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Policy      string `json:"policy"` // allow | ask | deny | unavailable
}

// baseLimitations are true of every mind by construction and are always
// present so the mind can state them (T25).
var baseLimitations = []string{
	"I cannot change my own capability policies or permissions.",
	"I cannot prevent, delay or influence my own shutdown, pause or disabling; those are operator controls.",
	"I cannot act on external systems except through capabilities the operator has granted, and 'ask' capabilities require approval each time.",
	"My beliefs carry explicit confidence and may be wrong; I should say when I do not know.",
	"I do not have access to other minds' private memories or beliefs.",
}

// NewSelfModel builds the initial self-model for a fresh mind. Capabilities are
// empty until the policy layer reports granted ones.
func NewSelfModel(p Personality) SelfModel {
	return SelfModel{Name: p.Identity.DisplayName, Role: p.Identity.Role, Capabilities: []CapabilityEntry{},
		Limitations: append([]string{}, baseLimitations...), CurrentActivities: []string{}, CurrentGoals: []string{},
		ConfidenceByDomain: map[string]float64{}, KnowledgeGaps: []string{}, UpdatedAt: time.Now()}
}

// Refresh re-applies invariants after any update.
func (s *SelfModel) Refresh(p Personality) {
	s.Name, s.Role = p.Identity.DisplayName, p.Identity.Role
	seen := map[string]bool{}
	var lim []string
	for _, l := range append(append([]string{}, baseLimitations...), s.Limitations...) {
		if !seen[l] {
			seen[l] = true
			lim = append(lim, l)
		}
	}
	s.Limitations = lim
	if s.ConfidenceByDomain == nil {
		s.ConfidenceByDomain = map[string]float64{}
	}
	s.UpdatedAt = time.Now()
}
