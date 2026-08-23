// Package events defines the cognitive event vocabulary shared by every part
// of Noema. Events are the append-only record of what a mind perceived,
// thought, believed, predicted and did, and the provenance links between
// those things (ADR-0002).
package events

import (
	"encoding/json"
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

// Type identifies what kind of thing happened.
type Type string

// Event types. The list is closed: adding one means adding it here so that
// filters, metrics and the UI vocabulary stay in step.
const (
	Observation            Type = "observation"
	Perception             Type = "perception"
	ThoughtCandidate       Type = "thought_candidate"
	ThoughtSelected        Type = "thought_selected"
	WorkspaceBroadcast     Type = "workspace_broadcast"
	AttentionShift         Type = "attention_shift"
	MemoryCreated          Type = "memory_created"
	MemoryRetrieved        Type = "memory_retrieved"
	MemoryReinforced       Type = "memory_reinforced"
	MemoryDecayed          Type = "memory_decayed"
	MemoryConsolidated     Type = "memory_consolidated"
	BeliefCreated          Type = "belief_created"
	BeliefUpdated          Type = "belief_updated"
	BeliefChallenged       Type = "belief_challenged"
	BeliefRetracted        Type = "belief_retracted"
	HypothesisCreated      Type = "hypothesis_created"
	HypothesisSupported    Type = "hypothesis_supported"
	HypothesisWeakened     Type = "hypothesis_weakened"
	PredictionCreated      Type = "prediction_created"
	PredictionResolved     Type = "prediction_resolved"
	PredictionError        Type = "prediction_error"
	GoalCreated            Type = "goal_created"
	GoalUpdated            Type = "goal_updated"
	GoalCompleted          Type = "goal_completed"
	GoalAbandoned          Type = "goal_abandoned"
	DriveChanged           Type = "drive_changed"
	EmotionChanged         Type = "emotion_changed"
	Reflection             Type = "reflection"
	ContradictionDetected  Type = "contradiction_detected"
	ToolRequested          Type = "tool_requested"
	ToolAuthorised         Type = "tool_authorised"
	ToolDenied             Type = "tool_denied"
	ToolExecuted           Type = "tool_executed"
	ToolFailed             Type = "tool_failed"
	NotificationCreated    Type = "notification_created"
	NotificationSuppressed Type = "notification_suppressed"
	LLMRequest             Type = "llm_request"
	LLMResponse            Type = "llm_response"
	PolicyDecision         Type = "policy_decision"
	SelfModelUpdated       Type = "self_model_updated"
	DreamAssociation       Type = "dream_association"
	OperatorAction         Type = "operator_action"
	Metacognition          Type = "metacognition"
	NarrativeUpdated       Type = "narrative_updated"
	ModeChanged            Type = "mode_changed"
	ResponseGenerated      Type = "response_generated"
	CycleCompleted         Type = "cycle_completed"
	Redaction              Type = "redaction" // content of an earlier row was made irrecoverable under a policy
)

// Category groups types for filtering and colouring in the UI.
type Category string

// Categories.
const (
	CatPerception Category = "perception"
	CatCognition  Category = "cognition"
	CatMemory     Category = "memory"
	CatBelief     Category = "belief"
	CatPrediction Category = "prediction"
	CatGoal       Category = "goal"
	CatAffect     Category = "affect"
	CatAction     Category = "action"
	CatLLM        Category = "llm"
	CatOperator   Category = "operator"
	CatSystem     Category = "system"
)

var categories = map[Type]Category{
	Observation: CatPerception, Perception: CatPerception,
	ThoughtCandidate: CatCognition, ThoughtSelected: CatCognition, WorkspaceBroadcast: CatCognition, AttentionShift: CatCognition,
	Reflection: CatCognition, ContradictionDetected: CatCognition, Metacognition: CatCognition, DreamAssociation: CatCognition,
	NarrativeUpdated: CatCognition, ResponseGenerated: CatCognition, CycleCompleted: CatSystem, ModeChanged: CatSystem, Redaction: CatSystem,
	MemoryCreated: CatMemory, MemoryRetrieved: CatMemory, MemoryReinforced: CatMemory, MemoryDecayed: CatMemory, MemoryConsolidated: CatMemory,
	BeliefCreated: CatBelief, BeliefUpdated: CatBelief, BeliefChallenged: CatBelief, BeliefRetracted: CatBelief,
	HypothesisCreated: CatBelief, HypothesisSupported: CatBelief, HypothesisWeakened: CatBelief,
	PredictionCreated: CatPrediction, PredictionResolved: CatPrediction, PredictionError: CatPrediction,
	GoalCreated: CatGoal, GoalUpdated: CatGoal, GoalCompleted: CatGoal, GoalAbandoned: CatGoal,
	DriveChanged: CatAffect, EmotionChanged: CatAffect,
	ToolRequested: CatAction, ToolAuthorised: CatAction, ToolDenied: CatAction, ToolExecuted: CatAction, ToolFailed: CatAction,
	NotificationCreated: CatAction, NotificationSuppressed: CatAction, PolicyDecision: CatAction,
	LLMRequest: CatLLM, LLMResponse: CatLLM,
	SelfModelUpdated: CatSystem, OperatorAction: CatOperator,
}

// CategoryOf returns the category of a type (CatSystem for unknown types).
func CategoryOf(t Type) Category {
	if c, ok := categories[t]; ok {
		return c
	}
	return CatSystem
}

// Valid reports whether t is a known event type.
func (t Type) Valid() bool { _, ok := categories[t]; return ok }

// AllTypes lists every known type in a stable order.
func AllTypes() []Type {
	out := make([]Type, 0, len(categories))
	for t := range categories {
		out = append(out, t)
	}
	sortTypes(out)
	return out
}

func sortTypes(ts []Type) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j] < ts[j-1]; j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// EvidenceRef points at something that supports or explains an event: a
// memory, belief, another event, an entity, a document, an LLM call, a
// prediction, a message.
type EvidenceRef struct {
	Kind string `json:"kind"` // event | memory | belief | entity | document | llm_call | prediction | goal | message | approval
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

// Event is one immutable record.
type Event struct {
	ID            ids.ID          `json:"id"`
	Seq           int64           `json:"seq"`
	MindID        ids.ID          `json:"mind_id"`
	Type          Type            `json:"type"`
	Category      Category        `json:"category"`
	Source        string          `json:"source"` // process, integration or actor that produced it
	OccurredAt    time.Time       `json:"occurred_at"`
	RecordedAt    time.Time       `json:"recorded_at"`
	ParentIDs     []ids.ID        `json:"parent_ids"`
	CausationID   ids.ID          `json:"causation_id,omitempty"`   // the event that directly caused this one
	CorrelationID ids.ID          `json:"correlation_id,omitempty"` // cycle, conversation or job this belongs to
	Salience      float64         `json:"salience"`
	Confidence    float64         `json:"confidence"`
	Valence       *float64        `json:"valence,omitempty"`
	Arousal       *float64        `json:"arousal,omitempty"`
	Evidence      []EvidenceRef   `json:"evidence"`
	Summary       string          `json:"summary"`
	Metadata      map[string]any  `json:"metadata"`
	Payload       json.RawMessage `json:"payload"`
	RedactedAt    *time.Time      `json:"redacted_at,omitempty"` // content replaced by a marker; see redactions
}

// New describes an event to append. Zero OccurredAt means now.
type New struct {
	MindID        ids.ID
	Type          Type
	Source        string
	OccurredAt    time.Time
	ParentIDs     []ids.ID
	CausationID   ids.ID
	CorrelationID ids.ID
	Salience      float64
	Confidence    float64
	Valence       *float64
	Arousal       *float64
	Evidence      []EvidenceRef
	Summary       string
	Metadata      map[string]any
	Payload       any // marshalled to JSON; nil → {}
}

// ObservationPayload is the payload contract for Observation events. Every
// piece of external data carries its provenance and trust level (spec §25).
type ObservationPayload struct {
	Text        string `json:"text"`
	ContentType string `json:"content_type,omitempty"` // text/plain, text/markdown, application/json ...
	Source      string `json:"source"`                 // conversation, operator, research_folder, git, prometheus, webhook ...
	Trust       Trust  `json:"trust"`
	Method      string `json:"method,omitempty"`    // how it was ingested: chat, api, watch, poll, push
	Reference   string `json:"reference,omitempty"` // original locator: message id, file path, URL, commit
	Channel     string `json:"channel,omitempty"`   // conversation or integration identifier
	Actor       string `json:"actor,omitempty"`     // who said or produced it, if known
}

// Trust is the credibility class assigned to an external source. It never
// exceeds the trust of the channel the data arrived through.
type Trust string

// Trust levels, from most to least trusted.
const (
	TrustOperator Trust = "operator" // an authenticated operator speaking as themselves
	TrustUser     Trust = "user"     // an authenticated user in conversation
	TrustSystem   Trust = "system"   // Noema's own instrumentation
	TrustTool     Trust = "tool"     // output of a capability invocation
	TrustSource   Trust = "source"   // a configured integration (metrics, repo, feed)
	TrustLow      Trust = "low"      // arbitrary documents, web pages, unknown senders
)

// Valid reports whether t is a known trust level.
func (t Trust) Valid() bool {
	switch t {
	case TrustOperator, TrustUser, TrustSystem, TrustTool, TrustSource, TrustLow:
		return true
	}
	return false
}

// Credibility maps trust to a base weight used by belief and memory scoring.
func (t Trust) Credibility() float64 {
	switch t {
	case TrustOperator:
		return 0.95
	case TrustUser:
		return 0.85
	case TrustSystem:
		return 0.9
	case TrustTool:
		return 0.8
	case TrustSource:
		return 0.7
	default:
		return 0.4
	}
}
