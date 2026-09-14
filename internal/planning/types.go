// Package planning turns intentions into explicit, revisable plans and
// carries them out (docs/planning.md). A plan is a dependency graph of steps
// with prerequisites, capabilities, expected observations, risk, resource
// estimates, completion criteria, abort conditions and alternatives. Go code
// owns validation, dependency integrity, the capability vocabulary, policy
// checks (through the capability layer), resource accounting, execution
// state, retries, deadlines and approvals. A language model may propose a
// decomposition; it is parsed into the same structure and validated like any
// other draft, and it can never name a capability that is not registered.
//
// Plans are not promises and not authority. Every action goes through
// capabilities.Service.Invoke, and replanning creates a new revision while
// the old plan stays as history.
package planning

import (
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

// Plan statuses.
const (
	PlanDraft            = "draft"
	PlanValidated        = "validated"
	PlanWaiting          = "waiting"
	PlanExecuting        = "executing"
	PlanBlocked          = "blocked"
	PlanAwaitingApproval = "awaiting_approval"
	PlanReplanning       = "replanning"
	PlanCompleted        = "completed"
	PlanFailed           = "failed"
	PlanAbandoned        = "abandoned"
	PlanSuperseded       = "superseded"
)

// PlanOpen reports whether a plan may still make progress.
func PlanOpen(status string) bool {
	switch status {
	case PlanValidated, PlanWaiting, PlanExecuting, PlanBlocked, PlanAwaitingApproval, PlanReplanning:
		return true
	}
	return false
}

// Step kinds.
const (
	KindCapability        = "capability"
	KindRequestCapability = "request_capability"
	KindVerify            = "verify"
	KindWait              = "wait"
	KindMonitor           = "monitor"
	KindInternal          = "internal"
)

// Step statuses.
const (
	StepPending          = "pending"
	StepReady            = "ready"
	StepExecuting        = "executing"
	StepAwaitingApproval = "awaiting_approval"
	StepAwaitingGrant    = "awaiting_grant"
	StepWaiting          = "waiting"
	StepCompleted        = "completed"
	StepFailed           = "failed"
	StepSkipped          = "skipped"
	StepBlocked          = "blocked"
	StepCancelled        = "cancelled"
)

// StepDone reports whether a step no longer blocks its dependants.
func StepDone(status string) bool { return status == StepCompleted || status == StepSkipped }

// StepTerminal reports whether a step will not change again by itself.
func StepTerminal(status string) bool {
	return status == StepCompleted || status == StepSkipped || status == StepFailed || status == StepCancelled
}

// Check observes the world through a read-only capability and compares one
// value of its structured result.
type Check struct {
	Capability  string         `json:"capability"`
	Target      string         `json:"target,omitempty"`
	Params      map[string]any `json:"params,omitempty"`
	Path        string         `json:"path"`            // dot path into the result data, e.g. "active" or "properties.ActiveState"
	Op          string         `json:"op"`              // eq | ne | lt | lte | gt | gte | contains | exists
	Value       any            `json:"value,omitempty"` // compared value
	Description string         `json:"description,omitempty"`
}

// Expectation is what a consequential step should bring about. It becomes a
// prediction when the step completes; a verify step resolves it.
type Expectation struct {
	Description   string  `json:"description"`
	Confidence    float64 `json:"confidence"`
	WithinSeconds int     `json:"within_seconds"`
	BeliefID      ids.ID  `json:"belief_id,omitempty"` // belief the expectation rests on; a miss is evidence against it
}

// Precondition must hold before a step acts. When it does not, the step is
// skipped: if SatisfiedIfFalse, because the desired state already holds.
type Precondition struct {
	Check            Check `json:"check"`
	SatisfiedIfFalse bool  `json:"satisfied_if_false"`
}

// RequestSpec is a structured capability request a step raises.
type RequestSpec struct {
	Capability       string            `json:"capability"`
	TargetPattern    string            `json:"target_pattern"`
	ParamConstraints map[string]string `json:"param_constraints,omitempty"`
	DurationSeconds  int               `json:"duration_seconds"`
	MaxOperations    *int              `json:"max_operations,omitempty"`
	Reason           string            `json:"reason"`
	Risk             string            `json:"risk"`
	Mitigation       string            `json:"mitigation"`
	Fallback         string            `json:"fallback"`
	Alternatives     []string          `json:"alternatives,omitempty"`
}

// VerifySpec resolves another step's expectation by observation.
type VerifySpec struct {
	Of              string `json:"of"` // key of the step whose expectation is checked
	Check           Check  `json:"check"`
	WithinSeconds   int    `json:"within_seconds"`   // keep checking until this long after the action
	IntervalSeconds int    `json:"interval_seconds"` // between checks
}

// MonitorSpec repeats a check until it holds continuously for StableSeconds,
// or fails at DeadlineSeconds.
type MonitorSpec struct {
	Check           Check `json:"check"`
	IntervalSeconds int   `json:"interval_seconds"`
	StableSeconds   int   `json:"stable_seconds"`
	DeadlineSeconds int   `json:"deadline_seconds"`
}

// WaitSpec pauses a branch.
type WaitSpec struct {
	Seconds int `json:"seconds"`
}

// InternalSpec is a deterministic internal step.
type InternalSpec struct {
	Action string `json:"action"` // summarise_findings | record_procedure
}

// InternalActions is the closed vocabulary of internal steps.
var InternalActions = map[string]string{
	"summarise_findings": "collect the observations of the steps it depends on into one record for belief formation",
	"record_procedure":   "record the steps that achieved the outcome as a procedural memory",
}

// StepSpec is one step of a draft.
type StepSpec struct {
	Key             string         `json:"key"`
	Title           string         `json:"title"`
	Kind            string         `json:"kind"`
	DependsOn       []string       `json:"depends_on,omitempty"`
	Capability      string         `json:"capability,omitempty"`
	Target          string         `json:"target,omitempty"`
	Params          map[string]any `json:"params,omitempty"`
	RequireApproval bool           `json:"require_approval,omitempty"`
	MaxAttempts     int            `json:"max_attempts,omitempty"`
	Expectation     *Expectation   `json:"expectation,omitempty"`
	Precondition    *Precondition  `json:"precondition,omitempty"`
	Request         *RequestSpec   `json:"request,omitempty"`
	Verify          *VerifySpec    `json:"verify,omitempty"`
	Monitor         *MonitorSpec   `json:"monitor,omitempty"`
	Wait            *WaitSpec      `json:"wait,omitempty"`
	Internal        *InternalSpec  `json:"internal,omitempty"`
}

// Assumption is a belief the plan depends on. If the belief falls below
// MinConfidence the plan is reconsidered.
type Assumption struct {
	BeliefID      ids.ID  `json:"belief_id,omitempty"`
	Proposition   string  `json:"proposition"`
	MinConfidence float64 `json:"min_confidence,omitempty"`
}

// Resources are estimated or consumed resources.
type Resources struct {
	Invocations int     `json:"capability_invocations"`
	Approvals   int     `json:"human_approvals"`
	WallSeconds float64 `json:"wall_seconds"`
	Network     int     `json:"network_operations"`
	Money       float64 `json:"money"`
	Tokens      int     `json:"llm_tokens"`
}

// Budget caps a plan's consumption. Zero fields are unlimited except
// Invocations, which defaults to 20 so no plan can loop without bound.
type Budget struct {
	MaxInvocations int     `json:"max_capability_invocations"`
	MaxWallSeconds float64 `json:"max_wall_seconds,omitempty"`
	MaxMoney       float64 `json:"max_money,omitempty"`
	MaxApprovals   int     `json:"max_human_approvals,omitempty"`
}

// Alternative is a strategy the planner considered.
type Alternative struct {
	Strategy           string    `json:"strategy"`
	Description        string    `json:"description"`
	Estimate           Resources `json:"estimate"`
	ExpectedConfidence float64   `json:"expected_confidence"`
	Score              float64   `json:"score"`
	Selected           bool      `json:"selected"`
	Why                string    `json:"why,omitempty"`
}

// Draft is a proposed plan before it is stored.
type Draft struct {
	Objective          string        `json:"objective"`
	Origin             string        `json:"origin"` // template | model | operator | dream | replan
	Strategy           string        `json:"strategy"`
	Steps              []StepSpec    `json:"steps"`
	Assumptions        []Assumption  `json:"assumptions,omitempty"`
	CompletionCriteria string        `json:"completion_criteria"`
	AbortConditions    []string      `json:"abort_conditions,omitempty"`
	Alternatives       []Alternative `json:"alternatives,omitempty"`
	Risk               string        `json:"risk"`
	Budget             Budget        `json:"budget"`
	RequireApproval    bool          `json:"require_approval,omitempty"`
	LLMCall            ids.ID        `json:"llm_call,omitempty"`
	Ignored            []string      `json:"ignored,omitempty"` // fields of a model proposal that were not accepted
	Estimate           *Resources    `json:"estimate,omitempty"`
}

// Plan is a stored plan.
type Plan struct {
	ID                 ids.ID        `json:"id"`
	MindID             ids.ID        `json:"mind_id"`
	IntentionID        ids.ID        `json:"intention_id"`
	Revision           int           `json:"revision"`
	Supersedes         ids.ID        `json:"supersedes,omitempty"`
	SupersededBy       ids.ID        `json:"superseded_by,omitempty"`
	Objective          string        `json:"objective"`
	Origin             string        `json:"origin"`
	Strategy           string        `json:"strategy"`
	Status             string        `json:"status"`
	Assumptions        []Assumption  `json:"assumptions"`
	CompletionCriteria string        `json:"completion_criteria"`
	AbortConditions    []string      `json:"abort_conditions"`
	Alternatives       []Alternative `json:"alternatives"`
	Risk               string        `json:"risk"`
	Estimate           Resources     `json:"estimate"`
	Actual             Resources     `json:"actual"`
	Budget             Budget        `json:"budget"`
	Validation         Validation    `json:"validation"`
	RequireApproval    bool          `json:"require_approval"`
	MaxReplans         int           `json:"max_replans"`
	LLMCall            ids.ID        `json:"llm_call,omitempty"`
	StatusReason       string        `json:"status_reason"`
	ClosedAt           *time.Time    `json:"closed_at,omitempty"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	Steps              []Step        `json:"steps,omitempty"`
}

// Step is a stored step with its lifecycle state.
type Step struct {
	ID              ids.ID         `json:"id"`
	PlanID          ids.ID         `json:"plan_id"`
	MindID          ids.ID         `json:"mind_id"`
	Key             string         `json:"key"`
	Position        int            `json:"position"`
	Title           string         `json:"title"`
	Kind            string         `json:"kind"`
	DependsOn       []string       `json:"depends_on"`
	Capability      string         `json:"capability"`
	Target          string         `json:"target"`
	Params          map[string]any `json:"params"`
	RequireApproval bool           `json:"require_approval"`
	Expectation     *Expectation   `json:"expectation,omitempty"`
	Precondition    *Precondition  `json:"precondition,omitempty"`
	Spec            StepSpec       `json:"spec"`
	Status          string         `json:"status"`
	Attempts        int            `json:"attempts"`
	MaxAttempts     int            `json:"max_attempts"`
	LeaseUntil      *time.Time     `json:"lease_until,omitempty"`
	NextRunAt       *time.Time     `json:"next_run_at,omitempty"`
	ApprovalID      ids.ID         `json:"approval_id,omitempty"`
	RequestID       ids.ID         `json:"request_id,omitempty"`
	ExecutionID     ids.ID         `json:"execution_id,omitempty"`
	PredictionID    ids.ID         `json:"prediction_id,omitempty"`
	IntentionID     ids.ID         `json:"intention_id,omitempty"`
	Result          map[string]any `json:"result,omitempty"`
	Observation     map[string]any `json:"observation,omitempty"`
	Error           string         `json:"error"`
	StatusReason    string         `json:"status_reason"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Origin returns the origin string recorded on invocations for this step.
func (s Step) Origin() string { return "plan_step:" + string(s.ID) }
