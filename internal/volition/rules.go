package volition

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// ---- want derivation ----

// GoalInfo is what derivation needs to know about a goal.
type GoalInfo struct {
	ID       ids.ID
	Title    string
	Subject  string
	Kind     string
	Owner    string
	Priority float64
	DueAt    *time.Time
}

// DeriveContext is the state want derivation reads.
type DeriveContext struct {
	Now         time.Time
	Drives      map[string]float64 // current drive values
	Values      map[string]float64 // configured personality values
	Goals       map[ids.ID]GoalInfo
	Commitments map[ids.ID]Commitment
}

// Candidate is a want derivation proposes; the store creates or reinforces.
type Candidate struct {
	Subject      string
	Description  string
	Origin       Origin
	OriginEvent  ids.ID
	OriginRef    string
	Strength     float64
	Urgency      float64
	Valence      string
	Drives       []string
	Values       []string
	GoalID       ids.ID
	CommitmentID ids.ID
	Proposal     *Proposal
	Terms        map[string]float64
}

// WantThreshold is the minimum strength for a candidate to become a want.
// Below it nothing is stored, which keeps wants from accumulating.
const WantThreshold = 0.35

// strength combines a base with the named drives and values. With neutral
// drives and values (0.5) the result equals the base; the terms are kept.
func strength(base float64, dctx DeriveContext, drives, values []string) (float64, map[string]float64) {
	dm, vm := mean(dctx.Drives, drives), mean(dctx.Values, values)
	df, vf := 0.7+0.6*dm, 0.8+0.4*vm
	s := round3(clamp01(base * df * vf))
	return s, map[string]float64{"base": round3(base), "drive_mean": round3(dm), "drive_factor": round3(df), "value_mean": round3(vm), "value_factor": round3(vf), "strength": s}
}

func mean(m map[string]float64, keys []string) float64 {
	if len(keys) == 0 {
		return 0.5
	}
	sum := 0.0
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			v = 0.5
		}
		sum += v
	}
	return sum / float64(len(keys))
}

// Derive proposes wants from one event. Most events propose nothing: only
// states that call for something to change do (a failed prediction, a
// contradiction, a stalled or new operator goal, a question or request, a
// due commitment, an explicit action request).
func Derive(ev events.Event, dctx DeriveContext) []Candidate {
	meta := func(k string) string { s, _ := ev.Metadata[k].(string); return s }
	mk := func(base float64, c Candidate) []Candidate {
		c.Strength, c.Terms = strength(base, dctx, c.Drives, c.Values)
		c.OriginEvent = ev.ID
		if c.Valence == "" {
			c.Valence = "approach"
		}
		c.Urgency = round3(clamp01(c.Urgency))
		if c.Strength < WantThreshold {
			return nil
		}
		return []Candidate{c}
	}
	switch ev.Type {
	case events.PredictionError:
		pid := meta("prediction_id")
		if pid == "" {
			return nil
		}
		return mk(0.35+0.35*ev.Salience, Candidate{Subject: "understand:prediction:" + pid, Description: "understand why a prediction failed: " + trunc(ev.Summary, 160),
			Origin: OriginPredictionError, OriginRef: "prediction:" + pid, Urgency: 0.4, Drives: []string{"uncertainty_reduction", "curiosity"}, Values: []string{"accuracy", "evidence"},
			Proposal: &Proposal{Kind: "investigate", Subject: trunc(ev.Summary, 200)}})
	case events.ContradictionDetected:
		bid := meta("belief_id")
		if bid == "" {
			return nil
		}
		return mk(0.5, Candidate{Subject: "resolve:contradiction:" + bid, Description: "resolve a contradiction: " + trunc(strings.TrimPrefix(ev.Summary, "contradiction: "), 160),
			Origin: OriginContradiction, OriginRef: "belief:" + bid, Urgency: 0.35, Drives: []string{"consistency", "uncertainty_reduction"}, Values: []string{"evidence", "accuracy"},
			Proposal: &Proposal{Kind: "investigate", Subject: trunc(ev.Summary, 200)}})
	case events.GoalCreated:
		g, ok := dctx.Goals[ids.ID(meta("goal_id"))]
		if !ok || g.Owner != "operator" {
			return nil // curiosity and metacognition goals already express their own drive
		}
		c := Candidate{Subject: "achieve:goal:" + string(g.ID), Description: "achieve the goal: " + g.Title, Origin: OriginGoal, OriginRef: "goal:" + string(g.ID), GoalID: g.ID,
			Urgency: dueUrgency(g.DueAt, dctx.Now, 0.4), Drives: []string{"task_completion"}, Values: []string{"persistence"}}
		c.Proposal = ProposalFromText(g.Title, g.Subject)
		return mk(0.4+0.5*g.Priority, c)
	case events.GoalUpdated:
		if stalled, _ := ev.Metadata["stalled"].(bool); !stalled {
			return nil
		}
		g, ok := dctx.Goals[ids.ID(meta("goal_id"))]
		if !ok {
			return nil
		}
		return mk(0.3+0.4*g.Priority, Candidate{Subject: "unblock:goal:" + string(g.ID), Description: "find and remove what is stopping progress on: " + g.Title, Origin: OriginStalledGoal, OriginRef: "goal:" + string(g.ID), GoalID: g.ID,
			Urgency: dueUrgency(g.DueAt, dctx.Now, 0.3), Drives: []string{"task_completion"}, Values: []string{"persistence"}, Proposal: &Proposal{Kind: "investigate", Subject: firstNonEmpty(g.Subject, g.Title)}})
	case events.Perception:
		intent := meta("intent")
		conv, msg := meta("conversation_id"), meta("message_id")
		if conv == "" || msg == "" || (intent != "question" && intent != "request" && intent != "introspection") {
			return nil
		}
		actor := ""
		if strings.HasPrefix(ev.Summary, "perceived ") {
			if i := strings.Index(ev.Summary, " from "); i > 0 {
				actor = strings.SplitN(ev.Summary[i+6:], ":", 2)[0]
			}
		}
		return mk(0.55, Candidate{Subject: "answer:message:" + msg, Description: "give " + firstNonEmpty(actor, "the person") + " a useful, honest answer", Origin: OriginRequest, OriginRef: "message:" + msg,
			Urgency: 0.7, Drives: []string{"social_coherence"}, Values: []string{"kindness", "accuracy"}})
	case events.CommitmentDue:
		cid := ids.ID(meta("commitment_id"))
		c, ok := dctx.Commitments[cid]
		if !ok || c.Polarity != "do" {
			return nil
		}
		return mk(0.3+0.6*c.Weight(), Candidate{Subject: "fulfil:commitment:" + string(c.ID), Description: "fulfil the commitment: " + c.Description, Origin: OriginCommitment, OriginRef: "commitment:" + string(c.ID), CommitmentID: c.ID, GoalID: c.GoalID,
			Urgency: dueUrgency(c.DueAt, dctx.Now, 0.6), Drives: []string{"task_completion"}, Values: []string{"persistence"}, Proposal: ProposalFromText(c.Description, c.Subject)})
	}
	return nil
}

