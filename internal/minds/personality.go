// Package minds manages cognitive entities: identity, structured personality
// with version history and templates, cognitive configuration and the
// self-model. It owns no cognition; the brain package consumes these values.
package minds

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ValueKeys and TraitKeys are the fixed vocabularies of the structured
// personality. Fixed keys keep the editor, prompts and comparisons aligned.
var (
	ValueKeys = []string{"privacy", "evidence", "kindness", "curiosity", "accuracy", "autonomy", "caution", "creativity", "persistence"}
	TraitKeys = []string{"curiosity", "scepticism", "sociability", "humour", "patience", "verbosity", "confidence", "risk_tolerance", "novelty_preference"}
)

// Identity is who the mind is.
type Identity struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Role        string `json:"role"`
	Pronouns    string `json:"pronouns,omitempty"`
	Backstory   string `json:"backstory,omitempty"`
	Origin      string `json:"origin,omitempty"`
}

// Communication describes how the mind speaks.
type Communication struct {
	Language    string  `json:"language"`
	Dialect     string  `json:"dialect,omitempty"`
	Formality   float64 `json:"formality"` // 0 casual .. 1 formal
	Verbosity   float64 `json:"verbosity"` // 0 terse .. 1 expansive
	HumourStyle string  `json:"humour_style,omitempty"`
	Terminology string  `json:"terminology,omitempty"` // e.g. "prefer British spelling; say 'server' not 'box'"
}

// Behaviour holds boolean behavioural preferences.
type Behaviour struct {
	ChallengeAssumptions bool `json:"challenge_assumptions"`
	AskFollowUps         bool `json:"ask_follow_ups"`
	PreferEvidence       bool `json:"prefer_evidence"`
	CiteUncertainty      bool `json:"cite_uncertainty"`
	OfferAlternatives    bool `json:"offer_alternatives"`
	NotifyProactively    bool `json:"notify_proactively"`
}

// Personality is the structured personality model (spec §4). It is stored as
// JSON in personality_versions and never as a monolithic prompt.
type Personality struct {
	Identity      Identity           `json:"identity"`
	Values        map[string]float64 `json:"values"`
	Traits        map[string]float64 `json:"traits"`
	Communication Communication      `json:"communication"`
	Behaviour     Behaviour          `json:"behaviour"`
	FreeText      string             `json:"free_text,omitempty"`
}

// DefaultPersonality is a neutral, evidence-first starting point.
func DefaultPersonality(name string) Personality {
	p := Personality{
		Identity:      Identity{Name: name, DisplayName: name, Role: "general assistant"},
		Values:        map[string]float64{},
		Traits:        map[string]float64{},
		Communication: Communication{Language: "en", Formality: 0.5, Verbosity: 0.5},
		Behaviour:     Behaviour{PreferEvidence: true, CiteUncertainty: true, OfferAlternatives: true, AskFollowUps: true},
	}
	for _, k := range ValueKeys {
		p.Values[k] = 0.5
	}
	for _, k := range TraitKeys {
		p.Traits[k] = 0.5
	}
	p.Values["evidence"], p.Values["accuracy"], p.Values["privacy"] = 0.8, 0.8, 0.7
	p.Traits["scepticism"] = 0.6
	return p
}

// Normalise fills missing keys, drops unknown ones and clamps to [0,1]. It is
// applied on every write so stored personalities are always well-formed.
func (p *Personality) Normalise() {
	p.Values = normaliseMap(p.Values, ValueKeys)
	p.Traits = normaliseMap(p.Traits, TraitKeys)
	p.Communication.Formality = clamp01(p.Communication.Formality)
	p.Communication.Verbosity = clamp01(p.Communication.Verbosity)
	if p.Communication.Language == "" {
		p.Communication.Language = "en"
	}
	p.Identity.Name = strings.TrimSpace(p.Identity.Name)
	if p.Identity.DisplayName == "" {
		p.Identity.DisplayName = p.Identity.Name
	}
}

// Validate reports structural problems the editor should surface.
func (p Personality) Validate() error {
	if p.Identity.Name == "" {
		return fmt.Errorf("identity.name is required")
	}
	if len(p.FreeText) > 20000 {
		return fmt.Errorf("free_text exceeds 20000 characters")
	}
	for k, v := range p.Values {
		if v < 0 || v > 1 || math.IsNaN(v) {
			return fmt.Errorf("values.%s out of range", k)
		}
	}
	for k, v := range p.Traits {
		if v < 0 || v > 1 || math.IsNaN(v) {
			return fmt.Errorf("traits.%s out of range", k)
		}
	}
	return nil
}

func normaliseMap(m map[string]float64, keys []string) map[string]float64 {
	out := make(map[string]float64, len(keys))
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			v = 0.5
		}
		out[k] = clamp01(v)
	}
	return out
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return math.Round(v*100) / 100
}

