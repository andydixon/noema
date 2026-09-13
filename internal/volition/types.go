// Package volition is Noema's model of wanting, intending and committing
// (docs/volition.md). The four states it separates have different
// consequences:
//
//   - a want is a motivational preference with an origin, a strength that
//     decays and an urgency; it never implies action;
//   - an intention is a recorded decision to pursue an outcome, with its
//     sources, expected result, confidence, priority and status;
//   - a commitment is normative state (do or refrain) that shapes which
//     intentions form and how they may be carried out;
//   - an action is what the capability layer actually did, recorded there.
//
// Every rule here is deterministic Go code operating on plain inputs, so the
// same state gives the same decision, and every decision is stored with the
// terms that produced it. Nothing in this package can grant authority: it
// may describe what a mind wants to do, and the capability layer decides.
package volition

import (
	"context"
	"math"
	"time"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Origin says where a want came from.
type Origin string

// Origins.
const (
	OriginPredictionError Origin = "prediction_error"
	OriginContradiction   Origin = "contradiction"
	OriginGoal            Origin = "goal"
	OriginStalledGoal     Origin = "stalled_goal"
	OriginRequest         Origin = "request"
	OriginCommitment      Origin = "commitment"
	OriginCuriosity       Origin = "curiosity"
	OriginValue           Origin = "value"
	OriginOperator        Origin = "operator"
	OriginDream           Origin = "dream"
)

// WantStatus is the lifecycle of a want.
type WantStatus string

// Want statuses. Suppression is not a status: a suppressed want stays
// active and represented, and its suppression count rises.
const (
	WantActive     WantStatus = "active"
	WantSatisfied  WantStatus = "satisfied"
	WantExpired    WantStatus = "expired"
	WantAbandoned  WantStatus = "abandoned"
	WantSuperseded WantStatus = "superseded"
)

// Proposal is an optional concrete means attached to a want: a capability
// with target and parameters, or an investigation.
type Proposal struct {
	Kind       string         `json:"kind"` // capability | investigate
	Capability string         `json:"capability,omitempty"`
	Target     string         `json:"target,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Subject    string         `json:"subject,omitempty"` // what an investigation is about
}

// Want is a persisted motivational preference.
type Want struct {
	ID               ids.ID             `json:"id"`
	MindID           ids.ID             `json:"mind_id"`
	Subject          string             `json:"subject"`
	Description      string             `json:"description"`
	Origin           Origin             `json:"origin_type"`
	OriginEvent      ids.ID             `json:"origin_event,omitempty"`
	OriginRef        string             `json:"origin_ref"`
	Strength         float64            `json:"strength"`
	Valence          string             `json:"valence"` // approach | avoid
	Urgency          float64            `json:"urgency"`
	Status           WantStatus         `json:"status"`
	Drives           []string           `json:"drives"`
	Values           []string           `json:"values"`
	GoalID           ids.ID             `json:"goal_id,omitempty"`
	CommitmentID     ids.ID             `json:"commitment_id,omitempty"`
	Proposal         *Proposal          `json:"proposal,omitempty"`
	Terms            map[string]float64 `json:"terms"`
	Reinforcements   int                `json:"reinforcements"`
	Suppressions     int                `json:"suppressions"`
	LastReinforcedAt time.Time          `json:"last_reinforced_at"`
	Resolution       string             `json:"resolution"`
	ResolvedAt       *time.Time         `json:"resolved_at,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

// WantHalfLife is how quickly an unreinforced want fades.
const WantHalfLife = 6 * time.Hour

// Effective is the want's strength after decay since it was last reinforced.
func (w Want) Effective(now time.Time) float64 {
	el := now.Sub(w.LastReinforcedAt)
	if el <= 0 {
		return w.Strength
	}
	return round3(w.Strength * math.Pow(0.5, el.Hours()/WantHalfLife.Hours()))
}

// Conflict records two wants that cannot both be fully served.
type Conflict struct {
	ID            ids.ID         `json:"id"`
	MindID        ids.ID         `json:"mind_id"`
	WantA         ids.ID         `json:"want_a"`
	WantB         ids.ID         `json:"want_b"`
	Kind          string         `json:"kind"` // value | priority | commitment | exclusive
	Status        string         `json:"status"`
	Preferred     ids.ID         `json:"preferred,omitempty"`
	Resolution    string         `json:"resolution"`
	Rationale     map[string]any `json:"rationale"`
	DetectedEvent ids.ID         `json:"detected_event,omitempty"`
	ResolvedAt    *time.Time     `json:"resolved_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

// CommitmentKind says where a commitment's normative weight comes from.
type CommitmentKind string

// Commitment kinds.
const (
	KindPromise             CommitmentKind = "promise"
	KindOperatorInstruction CommitmentKind = "operator_instruction"
	KindResponsibility      CommitmentKind = "responsibility"
	KindValueRule           CommitmentKind = "value_rule"
	KindRelationship        CommitmentKind = "relationship"
	KindPriorDecision       CommitmentKind = "prior_decision"
	KindPolicy              CommitmentKind = "policy"
	KindScheduled           CommitmentKind = "scheduled"
)

// ValidKind reports whether k is known.
func ValidKind(k CommitmentKind) bool {
	switch k {
	case KindPromise, KindOperatorInstruction, KindResponsibility, KindValueRule, KindRelationship, KindPriorDecision, KindPolicy, KindScheduled:
		return true
	}
	return false
}

// Matcher describes the actions a commitment is about.
type Matcher struct {
	Capabilities []string `json:"capabilities,omitempty"` // globs over capability names
	Targets      []string `json:"targets,omitempty"`      // globs over targets
	Access       string   `json:"access,omitempty"`       // read | write | "" (any)
	MinRisk      string   `json:"min_risk,omitempty"`     // low | medium | high | critical | ""
}

// Commitment is normative state.
type Commitment struct {
	ID            ids.ID               `json:"id"`
	MindID        ids.ID               `json:"mind_id"`
	Subject       string               `json:"subject"`
	Description   string               `json:"description"`
	Kind          CommitmentKind       `json:"kind"`
	Polarity      string               `json:"polarity"` // do | refrain
	Matcher       Matcher              `json:"matcher"`
	Unless        string               `json:"unless"` // "" | approval
	Beneficiary   string               `json:"beneficiary"`
	Origin        string               `json:"origin_type"`
	OriginEvent   ids.ID               `json:"origin_event,omitempty"`
	OriginRef     string               `json:"origin_ref"`
	Strength      float64              `json:"strength"`
	Priority      float64              `json:"priority"`
	Conditions    string               `json:"conditions"`
	DueAt         *time.Time           `json:"due_at,omitempty"`
	ExpiresAt     *time.Time           `json:"expires_at,omitempty"`
	Revocable     bool                 `json:"revocable"`
	Status        string               `json:"status"`
	SupersededBy  ids.ID               `json:"superseded_by,omitempty"`
	GoalID        ids.ID               `json:"goal_id,omitempty"`
	IntentionID   ids.ID               `json:"intention_id,omitempty"`
	Evidence      []events.EvidenceRef `json:"evidence"`
	CreatedBy     string               `json:"created_by"`
	StatusReason  string               `json:"status_reason"`
	ClosedAt      *time.Time           `json:"closed_at,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
	DueNotifiedAt *time.Time           `json:"due_notified_at,omitempty"`
}

// kindFactor is the normative weight of each commitment kind. An operator
// instruction weighs most because a person with authority gave it; a prior
// decision of the mind's own weighs least because it is cheapest to revisit.
var kindFactor = map[CommitmentKind]float64{KindOperatorInstruction: 1, KindPolicy: 1, KindPromise: 0.9, KindResponsibility: 0.85, KindValueRule: 0.8, KindRelationship: 0.75, KindScheduled: 0.7, KindPriorDecision: 0.6}

// Weight is the commitment's normative weight in [0,1].
func (c Commitment) Weight() float64 {
	return round3(c.Strength * (0.5 + 0.5*c.Priority) * kindFactor[c.Kind])
}

// IntentionKind is what an intention sets out to do.
type IntentionKind string

// Intention kinds.
const (
	IntentAct               IntentionKind = "act"                // carry out a concrete capability action
	IntentInvestigate       IntentionKind = "investigate"        // reduce uncertainty with read-only operations
	IntentRequestApproval   IntentionKind = "request_approval"   // act, but only after an operator approves
	IntentRequestCapability IntentionKind = "request_capability" // obtain temporary authority first
	IntentFulfilCommitment  IntentionKind = "fulfil_commitment"
	IntentPursue            IntentionKind = "pursue" // advance a goal
	IntentRespond           IntentionKind = "respond"
)

// IntentionStatus is the lifecycle of an intention.
type IntentionStatus string

// Intention statuses.
const (
	IntentionProposed   IntentionStatus = "proposed"
	IntentionActive     IntentionStatus = "active"
	IntentionSuspended  IntentionStatus = "suspended"
	IntentionBlocked    IntentionStatus = "blocked"
	IntentionAbandoned  IntentionStatus = "abandoned"
	IntentionFulfilled  IntentionStatus = "fulfilled"
	IntentionSuperseded IntentionStatus = "superseded"
)

// Open reports whether an intention is still live.
func (s IntentionStatus) Open() bool {
	return s == IntentionProposed || s == IntentionActive || s == IntentionSuspended || s == IntentionBlocked
}

// Blocked reasons: the intention–action gap.
const (
	BlockApproval   = "approval_required"
	BlockCapability = "capability_unavailable"
	BlockResource   = "resource_unavailable"
	BlockDependency = "external_dependency"
	BlockWaiting    = "waiting_period"
	BlockPrereq     = "prerequisite_incomplete"
	BlockCommitment = "commitment"
	BlockVerify     = "outcome_unverified"
)

// Intention is a recorded decision to pursue an outcome.
type Intention struct {
	ID                   ids.ID          `json:"id"`
	MindID               ids.ID          `json:"mind_id"`
	ParentID             ids.ID          `json:"parent_id,omitempty"`
	Description          string          `json:"description"`
	DesiredOutcome       string          `json:"desired_outcome"`
	Kind                 IntentionKind   `json:"kind"`
	SourceWants          []ids.ID        `json:"source_wants"`
	GoalID               ids.ID          `json:"goal_id,omitempty"`
	CommitmentID         ids.ID          `json:"commitment_id,omitempty"`
	BeliefIDs            []ids.ID        `json:"belief_ids"`
	ExpectedResult       string          `json:"expected_result"`
	Confidence           float64         `json:"confidence"`
	Priority             float64         `json:"priority"`
	Status               IntentionStatus `json:"status"`
	BlockedKind          string          `json:"blocked_kind"`
	BlockedReason        string          `json:"blocked_reason"`
	RequiredCapabilities []string        `json:"required_capabilities"`
	RequireApproval      bool            `json:"require_approval"`
	Proposal             *Proposal       `json:"proposal,omitempty"`
	PlanID               ids.ID          `json:"plan_id,omitempty"`
	SupersededBy         ids.ID          `json:"superseded_by,omitempty"`
	FormedEvent          ids.ID          `json:"formed_event,omitempty"`
	LastEvent            ids.ID          `json:"last_event,omitempty"`
	Formation            map[string]any  `json:"formation"`
	Reconsiderations     int             `json:"reconsiderations"`
	StatusReason         string          `json:"status_reason"`
	ClosedAt             *time.Time      `json:"closed_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

// Decision is one append-only volitional decision.
type Decision struct {
	ID           ids.ID             `json:"id"`
	MindID       ids.ID             `json:"mind_id"`
	Kind         string             `json:"kind"`
	Outcome      string             `json:"outcome"`
	Reason       string             `json:"reason"`
	Rationale    string             `json:"rationale"`
	WantID       ids.ID             `json:"want_id,omitempty"`
	IntentionID  ids.ID             `json:"intention_id,omitempty"`
	CommitmentID ids.ID             `json:"commitment_id,omitempty"`
	ConflictID   ids.ID             `json:"conflict_id,omitempty"`
	Terms        map[string]float64 `json:"terms"`
	Inputs       map[string]any     `json:"inputs"`
	EventID      ids.ID             `json:"event_id,omitempty"`
	CreatedAt    time.Time          `json:"created_at"`
}

// Emitter appends an event, inside the caller's transaction when there is one.
type Emitter func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error)

// Recorder carries where and how a volitional change is recorded.
type Recorder struct {
	Q           storage.Querier
	Emit        Emitter
	Source      string // event source, e.g. "brain.volition" inside a cycle
	Cause       ids.ID // causation for emitted events
	Correlation ids.ID
	Now         time.Time
}

func (r Recorder) now() time.Time {
	if r.Now.IsZero() {
		return time.Now()
	}
	return r.Now
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