// FromRequest proposes a want for an explicit action request (for example
// "fetch <url>" in conversation), carrying the concrete means.
func FromRequest(ev events.Event, actor, text string, p Proposal, dctx DeriveContext, operator bool) []Candidate {
	base := 0.55
	if operator {
		base = 0.7
	}
	s, terms := strength(base, dctx, []string{"task_completion", "social_coherence"}, []string{"kindness"})
	if s < WantThreshold {
		return nil
	}
	return []Candidate{{Subject: "act:" + p.Capability + ":" + p.Target, Description: fmt.Sprintf("do what %s asked: %s", firstNonEmpty(actor, "someone"), trunc(text, 140)), Origin: OriginRequest, OriginEvent: ev.ID,
		OriginRef: "event:" + string(ev.ID), Strength: s, Terms: terms, Urgency: 0.6, Valence: "approach", Drives: []string{"task_completion", "social_coherence"}, Values: []string{"kindness"}, Proposal: &p}}
}

func dueUrgency(due *time.Time, now time.Time, base float64) float64 {
	if due == nil {
		return base
	}
	left := due.Sub(now)
	switch {
	case left <= 0:
		return 1
	case left < time.Hour:
		return 0.9
	case left < 6*time.Hour:
		return 0.75
	case left < 24*time.Hour:
		return 0.6
	}
	return base
}

var (
	urlRe       = regexp.MustCompile(`https?://[^\s<>"']+`)
	restartRe   = regexp.MustCompile(`(?i)\brestart\s+(?:the\s+)?(?:service\s+)?([A-Za-z0-9][A-Za-z0-9@_.:-]*)`)
	statusRe    = regexp.MustCompile(`(?i)\b(?:status|health)\s+of\s+(?:the\s+)?(?:service\s+)?([A-Za-z0-9][A-Za-z0-9@_.:-]*)`)
	logsRe      = regexp.MustCompile(`(?i)\blogs?\s+(?:of|for|from)\s+(?:the\s+)?(?:service\s+)?([A-Za-z0-9][A-Za-z0-9@_.:-]*)`)
	diagnoseRe  = regexp.MustCompile(`(?i)\b(?:diagnose|investigate|find out why|work out why|determine why)\b\s*(.*)`)
	trailingPun = ".,;:!?)"
)