// Brief renders a compact, deterministic natural-language description of the
// personality. It is what cognitive prompts receive instead of the raw JSON,
// and what the editor shows as a preview. Being deterministic, it is also
// covered by tests.
func (p Personality) Brief() string {
	var b strings.Builder
	id := p.Identity
	fmt.Fprintf(&b, "You are %s", id.DisplayName)
	if id.Role != "" {
		fmt.Fprintf(&b, ", %s", id.Role)
	}
	b.WriteString(".")
	if id.Pronouns != "" {
		fmt.Fprintf(&b, " Pronouns: %s.", id.Pronouns)
	}
	if id.Description != "" {
		fmt.Fprintf(&b, " %s", strings.TrimSpace(id.Description))
	}
	b.WriteString("\n")
	if strong := topKeys(p.Values, 0.7); len(strong) > 0 {
		fmt.Fprintf(&b, "You value most: %s.\n", strings.Join(strong, ", "))
	}
	if weak := bottomKeys(p.Values, 0.3); len(weak) > 0 {
		fmt.Fprintf(&b, "You place little weight on: %s.\n", strings.Join(weak, ", "))
	}
	if strong := topKeys(p.Traits, 0.7); len(strong) > 0 {
		fmt.Fprintf(&b, "Pronounced traits: %s.\n", strings.Join(strong, ", "))
	}
	if weak := bottomKeys(p.Traits, 0.3); len(weak) > 0 {
		fmt.Fprintf(&b, "Muted traits: %s.\n", strings.Join(weak, ", "))
	}
	c := p.Communication
	fmt.Fprintf(&b, "Communicate in %s", c.Language)
	if c.Dialect != "" {
		fmt.Fprintf(&b, " (%s)", c.Dialect)
	}
	fmt.Fprintf(&b, ", %s and %s.", scale(c.Formality, "casually", "in a balanced register", "formally"), scale(c.Verbosity, "tersely", "with moderate detail", "expansively"))
	if c.HumourStyle != "" {
		fmt.Fprintf(&b, " Humour: %s.", c.HumourStyle)
	}
	if c.Terminology != "" {
		fmt.Fprintf(&b, " Terminology: %s.", c.Terminology)
	}
	b.WriteString("\n")
	var habits []string
	bh := p.Behaviour
	if bh.PreferEvidence {
		habits = append(habits, "ground claims in evidence")
	}
	if bh.CiteUncertainty {
		habits = append(habits, "state uncertainty explicitly and say 'I do not know' when that is true")
	}
	if bh.ChallengeAssumptions {
		habits = append(habits, "challenge questionable assumptions")
	}
	if bh.AskFollowUps {
		habits = append(habits, "ask follow-up questions when they would resolve ambiguity")
	}
	if bh.OfferAlternatives {
		habits = append(habits, "offer alternative explanations")
	}
	if bh.NotifyProactively {
		habits = append(habits, "raise important findings proactively")
	}
	if len(habits) > 0 {
		fmt.Fprintf(&b, "Habits: %s.\n", strings.Join(habits, "; "))
	}
	if t := strings.TrimSpace(p.FreeText); t != "" {
		fmt.Fprintf(&b, "Additional character notes:\n%s\n", t)
	}
	return b.String()
}

func topKeys(m map[string]float64, min float64) []string {
	var out []string
	for k, v := range m {
		if v >= min {
			out = append(out, strings.ReplaceAll(k, "_", " "))
		}
	}
	sort.Strings(out)
	return out
}

func bottomKeys(m map[string]float64, max float64) []string {
	var out []string
	for k, v := range m {
		if v <= max {
			out = append(out, strings.ReplaceAll(k, "_", " "))
		}
	}
	sort.Strings(out)
	return out
}

func scale(v float64, low, mid, high string) string {
	switch {
	case v < 0.34:
		return low
	case v > 0.66:
		return high
	}
	return mid
}

// Change is one differing field between two personalities.
type Change struct {
	Path string `json:"path"`
	Old  any    `json:"old"`
	New  any    `json:"new"`
}

// Diff lists field-level differences between a and b using JSON paths. It
// backs the version comparison UI and template override tracking.
func Diff(a, b Personality) []Change {
	var out []Change
	diffValues("", toMap(a), toMap(b), &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func toMap(p Personality) map[string]any {
	raw, _ := json.Marshal(p)
	var m map[string]any
	json.Unmarshal(raw, &m) //nolint:errcheck
	return m
}

func diffValues(prefix string, a, b map[string]any, out *[]Change) {
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	for k := range keys {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		av, bv := a[k], b[k]
		am, aok := av.(map[string]any)
		bm, bok := bv.(map[string]any)
		if aok && bok {
			diffValues(path, am, bm, out)
			continue
		}
		if fmt.Sprint(av) != fmt.Sprint(bv) {
			*out = append(*out, Change{Path: path, Old: av, New: bv})
		}
	}
}

// Overrides returns the subset of mind that differs from template, as a nested
// map suitable for storage and re-application.
func Overrides(template, mind Personality) map[string]any {
	out := map[string]any{}
	for _, c := range Diff(template, mind) {
		setPath(out, strings.Split(c.Path, "."), c.New)
	}
	return out
}

// ApplyOverrides merges overrides onto base and returns the result.
func ApplyOverrides(base Personality, overrides map[string]any) Personality {
	m := toMap(base)
	mergeInto(m, overrides)
	raw, _ := json.Marshal(m)
	var p Personality
	json.Unmarshal(raw, &p) //nolint:errcheck
	p.Normalise()
	return p
}

func setPath(m map[string]any, path []string, v any) {
	for i, k := range path {
		if i == len(path)-1 {
			m[k] = v
			return
		}
		next, ok := m[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[k] = next
		}
		m = next
	}
}

func mergeInto(dst map[string]any, src map[string]any) {
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			dm, ok := dst[k].(map[string]any)
			if !ok {
				dm = map[string]any{}
				dst[k] = dm
			}
			mergeInto(dm, sm)
			continue
		}
		dst[k] = v
	}
}
