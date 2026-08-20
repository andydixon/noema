// Package identity records enough about the running system to explain, later,
// why two runs of the same cognition differed: the build, the schema, the
// non-secret configuration, the model routing, prompt and personality
// versions and the policy in force. Replays and experiments store it and
// compare it; a difference is reported, never papered over.
package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/version"
	"github.com/andydixon/noema/pkg/ids"
)

// Identity is the runtime identity captured with a snapshot, replay or
// experiment. Everything in it is safe to display: no secret or secret
// reference is ever included.
type Identity struct {
	CapturedAt         time.Time         `json:"captured_at"`
	Build              version.Build     `json:"build"`
	SchemaVersion      int               `json:"schema_version"`
	ConfigDigest       string            `json:"config_digest"`
	Timezone           string            `json:"timezone"`
	PersonalityVersion int               `json:"personality_version,omitempty"`
	PolicyDigest       string            `json:"policy_digest,omitempty"`
	Routing            map[string]string `json:"routing,omitempty"`         // role → provider/model
	PromptVersions     map[string]int    `json:"prompt_versions,omitempty"` // prompt name → current version
	Deterministic      map[string]string `json:"deterministic"`             // versions of deterministic subsystems whose output is compared
}

// Deterministic subsystem versions. Bump one when its output for the same
// input changes, so a replay can name the component that moved.
var Deterministic = map[string]string{
	"attention":     "1",
	"retrieval":     "1",
	"perception":    "1",
	"epistemics":    "1",
	"confidence":    "2", // family grouping
	"contradiction": "2", // temporal supersession
	"association":   "2", // speech acts excluded
}

// Capturer gathers an identity from the live system. Every field is optional
// so tests and partial deployments still get a truthful, smaller record.
type Capturer struct {
	DB           *pgxpool.Pool
	Config       *config.Config
	Minds        *minds.Store
	LLM          *router.Store
	Prompts      *prompts.Store
	Capabilities *capabilities.Service
}

// Capture records the identity for a mind (ids.Nil for daemon-wide).
func (c *Capturer) Capture(ctx context.Context, mind ids.ID) Identity {
	id := Identity{CapturedAt: time.Now().UTC(), Build: version.Info(), Timezone: time.Now().Location().String(), Deterministic: Deterministic}
	if c == nil {
		return id
	}
	if c.DB != nil {
		id.SchemaVersion, _ = storage.SchemaVersion(ctx, c.DB)
	}
	if c.Config != nil {
		id.ConfigDigest = c.Config.Digest()
	}
	if c.Minds != nil && !mind.IsZero() {
		if m, err := c.Minds.Get(ctx, mind); err == nil {
			id.PersonalityVersion = m.PersonalityVersion
		}
	}
	if c.LLM != nil {
		id.Routing = map[string]string{}
		models := map[ids.ID]router.Model{}
		if ms, err := c.LLM.ListModels(ctx); err == nil {
			for _, m := range ms {
				models[m.ID] = m
			}
		}
		if roles, err := c.LLM.ListRoles(ctx); err == nil {
			for _, r := range roles {
				if r.ModelID != nil {
					if m, ok := models[*r.ModelID]; ok {
						id.Routing[string(r.Role)] = m.Provider + "/" + m.Name
					}
				}
			}
		}
		if c.Minds != nil && !mind.IsZero() {
			if m, err := c.Minds.Get(ctx, mind); err == nil {
				for role, mid := range m.Config.LLMRoles {
					if md, ok := models[ids.ID(mid)]; ok {
						id.Routing[role] = md.Provider + "/" + md.Name + " (mind override)"
					}
				}
			}
		}
	}
	if c.Prompts != nil {
		if ps, err := c.Prompts.List(ctx); err == nil {
			id.PromptVersions = map[string]int{}
			for _, p := range ps {
				id.PromptVersions[p.Name] = p.CurrentVersion
			}
		}
	}
	if c.Capabilities != nil && !mind.IsZero() {
		if ps, err := c.Capabilities.Policies(ctx, mind); err == nil {
			id.PolicyDigest = policyDigest(ps)
		}
	}
	return id
}