// ProposalFromText recognises a few explicit request shapes in English. It
// is deliberately literal: anything it does not recognise gets no proposed
// means, and the want waits for an operator or a planner.
func ProposalFromText(text, subject string) *Proposal {
	lower := strings.ToLower(text)
	if u := urlRe.FindString(text); u != "" && (strings.Contains(lower, "fetch") || strings.Contains(lower, "read") || strings.Contains(lower, "look at") || strings.Contains(lower, "check")) {
		u = strings.TrimRight(u, trailingPun)
		return &Proposal{Kind: "capability", Capability: "http.fetch", Target: u, Params: map[string]any{"url": u}}
	}
	if m := restartRe.FindStringSubmatch(text); m != nil {
		unit := strings.TrimRight(m[1], trailingPun)
		return &Proposal{Kind: "capability", Capability: "service.restart", Target: unit, Params: map[string]any{"service": unit}}
	}
	if m := logsRe.FindStringSubmatch(text); m != nil {
		unit := strings.TrimRight(m[1], trailingPun)
		return &Proposal{Kind: "capability", Capability: "logs.query", Target: unit, Params: map[string]any{"service": unit}}
	}
	if m := statusRe.FindStringSubmatch(text); m != nil {
		unit := strings.TrimRight(m[1], trailingPun)
		return &Proposal{Kind: "capability", Capability: "service.status", Target: unit, Params: map[string]any{"service": unit}}
	}
	if m := diagnoseRe.FindStringSubmatch(text); m != nil {
		return &Proposal{Kind: "investigate", Subject: firstNonEmpty(subject, strings.TrimRight(strings.TrimSpace(m[1]), trailingPun))}
	}
	return nil
}

// ---- value-derived companions and conflicts ----

// ActionInfo describes a proposed capability action for commitment and
// value checks. Effect is the current policy effect (allow, ask, deny) or
// "locked" / "unknown".
type ActionInfo struct {
	Capability  string
	Target      string
	Access      string
	Risk        string
	Effect      string
	Idempotent  bool
	SideEffects string
	WallSeconds float64
}

var riskLevel = map[string]float64{"low": 0.1, "medium": 0.3, "high": 0.6, "critical": 0.9}

// Sensitive reports whether the action's output may carry personal data.
func (a ActionInfo) Sensitive() bool { return strings.Contains(a.SideEffects, "personal data") }

// Companions proposes value-derived "avoid" wants that accompany a proposed
// action: caution opposes disruptive writes, privacy opposes reading
// personal data. They do not veto anything; they make the tension explicit
// so conflict resolution can weigh it.
func Companions(approach Want, a ActionInfo, values map[string]float64) []Candidate {
	var out []Candidate
	if a.Access == "write" {
		caution := valueOr(values, "caution")
		if s := round3(clamp01(caution * (0.4 + riskLevel[a.Risk]))); s >= WantThreshold {
			out = append(out, Candidate{Subject: "avoid:disruption:" + a.Target, Description: "avoid unnecessary disruption to " + firstNonEmpty(a.Target, a.Capability), Origin: OriginValue, OriginRef: "value:caution",
				Strength: s, Urgency: 0.2, Valence: "avoid", Values: []string{"caution"}, Terms: map[string]float64{"caution": caution, "risk": riskLevel[a.Risk], "strength": s}})
		}
	}
	if a.Sensitive() {
		privacy := valueOr(values, "privacy")
		if s := round3(clamp01(privacy * 0.6)); s >= WantThreshold {
			out = append(out, Candidate{Subject: "avoid:exposure:" + a.Target, Description: "avoid exposing personal data held in " + firstNonEmpty(a.Target, a.Capability), Origin: OriginValue, OriginRef: "value:privacy",
				Strength: s, Urgency: 0.2, Valence: "avoid", Values: []string{"privacy"}, Terms: map[string]float64{"privacy": privacy, "strength": s}})
		}
	}
	return out
}

func valueOr(m map[string]float64, k string) float64 {
	if v, ok := m[k]; ok {
		return v
	}
	return 0.5
}

// Resolution is how a conflict between two wants was settled.
type Resolution struct {
	Preferred ids.ID
	Outcome   string // preferred | preferred_with_constraint | deferred
	Constrain bool   // the preferred approach must take the least disruptive, approved route
	Rationale map[string]any
}

// wantScore is what a want brings to a conflict.
func wantScore(w Want, now time.Time, goalPriority, commitmentWeight float64) (float64, map[string]float64) {
	eff := w.Effective(now)
	// A value-derived avoid want is standing, not time-bound: urgency does
	// not scale it.
	uf := 0.5 + 0.5*w.Urgency
	if w.Valence == "avoid" {
		uf = 1
	}
	t := map[string]float64{"effective_strength": eff, "urgency": w.Urgency, "urgency_factor": round3(uf), "goal_priority": goalPriority, "commitment_weight": commitmentWeight}
	score := eff*uf + 0.2*goalPriority + 0.4*commitmentWeight
	t["score"] = round3(score)
	return round3(score), t
}

// ResolveConflict settles two conflicting wants by explicit, recorded
// scores. There is no fixed hierarchy: a commitment-backed want gains its
// normative weight, a goal its priority, and urgency scales strength. An
// approach want opposed by a value-derived avoid want wins only with a
// constraint (least disruptive route, approval) unless it is clearly
// stronger; otherwise it is deferred, never discarded.
func ResolveConflict(a, b Want, now time.Time, goalPriority map[ids.ID]float64, commitmentWeight map[ids.ID]float64) Resolution {
	sa, ta := wantScore(a, now, goalPriority[a.ID], commitmentWeight[a.ID])
	sb, tb := wantScore(b, now, goalPriority[b.ID], commitmentWeight[b.ID])
	r := Resolution{Rationale: map[string]any{"a": map[string]any{"id": a.ID, "description": a.Description, "terms": ta}, "b": map[string]any{"id": b.ID, "description": b.Description, "terms": tb}}}
	if a.Valence != b.Valence {
		approach, avoid, sap, sav := a, b, sa, sb
		if a.Valence == "avoid" {
			approach, avoid, sap, sav = b, a, sb, sa
		}
		switch {
		case sap >= 1.5*sav:
			r.Preferred, r.Outcome = approach.ID, "preferred"
			r.Rationale["rule"] = fmt.Sprintf("approach score %.3f is at least 1.5× the opposing value's %.3f", sap, sav)
		case sap >= sav:
			r.Preferred, r.Outcome, r.Constrain = approach.ID, "preferred_with_constraint", true
			r.Rationale["rule"] = fmt.Sprintf("approach score %.3f ≥ opposing value %.3f but not decisively: pursue only by the least disruptive, approved route", sap, sav)
		default:
			r.Preferred, r.Outcome = avoid.ID, "deferred"
			r.Rationale["rule"] = fmt.Sprintf("opposing value %.3f outweighs approach %.3f: the approach want stays active but is not pursued now", sav, sap)
		}
		return r
	}
	if sa >= sb {
		r.Preferred = a.ID
	} else {
		r.Preferred = b.ID
	}
	r.Outcome = "preferred"
	r.Rationale["rule"] = fmt.Sprintf("higher score wins (%.3f vs %.3f); the other want stays active", maxf(sa, sb), minf(sa, sb))
	return r
}

// ---- commitments ----

func globAny(patterns []string, v string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if p == "*" || strings.EqualFold(p, v) {
			return true
		}
		if ok, _ := path.Match(p, v); ok {
			return true
		}
	}
	return false
}

// Matches reports whether a commitment's matcher covers an action.
func (c Commitment) Matches(a ActionInfo) bool {
	m := c.Matcher
	if len(m.Capabilities) == 0 && len(m.Targets) == 0 && m.Access == "" && m.MinRisk == "" {
		return false // an empty matcher is about no action in particular
	}
	if !globAny(m.Capabilities, a.Capability) || !globAny(m.Targets, a.Target) {
		return false
	}
	if m.Access != "" && m.Access != a.Access {
		return false
	}
	if m.MinRisk != "" && riskLevel[a.Risk] < riskLevel[m.MinRisk] {
		return false
	}
	return true
}

// Live reports whether a commitment currently binds.
func (c Commitment) Live(now time.Time) bool {
	return c.Status == "active" && (c.ExpiresAt == nil || now.Before(*c.ExpiresAt))
}

// ActionCheck is what live commitments say about one action.
type ActionCheck struct {
	Forbidden       []Commitment // refrain without exception
	RequireApproval []Commitment // refrain unless approved
}

// CheckAction applies live refrain commitments to a proposed action.
func CheckAction(cs []Commitment, a ActionInfo, now time.Time) ActionCheck {
	var out ActionCheck
	for _, c := range cs {
		if !c.Live(now) || c.Polarity != "refrain" || !c.Matches(a) {
			continue
		}
		if c.Unless == "approval" {
			out.RequireApproval = append(out.RequireApproval, c)
		} else {
			out.Forbidden = append(out.Forbidden, c)
		}
	}
	return out
}

// CommitmentConflict is a pair of live commitments that cannot both hold.
type CommitmentConflict struct {
	A, B   Commitment
	Reason string
}