func policyDigest(ps []capabilities.Policy) string {
	type row struct {
		Mind, Capability, Target, Action, Effect string
		From, To                                 *int
	}
	var rows []row
	for _, p := range ps {
		rows = append(rows, row{p.MindID.String(), p.Capability, p.TargetPattern, p.ActionKind, string(p.Effect), p.HoursFrom, p.HoursTo})
	}
	sort.Slice(rows, func(i, j int) bool { return fmt.Sprint(rows[i]) < fmt.Sprint(rows[j]) })
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// Compatibility labels for comparing the identity a snapshot was taken under
// with the identity a replay runs under.
const (
	CompatExactCandidate = "exact-candidate" // same schema and build; differences can only come from state or models
	CompatDifferentBuild = "different-build" // same schema, other code: exact steps are still exact, but divergence may be code
	CompatIncompatible   = "incompatible"    // schema differs: the sandbox cannot be a faithful reconstruction
)

// Compare lists the fields that differ and the resulting compatibility.
func Compare(recorded, current Identity) (string, []string) {
	var diffs []string
	if recorded.Build == (version.Build{}) && recorded.SchemaVersion == 0 {
		// Taken before identities were recorded: say so; do not compare blanks.
		return CompatDifferentBuild, []string{"snapshot identity not recorded (taken before 0.2); the comparison cannot rule out code differences"}
	}
	if recorded.SchemaVersion != 0 && recorded.SchemaVersion != current.SchemaVersion {
		diffs = append(diffs, fmt.Sprintf("schema %d → %d", recorded.SchemaVersion, current.SchemaVersion))
	}
	sameBuild := recorded.Build == current.Build
	if !sameBuild {
		diffs = append(diffs, fmt.Sprintf("build %s%s → %s%s", short(recorded.Build.Commit, recorded.Build.Version), dirty(recorded.Build.Modified), short(current.Build.Commit, current.Build.Version), dirty(current.Build.Modified)))
	}
	if recorded.ConfigDigest != "" && recorded.ConfigDigest != current.ConfigDigest {
		diffs = append(diffs, "configuration digest differs")
	}
	if recorded.PersonalityVersion != 0 && recorded.PersonalityVersion != current.PersonalityVersion {
		diffs = append(diffs, fmt.Sprintf("personality v%d → v%d", recorded.PersonalityVersion, current.PersonalityVersion))
	}
	if recorded.PolicyDigest != "" && recorded.PolicyDigest != current.PolicyDigest {
		diffs = append(diffs, "policy differs")
	}
	for role, m := range recorded.Routing {
		if cur := current.Routing[role]; cur != m {
			diffs = append(diffs, fmt.Sprintf("routing %s: %s → %s", role, m, orNone(cur)))
		}
	}
	for name, v := range recorded.PromptVersions {
		if cur := current.PromptVersions[name]; cur != v {
			diffs = append(diffs, fmt.Sprintf("prompt %s: v%d → v%d", name, v, cur))
		}
	}
	for comp, v := range recorded.Deterministic {
		if cur := current.Deterministic[comp]; cur != v {
			diffs = append(diffs, fmt.Sprintf("%s subsystem v%s → v%s", comp, v, orNone(cur)))
		}
	}
	if recorded.Timezone != "" && recorded.Timezone != current.Timezone {
		diffs = append(diffs, "timezone "+recorded.Timezone+" → "+current.Timezone)
	}
	sort.Strings(diffs)
	switch {
	case recorded.SchemaVersion != 0 && recorded.SchemaVersion != current.SchemaVersion:
		return CompatIncompatible, diffs
	case len(diffs) > 0:
		return CompatDifferentBuild, diffs
	}
	if current.Build.Modified {
		// Not a difference between the two runs, but worth saying: the commit
		// alone does not describe the code either of them ran.
		diffs = append(diffs, "note: built from a modified working tree")
	}
	return CompatExactCandidate, diffs
}

func short(commit, v string) string {
	if len(commit) >= 8 {
		return commit[:8]
	}
	if commit != "" {
		return commit
	}
	return v
}

func dirty(m bool) string {
	if m {
		return "+dirty"
	}
	return ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