// CommitmentConflicts finds do/refrain pairs over the same actions and
// opposite polarities on the same subject.
func CommitmentConflicts(cs []Commitment, now time.Time) []CommitmentConflict {
	var out []CommitmentConflict
	for i := range cs {
		for j := i + 1; j < len(cs); j++ {
			a, b := cs[i], cs[j]
			if !a.Live(now) || !b.Live(now) || a.Polarity == b.Polarity {
				continue
			}
			do, refrain := a, b
			if a.Polarity == "refrain" {
				do, refrain = b, a
			}
			switch {
			case strings.EqualFold(a.Subject, b.Subject):
				out = append(out, CommitmentConflict{A: a, B: b, Reason: "same subject with opposite polarity"})
			case refrain.Unless == "" && len(do.Matcher.Capabilities) > 0 && overlaps(do.Matcher, refrain.Matcher):
				out = append(out, CommitmentConflict{A: do, B: refrain, Reason: "one commits to an action the other forbids"})
			}
		}
	}
	return out
}

func overlaps(a, b Matcher) bool {
	capOK := false
	for _, c := range a.Capabilities {
		if globAny(b.Capabilities, c) {
			capOK = true
		}
	}
	if !capOK {
		return false
	}
	if len(a.Targets) == 0 || len(b.Targets) == 0 {
		return true
	}
	for _, t := range a.Targets {
		if globAny(b.Targets, t) {
			return true
		}
	}
	return false
}

// CommitmentState is the time-based assessment of one commitment.
type CommitmentState string

// Time-based states.
const (
	StateFine    CommitmentState = "fine"
	StateDue     CommitmentState = "due"     // inside the due window, not yet notified
	StateOverdue CommitmentState = "overdue" // past due and grace: broken unless fulfilled
	StateExpired CommitmentState = "expired"
)

// DueWindow is how long before its deadline a commitment becomes due;
// DueGrace is how long after before it counts as broken.
const (
	DueWindow = 2 * time.Hour
	DueGrace  = 15 * time.Minute
)

// Assess classifies a live commitment against the clock.
func Assess(c Commitment, now time.Time) CommitmentState {
	if c.Status != "active" {
		return StateFine
	}
	if c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) {
		return StateExpired
	}
	if c.DueAt == nil || c.Polarity != "do" {
		return StateFine
	}
	if now.After(c.DueAt.Add(DueGrace)) {
		return StateOverdue
	}
	if c.DueNotifiedAt == nil && now.After(c.DueAt.Add(-DueWindow)) {
		return StateDue
	}
	return StateFine
}

// Impossible explains why a do-commitment cannot be fulfilled with the
// capabilities the mind has, or returns "".
func Impossible(c Commitment, effects map[string]string, now time.Time) string {
	if c.Status != "active" || c.Polarity != "do" {
		return ""
	}
	if c.DueAt != nil && c.DueAt.Before(c.CreatedAt) {
		return "its deadline was already past when it was made"
	}
	for _, name := range c.Matcher.Capabilities {
		if strings.ContainsAny(name, "*?[") {
			continue
		}
		switch effects[name] {
		case "deny", "locked":
			return "it needs " + name + ", which policy denies"
		case "", "unknown":
			return "it needs " + name + ", which is not a registered capability"
		}
	}
	return ""
}

// ---- intention formation ----

// FormationInput is everything formation weighs. It is plain data so the
// decision can be replayed and tested without a database.
type FormationInput struct {
	Want              Want
	Now               time.Time
	Goal              *GoalInfo
	CommitmentWeight  float64 // weight of the commitment this want serves, if any
	Commitments       []Commitment
	Values            map[string]float64
	Action            *ActionInfo // present when the want proposes a capability action
	ReadCapabilities  int         // usable read-only capabilities, for investigations
	Active            []Intention // open intentions of the mind
	MaxActive         int
	Threshold         float64
	Confidence        float64 // expected chance the intention achieves its outcome
	Constrained       bool    // a conflict resolution demanded the least disruptive, approved route
	Deferred          bool    // a conflict resolution deferred this want
	IgnoreCommitments bool    // ablation: commitments do not influence formation
	Operator          bool    // the want came from an operator
}

// Formation outcomes.
const (
	Formed    = "formed"
	NotFormed = "not_formed"
)

// Formation reasons for not forming.
const (
	ReasonBelowThreshold = "below_threshold"
	ReasonForbidden      = "forbidden_by_commitment"
	ReasonDenied         = "capability_denied"
	ReasonUnavailable    = "capability_unavailable"
	ReasonNoPlan         = "no_possible_plan"
	ReasonCapacity       = "capacity"
	ReasonDuplicate      = "duplicate"
	ReasonDeferred       = "deferred_by_conflict"
)

// Formation is the deterministic result.
type Formation struct {
	Outcome         string
	Reason          string
	Kind            IntentionKind
	Description     string
	DesiredOutcome  string
	RequireApproval bool
	Applied         []ids.ID // commitments that shaped the decision
	Suppress        bool     // record the want as suppressed
	Displace        ids.ID   // open intention to suspend in favour of this one
	Score           float64
	Priority        float64
	Terms           map[string]float64
	Rationale       string
}

// DefaultFormThreshold and DefaultMaxActive are formation defaults.
const (
	DefaultFormThreshold = 0.3
	DefaultMaxActive     = 5
)

// Form decides whether a want becomes an intention, of what kind, and why.
func Form(in FormationInput) Formation {
	if in.Threshold <= 0 {
		in.Threshold = DefaultFormThreshold
	}
	if in.MaxActive <= 0 {
		in.MaxActive = DefaultMaxActive
	}
	if in.Confidence <= 0 {
		in.Confidence = 0.5
	}
	w := in.Want
	f := Formation{Terms: map[string]float64{}, Kind: IntentPursue, Description: strings.TrimPrefix(w.Description, "do what "), DesiredOutcome: w.Description}
	for _, it := range in.Active {
		for _, src := range it.SourceWants {
			if src == w.ID {
				f.Outcome, f.Reason, f.Rationale = NotFormed, ReasonDuplicate, "an open intention already serves this want"
				return f
			}
		}
	}
	if in.Deferred {
		f.Outcome, f.Reason, f.Suppress = NotFormed, ReasonDeferred, true
		f.Rationale = "a conflicting want outweighed this one; it stays active and may be pursued later"
		return f
	}
	caution, privacy, autonomy := valueOr(in.Values, "caution"), valueOr(in.Values, "privacy"), valueOr(in.Values, "autonomy")
	eff := w.Effective(in.Now)
	t := f.Terms
	t["strength"] = round3(0.35 * eff)
	t["urgency"] = round3(0.15 * w.Urgency)
	if in.Goal != nil {
		t["goal"] = round3(0.15 * in.Goal.Priority)
	}
	t["commitment"] = round3(0.15 * in.CommitmentWeight)
	t["expected_utility"] = round3(0.1 * in.Confidence)
	if !in.Operator && w.Origin != OriginRequest {
		t["autonomy"] = round3(0.05 * autonomy)
	}
	var why []string
	switch {
	case in.Action != nil:
		a := *in.Action
		f.Kind, f.Description = IntentAct, describeAction(a)
		if w.CommitmentID != "" {
			f.Kind = IntentFulfilCommitment
		}
		switch a.Effect {
		case "locked", "unknown", "":
			f.Outcome, f.Reason, f.Suppress = NotFormed, ReasonUnavailable, true
			f.Rationale = a.Capability + " is not a capability this mind can use (" + firstNonEmpty(a.Effect, "unknown") + ")"
			return f
		case "deny":
			f.Outcome, f.Reason, f.Suppress = NotFormed, ReasonDenied, true
			f.Rationale = "policy denies " + a.Capability + " on " + firstNonEmpty(a.Target, "this target") + "; the want stays represented, and denied authority is not requested"
			return f
		}
		t["risk"] = round3(-0.2 * riskLevel[a.Risk] * (0.5 + caution))
		t["cost"] = round3(-0.05 * clamp01(a.WallSeconds/60))
		if a.Sensitive() {
			t["privacy"] = round3(-0.1 * privacy)
		}
		if !in.IgnoreCommitments {
			chk := CheckAction(in.Commitments, a, in.Now)
			if len(chk.Forbidden) > 0 {
				f.Outcome, f.Reason, f.Suppress = NotFormed, ReasonForbidden, true
				for _, c := range chk.Forbidden {
					f.Applied = append(f.Applied, c.ID)
				}
				f.Rationale = "commitment \"" + chk.Forbidden[0].Description + "\" forbids " + describeAction(a) + "; the want stays active and no intention to act is formed"
				return f
			}
			if len(chk.RequireApproval) > 0 {
				f.RequireApproval, f.Kind = true, IntentRequestApproval
				for _, c := range chk.RequireApproval {
					f.Applied = append(f.Applied, c.ID)
				}
				f.Description = "ask for approval to " + describeAction(a)
				why = append(why, "commitment \""+chk.RequireApproval[0].Description+"\" requires approval, so no immediate action is intended; the intention is to request approval")
			}
		}
		if in.Constrained && !f.RequireApproval {
			f.RequireApproval, f.Kind = true, IntentRequestApproval
			f.Description = "ask for approval to " + describeAction(a)
			why = append(why, "a conflicting value-derived want requires the least disruptive, approved route")
		}
		if a.Effect == "ask" && !f.RequireApproval {
			why = append(why, "policy asks for approval, so the action will wait for an operator")
		}
	case w.Proposal != nil && w.Proposal.Kind == "investigate":
		f.Kind, f.Description = IntentInvestigate, "investigate "+firstNonEmpty(w.Proposal.Subject, w.Description)
		if in.ReadCapabilities == 0 {
			f.Outcome, f.Reason = NotFormed, ReasonNoPlan
			f.Rationale = "no read-only capability is available to investigate with; the want stays active"
			return f
		}
	case w.Origin == OriginRequest && w.Proposal == nil:
		f.Kind, f.Description = IntentRespond, strings.TrimPrefix(w.Description, "give ")
	case w.CommitmentID != "":
		f.Kind = IntentFulfilCommitment
	}
	score := 0.0
	for _, v := range t {
		score += v
	}
	f.Score = round3(score)
	t["score"] = f.Score
	f.Priority = round3(clamp01(0.2 + 0.8*score))
	if f.Score < in.Threshold {
		f.Outcome, f.Reason = NotFormed, ReasonBelowThreshold
		f.Rationale = fmt.Sprintf("score %.3f is below the formation threshold %.2f", f.Score, in.Threshold)
		return f
	}
	open := 0
	var weakest *Intention
	for i := range in.Active {
		it := &in.Active[i]
		if it.Status != IntentionActive && it.Status != IntentionBlocked {
			continue
		}
		open++
		if weakest == nil || it.Priority < weakest.Priority {
			weakest = it
		}
	}
	if open >= in.MaxActive {
		if weakest != nil && f.Priority > weakest.Priority+0.1 {
			f.Displace = weakest.ID
			why = append(why, fmt.Sprintf("capacity reached; priority %.2f exceeds the weakest open intention's %.2f, which is suspended", f.Priority, weakest.Priority))
		} else {
			f.Outcome, f.Reason = NotFormed, ReasonCapacity
			f.Rationale = fmt.Sprintf("%d intentions are already open and none is weaker by more than 0.1", open)
			return f
		}
	}
	f.Outcome = Formed
	why = append([]string{fmt.Sprintf("score %.3f ≥ threshold %.2f", f.Score, in.Threshold)}, why...)
	f.Rationale = strings.Join(why, "; ")
	return f
}

func describeAction(a ActionInfo) string {
	if a.Target == "" {
		return "run " + a.Capability
	}
	return "run " + a.Capability + " on " + a.Target
}

// ---- reconsideration ----

// Trigger kinds.
const (
	TriggerPlanCompleted     = "plan_completed"
	TriggerStepFailed        = "step_failed"
	TriggerExpectationFailed = "expectation_failed"
	TriggerApprovalDenied    = "approval_denied"
	TriggerApprovalExpired   = "approval_expired"
	TriggerCapabilityChanged = "capability_changed"
	TriggerCommitmentChanged = "commitment_changed"
	TriggerResourceExhausted = "resource_exhausted"
	TriggerBeliefWeakened    = "belief_weakened"
	TriggerGoalCompleted     = "goal_completed"
	TriggerGoalAbandoned     = "goal_abandoned"
	TriggerOperator          = "operator_correction"
	TriggerSuperior          = "superior_alternative"
	TriggerWorldChanged      = "world_changed"
	TriggerRequestGranted    = "request_granted"
	TriggerReplanLimit       = "replan_limit"
)

// Trigger is a reason to reconsider an intention.
type Trigger struct {
	Kind    string
	EventID ids.ID
	Detail  string
	// Action is set for commitment and capability triggers so the rule can
	// check whether the change touches this intention's means.
	Action *ActionInfo
	// Commitment is set for commitment triggers.
	Commitment *Commitment
	// OutcomeObserved is set for world_changed when the desired state already
	// holds, and for plan_completed when the plan's completion means the
	// desired outcome was reached.
	OutcomeObserved bool
}

// Reconsideration outcomes.
const (
	Keep    = "keep"
	Block   = "block"
	Suspend = "suspend"
	Resume  = "resume"
	Abandon = "abandon"
	Fulfil  = "fulfil"
	Replan  = "replan"
	Tighten = "require_approval"
)

// Reconsideration is the deterministic result.
type Reconsideration struct {
	Outcome     string
	BlockedKind string
	Reason      string
}

// ReconsiderContext bounds replanning.
type ReconsiderContext struct {
	Replans    int // revisions already made
	MaxReplans int // default 3
	Now        time.Time
}

// Reconsider decides what happens to an intention when something relevant
// changed. It never rewrites what the intention was; the store records the
// transition as a new event and decision.
func Reconsider(it Intention, t Trigger, rc ReconsiderContext) Reconsideration {
	if rc.MaxReplans <= 0 {
		rc.MaxReplans = 3
	}
	replanOr := func(reason string) Reconsideration {
		if rc.Replans >= rc.MaxReplans {
			return Reconsideration{Outcome: Abandon, Reason: fmt.Sprintf("%s, and %d replans have already been tried", reason, rc.Replans)}
		}
		return Reconsideration{Outcome: Replan, Reason: reason}
	}
	switch t.Kind {
	case TriggerPlanCompleted:
		if !t.OutcomeObserved {
			return Reconsideration{Outcome: Abandon, Reason: "the plan finished without achieving the desired outcome (" + firstNonEmpty(t.Detail, "fallback strategy") + "); it has been handed to the operator"}
		}
		return Reconsideration{Outcome: Fulfil, Reason: "the plan completed and its checks passed"}
	case TriggerGoalCompleted:
		if it.Kind == IntentPursue || it.Kind == IntentInvestigate {
			return Reconsideration{Outcome: Fulfil, Reason: "the goal it served was completed"}
		}
		return Reconsideration{Outcome: Abandon, Reason: "the goal it served was completed, so the action is no longer needed"}
	case TriggerGoalAbandoned:
		return Reconsideration{Outcome: Abandon, Reason: "the goal it served was abandoned"}
	case TriggerOperator:
		return Reconsideration{Outcome: Abandon, Reason: "an operator corrected it: " + t.Detail}
	case TriggerApprovalDenied:
		return replanOr("the operator denied the request (" + firstNonEmpty(t.Detail, "no reason given") + ")")
	case TriggerApprovalExpired:
		if rc.Replans > 0 {
			return Reconsideration{Outcome: Suspend, Reason: "approval expired again without a decision; waiting rather than asking the operator repeatedly"}
		}
		return replanOr("the approval request expired before anyone decided")
	case TriggerCapabilityChanged:
		if t.Action != nil && (t.Action.Effect == "deny" || t.Action.Effect == "locked" || t.Action.Effect == "unknown") {
			return replanOr(t.Action.Capability + " is no longer available (" + t.Action.Effect + ")")
		}
		if t.Action != nil && t.Action.Effect == "ask" {
			return Reconsideration{Outcome: Block, BlockedKind: BlockApproval, Reason: t.Action.Capability + " now needs approval"}
		}
		return Reconsideration{Outcome: Keep, Reason: "capability change does not affect this intention"}
	case TriggerRequestGranted:
		if it.Status == IntentionBlocked || it.Status == IntentionSuspended {
			return Reconsideration{Outcome: Resume, Reason: "the operator granted the requested authority"}
		}
		return Reconsideration{Outcome: Keep, Reason: "already active"}
	case TriggerCommitmentChanged:
		if t.Commitment == nil || t.Action == nil || !t.Commitment.Live(rc.Now) || t.Commitment.Polarity != "refrain" || !t.Commitment.Matches(*t.Action) {
			return Reconsideration{Outcome: Keep, Reason: "the commitment does not concern this intention's action"}
		}
		if t.Commitment.Unless == "approval" {
			if it.RequireApproval {
				return Reconsideration{Outcome: Keep, Reason: "already waiting for approval"}
			}
			return Reconsideration{Outcome: Tighten, Reason: "a new commitment requires approval for " + describeAction(*t.Action)}
		}
		return Reconsideration{Outcome: Abandon, Reason: "a new commitment forbids " + describeAction(*t.Action) + ": " + t.Commitment.Description}
	case TriggerResourceExhausted:
		return Reconsideration{Outcome: Suspend, BlockedKind: BlockResource, Reason: "resource budget exhausted: " + t.Detail}
	case TriggerStepFailed, TriggerExpectationFailed, TriggerBeliefWeakened:
		return replanOr(firstNonEmpty(t.Detail, t.Kind))
	case TriggerWorldChanged:
		if t.OutcomeObserved {
			return Reconsideration{Outcome: Fulfil, Reason: "the desired state already holds (" + t.Detail + "); acting is unnecessary"}
		}
		return replanOr("the target changed outside Noema: " + t.Detail)
	case TriggerSuperior:
		return Reconsideration{Outcome: Suspend, Reason: "a higher-priority intention needs the capacity: " + t.Detail}
	case TriggerReplanLimit:
		return Reconsideration{Outcome: Abandon, Reason: "replanning limit reached"}
	}
	return Reconsideration{Outcome: Keep, Reason: "no rule for " + t.Kind}
}

// ---- helpers ----

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// SortWants orders wants by effective strength × urgency, strongest first.
func SortWants(ws []Want, now time.Time) {
	sort.SliceStable(ws, func(i, j int) bool {
		return ws[i].Effective(now)*(0.5+0.5*ws[i].Urgency) > ws[j].Effective(now)*(0.5+0.5*ws[j].Urgency)
	})
}
