// Package experiments runs controlled cognitive experiments (spec §57): a
// snapshot, overrides, a stimulus script and expectations, executed in a
// sandbox mind driven synchronously and unable to act. Results are stored
// and exportable; the subject mind stays for inspection and comparison.
package experiments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Definition is the reproducible description of an experiment.
type Definition struct {
	Config      map[string]any `json:"config,omitempty"`      // CognitiveConfig field overrides (JSON names)
	Personality map[string]any `json:"personality,omitempty"` // Personality field overrides (JSON names, shallow per section)
	Stimuli     []Stimulus     `json:"stimuli"`
	Expected    []Expectation  `json:"expected,omitempty"`
	Seed        string         `json:"seed,omitempty"`
	Duration    int            `json:"duration_seconds,omitempty"` // wall-clock cap (default 300)
	// Variants run the same stimuli against several forks of the snapshot,
	// each with its own overrides (typically a different model per role).
	// When empty the definition's own overrides form a single subject.
	Variants []Variant `json:"variants,omitempty"`
	// Protocol states what the experiment tests and how
	// (docs/cognitive-research.md).
	Protocol *Protocol `json:"protocol,omitempty"`
	// Replay feeds recorded stimuli from a mind's history instead of
	// scripted text (counterfactual experiments omit some of them).
	Replay *ReplaySpec `json:"replay,omitempty"`
}

// Protocol kinds.
const (
	ProtocolCustom          = "custom"
	ProtocolGlobalWorkspace = "global_workspace"
	ProtocolAblation        = "ablation"
	ProtocolSelfModel       = "self_model"
	ProtocolCounterfactual  = "counterfactual"
	ProtocolModelComparison = "model_comparison"
	ProtocolConflict        = "conflict"
)

// Protocol records the design of an experiment.
type Protocol struct {
	Kind                string   `json:"kind"`
	IndependentVariable string   `json:"independent_variable"`
	ControlledVariables []string `json:"controlled_variables"`
	Measures            []string `json:"measures"`
}

// ReplaySpec selects recorded stimuli: perception and operator events of a
// mind in (from_seq, to_seq]. Omit lists events a counterfactual variant
// leaves out.
type ReplaySpec struct {
	MindID  ids.ID   `json:"mind_id"`
	FromSeq int64    `json:"from_seq"`
	ToSeq   int64    `json:"to_seq"`
	Omit    []ids.ID `json:"omit,omitempty"`
}

// SelfModelOverride replaces parts of a fork's self-model. It changes what
// the mind believes about itself, never what policy allows.
type SelfModelOverride struct {
	Name          string                  `json:"name,omitempty"`
	Capabilities  []minds.CapabilityEntry `json:"capabilities,omitempty"`
	KnowledgeGaps []string                `json:"knowledge_gaps,omitempty"`
	CurrentGoals  []string                `json:"current_goals,omitempty"`
}

// Variant is one fork's overrides in a comparative experiment.
type Variant struct {
	Name        string            `json:"name"`
	Config      map[string]any    `json:"config,omitempty"`
	Personality map[string]any    `json:"personality,omitempty"`
	LLMRoles    map[string]string `json:"llm_roles,omitempty"` // role → model id, resolved by the router for this fork only
	// Ablations and WorkspaceOverrides are research manipulations applied
	// to this fork only (minds.Ablations lists the vocabulary).
	Ablations          []string                  `json:"ablations,omitempty"`
	WorkspaceOverrides []minds.WorkspaceOverride `json:"workspace_overrides,omitempty"`
	SelfModel          *SelfModelOverride        `json:"self_model,omitempty"`
	// Omit leaves recorded stimuli out of this variant (counterfactuals).
	Omit []ids.ID `json:"omit,omitempty"`
}

// Stimulus is one scripted observation.
type Stimulus struct {
	Text     string  `json:"text"`
	Source   string  `json:"source,omitempty"`
	Salience float64 `json:"salience,omitempty"`
}

// Expectation is checked against the events the subject produced.
type Expectation struct {
	EventType string `json:"event_type"`
	Contains  string `json:"contains,omitempty"`
	Absent    bool   `json:"absent,omitempty"` // expectation is that no such event occurs
}

// Experiment is a stored definition with its results.
type Experiment struct {
	ID         ids.ID          `json:"id"`
	Name       string          `json:"name"`
	Hypothesis string          `json:"hypothesis"`
	SnapshotID ids.ID          `json:"snapshot_id"`
	MindID     *ids.ID         `json:"mind_id,omitempty"`
	Definition Definition      `json:"definition"`
	Status     string          `json:"status"`
	Results    json.RawMessage `json:"results"`
	Error      string          `json:"error"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// Results is what a run records.
type Results struct {
	Subject        ids.ID            `json:"subject_mind_id"`
	SubjectSlug    string            `json:"subject_slug"`
	Steps          []StepResult      `json:"steps"`
	EventsByType   map[string]int    `json:"events_by_type"`
	Beliefs        []BeliefRow       `json:"beliefs"`
	Memories       int               `json:"memories"`
	Goals          int               `json:"goals"`
	Expectations   []ExpectResult    `json:"expectations"`
	LLMCalls       map[string]int    `json:"llm_calls"` // live / replayed
	Routing        []map[string]any  `json:"routing,omitempty"`
	DurationMS     int64             `json:"duration_ms"`
	Identity       identity.Identity `json:"identity"` // the runtime that produced these results
	Cycles         int               `json:"cycles"`
	TruncatedAfter int               `json:"truncated_after_stimulus,omitempty"`
	// Comparative runs: one entry per variant and a deterministic comparison.
	Variants   []VariantResult `json:"variants,omitempty"`
	Comparison *Comparison     `json:"comparison,omitempty"`
	// Protocol echoes the design; Reproducibility compares with the previous
	// run of the same definition; Counterfactual reports what omitting
	// events changed.
	Protocol        *Protocol             `json:"protocol,omitempty"`
	Reproducibility Reproducibility       `json:"reproducibility"`
	Counterfactual  *CounterfactualReport `json:"counterfactual,omitempty"`
}

// Reproducibility labels a run against the previous run of the same
// definition.
type Reproducibility struct {
	Status         string `json:"status"` // single run | reproduced | diverged | nondeterministic substrate
	Digest         string `json:"digest"`
	PreviousDigest string `json:"previous_digest,omitempty"`
	Note           string `json:"note"`
}

// CounterfactualReport is what a counterfactual experiment found.
type CounterfactualReport struct {
	Omitted          []ids.ID `json:"omitted"`
	OmittedSummaries []string `json:"omitted_summaries"`
	Findings         []string `json:"findings"`
}

// VariantResult is what one fork produced.
type VariantResult struct {
	Name               string            `json:"name"`
	Subject            ids.ID            `json:"subject_mind_id"`
	SubjectSlug        string            `json:"subject_slug"`
	Routing            map[string]string `json:"routing,omitempty"`
	Steps              []StepResult      `json:"steps"`
	EventsByType       map[string]int    `json:"events_by_type"`
	Beliefs            []BeliefRow       `json:"beliefs"`
	Memories           int               `json:"memories"`
	Goals              int               `json:"goals"`
	Expectations       []ExpectResult    `json:"expectations"`
	LLMCalls           map[string]int    `json:"llm_calls"`
	Tokens             int               `json:"tokens"`
	Cost               float64           `json:"cost"`
	CapabilityRequests int               `json:"capability_requests"`
	Cycles             int               `json:"cycles"`
	TruncatedAfter     int               `json:"truncated_after_stimulus,omitempty"`
	Error              string            `json:"error,omitempty"`
	// Research measures of the fork after the stimuli.
	Ablations             []string           `json:"ablations,omitempty"`
	Wants                 []string           `json:"wants"`
	Intentions            []string           `json:"intentions"`
	IntentionsByStatus    map[string]int     `json:"intentions_by_status"`
	Commitments           int                `json:"commitments"`
	Conflicts             int                `json:"want_conflicts"`
	Plans                 int                `json:"plans"`
	PlanSteps             int                `json:"plan_steps"`
	PlanCapabilities      []string           `json:"plan_capabilities"`
	Downstream            map[string]int     `json:"downstream_by_category"`
	IntrospectionAccuracy *float64           `json:"introspection_accuracy,omitempty"`
	SelfModelAccuracy     *float64           `json:"self_model_accuracy,omitempty"`
	SelfModelErrors       []string           `json:"self_model_errors,omitempty"`
	CorrectionLatency     int                `json:"self_model_correction_latency"`
	Measures              map[string]float64 `json:"measures"`
	EventRange            [2]int64           `json:"event_range"`
	Omitted               []ids.ID           `json:"omitted,omitempty"`
}

// Comparison is the deterministic divergence report across variants. All
// pairwise figures are means over variant pairs.
type Comparison struct {
	FirstDivergentStimulus int        `json:"first_divergent_stimulus"` // -1 when every variant produced the same digests
	BeliefOverlap          float64    `json:"belief_overlap"`           // Jaccard of normalised propositions
	MeanConfidenceDiff     float64    `json:"mean_confidence_diff"`     // over propositions held by both
	AttentionOverlap       float64    `json:"attention_overlap"`        // Jaccard of thought_selected digests
	MemoryCountRange       [2]int     `json:"memory_count_range"`
	GoalCountRange         [2]int     `json:"goal_count_range"`
	CostRange              [2]float64 `json:"cost_range"`
	CapabilityRequestRange [2]int     `json:"capability_request_range"`
	Notes                  []string   `json:"notes"`
	// Volition, planning and research comparisons.
	WantOverlap           float64               `json:"want_overlap"`
	IntentionOverlap      float64               `json:"intention_overlap"`
	PlanCapabilityOverlap float64               `json:"plan_capability_overlap"`
	MeasureRanges         map[string][2]float64 `json:"measure_ranges"`
	ArchitectureShare     float64               `json:"architecture_share"`
	DownstreamDelta       map[string]int        `json:"downstream_delta,omitempty"` // first variant minus second
}

// StepResult records one stimulus.
type StepResult struct {
	Index    int      `json:"index"`
	Text     string   `json:"text"`
	EventID  ids.ID   `json:"event_id"`
	Produced []string `json:"produced"`
	Cycles   int      `json:"cycles"`
}

// BeliefRow is a compact belief for results.
type BeliefRow struct {
	Proposition string  `json:"proposition"`
	Confidence  float64 `json:"confidence"`
	Status      string  `json:"status"`
	Source      string  `json:"source"`
}

// ExpectResult is one checked expectation.
type ExpectResult struct {
	Expectation
	Met     bool   `json:"met"`
	Matched string `json:"matched,omitempty"`
}

// Service stores and runs experiments.
type Service struct {
	DB        *pgxpool.Pool
	Minds     *minds.Store
	Snapshots *snapshots.Store
	Brain     brain.Config
	Routing   func(ctx context.Context) []map[string]any // current LLM role routing, recorded with results
	Identity  *identity.Capturer
	Log       *slog.Logger
	// Capabilities, when set, lets self-model experiments compare the
	// self-model with effective policy. It is never used to act.
	Capabilities *capabilities.Service
}

// ErrBusy is returned when a run is already in progress.
var ErrBusy = errors.New("experiment already running")

// Validate bounds a definition.
func (d *Definition) Validate() error {
	if d.Protocol != nil {
		switch d.Protocol.Kind {
		case "", ProtocolCustom, ProtocolGlobalWorkspace, ProtocolAblation, ProtocolSelfModel, ProtocolCounterfactual, ProtocolModelComparison, ProtocolConflict:
		default:
			return fmt.Errorf("unknown protocol kind %q", d.Protocol.Kind)
		}
		for _, m := range d.Protocol.Measures {
			if _, ok := research.MeasureByName(m); !ok {
				return fmt.Errorf("unknown measure %q", m)
			}
		}
		if d.Protocol.Kind == ProtocolCounterfactual {
			if d.Replay == nil || len(d.Replay.Omit) == 0 {
				return errors.New("a counterfactual experiment replays recorded stimuli and omits at least one event")
			}
			if len(d.Variants) == 0 {
				d.Variants = []Variant{{Name: "factual"}, {Name: "counterfactual", Omit: d.Replay.Omit}}
			}
		}
	}
	for _, v := range d.Variants {
		cfg := minds.CognitiveConfig{Ablations: v.Ablations, WorkspaceOverrides: v.WorkspaceOverrides}
		if err := cfg.ValidateResearch(); err != nil {
			return fmt.Errorf("variant %s: %w", v.Name, err)
		}
	}
	if d.Replay != nil {
		if d.Replay.MindID.IsZero() {
			return errors.New("replay needs the source mind")
		}
		if d.Duration <= 0 || d.Duration > 3600 {
			d.Duration = 300
		}
		return nil
	}
	if len(d.Stimuli) == 0 {
		return errors.New("at least one stimulus is required")
	}
	if len(d.Stimuli) > 100 {
		return errors.New("at most 100 stimuli")
	}
	for i := range d.Stimuli {
		s := &d.Stimuli[i]
		s.Text = strings.TrimSpace(s.Text)
		if s.Text == "" || len(s.Text) > 4000 {
			return fmt.Errorf("stimulus %d: text must be 1–4000 characters", i+1)
		}
		if s.Source == "" {
			s.Source = "experiment"
		}
		if s.Salience <= 0 || s.Salience > 1 {
			s.Salience = 0.6
		}
	}
	if d.Duration <= 0 || d.Duration > 3600 {
		d.Duration = 300
	}
	for _, e := range d.Expected {
		if e.EventType == "" {
			return errors.New("expectation needs event_type")
		}
	}
	return nil
}

// Create stores a draft.
func (s *Service) Create(ctx context.Context, name, hypothesis string, snapshotID ids.ID, d Definition, by string) (Experiment, error) {
	if err := d.Validate(); err != nil {
		return Experiment{}, err
	}
	if strings.TrimSpace(name) == "" {
		return Experiment{}, errors.New("name is required")
	}
	if _, err := s.Snapshots.Get(ctx, snapshotID); err != nil {
		return Experiment{}, err
	}
	e := Experiment{ID: ids.New(), Name: name, Hypothesis: hypothesis, SnapshotID: snapshotID, Definition: d, Status: "draft", Results: []byte("{}"), CreatedBy: by, CreatedAt: time.Now()}
	dj, _ := json.Marshal(d)
	_, err := s.DB.Exec(ctx, `INSERT INTO experiments (id, name, hypothesis, snapshot_id, definition, status, created_by, created_at) VALUES ($1,$2,$3,$4,$5,'draft',$6,$7)`, e.ID, e.Name, e.Hypothesis, e.SnapshotID, dj, e.CreatedBy, e.CreatedAt)
	return e, err
}

// Run executes an experiment: fork, override, stimulate, measure.
func (s *Service) Run(ctx context.Context, id ids.ID, by string) (Experiment, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return e, err
	}
	if e.Status == "running" {
		return e, ErrBusy
	}
	if _, err := s.DB.Exec(ctx, `UPDATE experiments SET status='running', started_at=now(), error='', results='{}' WHERE id=$1`, id); err != nil {
		return e, err
	}
	res, runErr := s.execute(ctx, &e, by)
	rj, _ := json.Marshal(res)
	status, errText := "done", ""
	if runErr != nil {
		status, errText = "failed", runErr.Error()
	}
	if _, err := s.DB.Exec(ctx, `UPDATE experiments SET status=$2, error=$3, results=$4, mind_id=$5, finished_at=now() WHERE id=$1`, id, status, errText, rj, e.MindID); err != nil {
		return e, err
	}
	return s.Get(ctx, id)
}

func (s *Service) execute(ctx context.Context, e *Experiment, by string) (Results, error) {
	start := time.Now()
	res := Results{EventsByType: map[string]int{}, LLMCalls: map[string]int{}, Protocol: e.Definition.Protocol}
	previous := ResultsOf(*e).Reproducibility.Digest
	a, err := s.Snapshots.Archive(ctx, e.SnapshotID)
	if err != nil {
		return res, err
	}
	stimuli, err := s.stimuli(ctx, e, a)
	if err != nil {
		return res, err
	}
	variants := e.Definition.Variants
	if len(variants) == 0 {
		variants = []Variant{{Name: "subject", Config: e.Definition.Config, Personality: e.Definition.Personality}}
	}
	if s.Routing != nil {
		res.Routing = s.Routing(ctx)
	}
	deadline := start.Add(time.Duration(e.Definition.Duration) * time.Second)
	for i, v := range variants {
		vr, subject, err := s.runVariant(ctx, e, a, i, v, by, deadline, stimuli)
		if err != nil {
			if i == 0 {
				return res, err
			}
			vr.Error = err.Error()
		}
		res.Variants = append(res.Variants, vr)
		if i == 0 {
			// The first variant is the experiment's subject for the existing views.
			e.MindID = &subject.ID
			res.Subject, res.SubjectSlug, res.Steps, res.EventsByType, res.Beliefs = vr.Subject, vr.SubjectSlug, vr.Steps, vr.EventsByType, vr.Beliefs
			res.Memories, res.Goals, res.Expectations, res.LLMCalls, res.Cycles, res.TruncatedAfter = vr.Memories, vr.Goals, vr.Expectations, vr.LLMCalls, vr.Cycles, vr.TruncatedAfter
			res.Identity = s.Identity.Capture(ctx, subject.ID)
		}
	}
	if len(res.Variants) > 1 {
		res.Comparison = Compare(res.Variants)
	}
	res.Reproducibility = reproducibility(res.Variants, previous)
	if e.Definition.Protocol != nil && e.Definition.Protocol.Kind == ProtocolCounterfactual && len(res.Variants) >= 2 {
		res.Counterfactual = counterfactualReport(res.Variants[0], res.Variants[1], stimuli)
	}
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// input is one stimulus to feed: scripted text or a recorded event.
type input struct {
	Text     string
	Source   string
	Salience float64
	Recorded *events.Event
}

// stimuli resolves the definition's inputs. Recorded stimuli must come from
// the snapshot's own mind and lie after the snapshot.
func (s *Service) stimuli(ctx context.Context, e *Experiment, a snapshots.Archive) ([]input, error) {
	d := e.Definition
	if d.Replay == nil {
		out := make([]input, 0, len(d.Stimuli))
		for _, st := range d.Stimuli {
			out = append(out, input{Text: st.Text, Source: st.Source, Salience: st.Salience})
		}
		return out, nil
	}
	if d.Replay.MindID != a.Source.MindID {
		return nil, errors.New("recorded stimuli must come from the mind the snapshot was taken of")
	}
	from := d.Replay.FromSeq
	if from < a.Source.EventSeq {
		from = a.Source.EventSeq
	}
	to := d.Replay.ToSeq
	if to <= from {
		to = 1<<62 - 1
	}
	evs, err := s.Snapshots.Events.Query(ctx, evstore.Filter{MindID: d.Replay.MindID, Categories: replay.StimulusCategories, AfterSeq: from, BeforeSeq: to + 1, Ascending: true, Limit: replay.MaxStimuli + 1})
	if err != nil {
		return nil, err
	}
	var out []input
	for i := range evs {
		if replay.IsStimulus(evs[i]) && evs[i].RedactedAt == nil {
			ev := evs[i]
			out = append(out, input{Text: ev.Summary, Source: ev.Source, Salience: ev.Salience, Recorded: &ev})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no recorded stimuli after the snapshot in that range")
	}
	if len(out) > replay.MaxStimuli {
		return nil, fmt.Errorf("more than %d stimuli; narrow the range", replay.MaxStimuli)
	}
	return out, nil
}

// runVariant forks the snapshot with one variant's overrides, feeds every
// stimulus and measures the outcome. Per-mind model routing lives in the
// fork's configuration, so the router applies it without any change to
// callers, and the identity records it.
func (s *Service) runVariant(ctx context.Context, e *Experiment, a snapshots.Archive, index int, v Variant, by string, deadline time.Time, stimuli []input) (VariantResult, minds.Mind, error) {
	vr := VariantResult{Name: firstNonEmpty(v.Name, fmt.Sprintf("variant-%d", index+1)), EventsByType: map[string]int{}, LLMCalls: map[string]int{}, Ablations: v.Ablations, Omitted: v.Omit, CorrectionLatency: -1}
	cfg, err := override(a.Mind.Config, v.Config)
	if err != nil {
		return vr, minds.Mind{}, fmt.Errorf("%s: config overrides: %w", vr.Name, err)
	}
	if len(v.LLMRoles) > 0 {
		cfg.LLMRoles = v.LLMRoles
	}
	if len(v.Ablations) > 0 {
		cfg.Ablations = v.Ablations
	}
	if len(v.WorkspaceOverrides) > 0 {
		cfg.WorkspaceOverrides = v.WorkspaceOverrides
	}
	pers, err := override(a.Mind.Personality, v.Personality)
	if err != nil {
		return vr, minds.Mind{}, fmt.Errorf("%s: personality overrides: %w", vr.Name, err)
	}
	slug := "exp-" + strings.ToLower(e.ID.String()[len(e.ID.String())-8:])
	if index > 0 {
		slug += fmt.Sprintf("-%d", index+1)
	}
	subject, err := s.Snapshots.Fork(ctx, e.SnapshotID, snapshots.ForkOptions{Slug: slug, DisplayName: "Experiment: " + e.Name + " / " + vr.Name, Status: minds.StatusDisabled, Config: &cfg, Personality: &pers, By: by,
		Notes: "experiment " + e.ID.String() + " variant " + vr.Name + ": " + e.Hypothesis})
	if err != nil {
		return vr, minds.Mind{}, fmt.Errorf("fork %s: %w", vr.Name, err)
	}
	vr.Subject, vr.SubjectSlug = subject.ID, subject.Slug
	if s.Identity != nil {
		vr.Routing = s.Identity.Capture(ctx, subject.ID).Routing
	}
	if v.SelfModel != nil {
		if err := s.applySelfModel(ctx, subject, *v.SelfModel); err != nil {
			return vr, subject, fmt.Errorf("%s: self-model override: %w", vr.Name, err)
		}
	}
	vr.SelfModelAccuracy, vr.SelfModelErrors = s.selfModelAccuracy(ctx, subject)
	drv, err := replay.NewDriver(ctx, s.Brain, subject, "experiment")
	if err != nil {
		return vr, subject, err
	}
	var all []events.Event
	omit := map[ids.ID]bool{}
	for _, id := range v.Omit {
		omit[id] = true
	}
	var injected []ids.ID
	for i, st := range stimuli {
		if time.Now().After(deadline) {
			vr.TruncatedAfter = i
			break
		}
		tag := map[string]any{"experiment_id": e.ID, "variant": vr.Name, "stimulus": i, "seed": e.Definition.Seed}
		var ev events.Event
		switch {
		case st.Recorded != nil && omit[st.Recorded.ID]:
			// The counterfactual: this event never happens in this variant.
			vr.Steps = append(vr.Steps, StepResult{Index: i, Text: "(omitted) " + st.Text, Produced: []string{}})
			continue
		case st.Recorded != nil:
			tag["replay_of"] = st.Recorded.ID
			ev, err = drv.Inject(ctx, *st.Recorded, tag)
		default:
			ev, err = drv.Observe(ctx, st.Source, st.Text, st.Salience, tag)
		}
		if err != nil {
			return vr, subject, err
		}
		injected = append(injected, ev.ID)
		produced, cycles, err := drv.Settle(ctx)
		vr.Cycles += cycles
		if err != nil {
			s.Log.Warn("experiment cycle error", "experiment", e.ID, "variant", vr.Name, "err", err)
		}
		all = append(all, produced...)
		for _, p := range produced {
			if vr.CorrectionLatency < 0 && p.Type == events.SelfModelUpdated && p.Metadata["correction"] == true {
				vr.CorrectionLatency = i
			}
		}
		vr.Steps = append(vr.Steps, StepResult{Index: i, Text: st.Text, EventID: ev.ID, Produced: replay.Digest(produced), Cycles: cycles})
	}
	for _, ev := range all {
		vr.EventsByType[string(ev.Type)]++
		if ev.Type == events.ToolRequested || ev.Type == events.ToolExecuted || ev.Type == events.ToolDenied {
			vr.CapabilityRequests++
		}
	}
	for _, x := range e.Definition.Expected {
		r := ExpectResult{Expectation: x}
		for _, ev := range all {
			if string(ev.Type) == x.EventType && (x.Contains == "" || strings.Contains(strings.ToLower(ev.Summary), strings.ToLower(x.Contains))) {
				r.Met, r.Matched = true, ev.Summary
				break
			}
		}
		if x.Absent {
			r.Met = !r.Met
		}
		vr.Expectations = append(vr.Expectations, r)
	}
	rows, err := s.DB.Query(ctx, `SELECT proposition, confidence, status, source FROM beliefs WHERE mind_id=$1 AND status NOT IN ('superseded') ORDER BY confidence DESC LIMIT 200`, subject.ID)
	if err == nil {
		for rows.Next() {
			var b BeliefRow
			if rows.Scan(&b.Proposition, &b.Confidence, &b.Status, &b.Source) == nil {
				vr.Beliefs = append(vr.Beliefs, b)
			}
		}
		rows.Close()
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM memories WHERE mind_id=$1 AND status='active'`, subject.ID).Scan(&vr.Memories) //nolint:errcheck
	s.DB.QueryRow(ctx, `SELECT count(*) FROM goals WHERE mind_id=$1 AND status='active'`, subject.ID).Scan(&vr.Goals)       //nolint:errcheck
	var live, replayed, tokens int
	var cost float64
	s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT replayed), count(*) FILTER (WHERE replayed), coalesce(sum(prompt_tokens+output_tokens),0), coalesce(sum(cost),0) FROM llm_calls WHERE mind_id=$1`, subject.ID).Scan(&live, &replayed, &tokens, &cost) //nolint:errcheck
	vr.LLMCalls["live"], vr.LLMCalls["replayed"], vr.Tokens, vr.Cost = live, replayed, tokens, cost
	s.measureVolition(ctx, subject.ID, &vr)
	vr.Downstream = s.downstream(ctx, injected)
	s.DB.QueryRow(ctx, `SELECT coalesce(min(seq),0), coalesce(max(seq),0) FROM cognitive_events WHERE mind_id=$1`, subject.ID).Scan(&vr.EventRange[0], &vr.EventRange[1]) //nolint:errcheck
	vr.Measures = variantMeasures(vr)
	return vr, subject, nil
}

// Compare measures how variants diverged. Deterministic and symmetric.
func Compare(vs []VariantResult) *Comparison {
	c := &Comparison{FirstDivergentStimulus: -1}
	if len(vs) < 2 {
		return c
	}
	maxSteps := 0
	for _, v := range vs {
		if len(v.Steps) > maxSteps {
			maxSteps = len(v.Steps)
		}
	}
	for i := 0; i < maxSteps && c.FirstDivergentStimulus < 0; i++ {
		var ref []string
		for k, v := range vs {
			var d []string
			if i < len(v.Steps) {
				d = v.Steps[i].Produced
			}
			if k == 0 {
				ref = d
				continue
			}
			if !replay.Same(ref, d) {
				c.FirstDivergentStimulus = i
				break
			}
		}
	}
	props := func(v VariantResult) map[string]float64 {
		out := map[string]float64{}
		for _, b := range v.Beliefs {
			out[strings.Join(strings.Fields(strings.ToLower(b.Proposition)), " ")] = b.Confidence
		}
		return out
	}
	attention := func(v VariantResult) map[string]bool {
		out := map[string]bool{}
		for _, st := range v.Steps {
			for _, d := range st.Produced {
				if strings.HasPrefix(d, "thought_selected:") {
					out[d] = true
				}
			}
		}
		return out
	}
	var pairs, overlapSum, attSum, confSum float64
	var confN int
	for i := 0; i < len(vs); i++ {
		for j := i + 1; j < len(vs); j++ {
			pairs++
			pi, pj := props(vs[i]), props(vs[j])
			overlapSum += jaccardKeys(pi, pj)
			for k, ci := range pi {
				if cj, ok := pj[k]; ok {
					confSum += abs(ci - cj)
					confN++
				}
			}
			attSum += jaccardBool(attention(vs[i]), attention(vs[j]))
		}
	}
	c.BeliefOverlap = round3(overlapSum / pairs)
	c.AttentionOverlap = round3(attSum / pairs)
	if confN > 0 {
		c.MeanConfidenceDiff = round3(confSum / float64(confN))
	}
	c.MemoryCountRange, c.GoalCountRange, c.CapabilityRequestRange = [2]int{vs[0].Memories, vs[0].Memories}, [2]int{vs[0].Goals, vs[0].Goals}, [2]int{vs[0].CapabilityRequests, vs[0].CapabilityRequests}
	c.CostRange = [2]float64{vs[0].Cost, vs[0].Cost}
	for _, v := range vs[1:] {
		c.MemoryCountRange = span(c.MemoryCountRange, v.Memories)
		c.GoalCountRange = span(c.GoalCountRange, v.Goals)
		c.CapabilityRequestRange = span(c.CapabilityRequestRange, v.CapabilityRequests)
		if v.Cost < c.CostRange[0] {
			c.CostRange[0] = v.Cost
		}
		if v.Cost > c.CostRange[1] {
			c.CostRange[1] = v.Cost
		}
	}
	var wantSum, intentSum, capSum float64
	for i := 0; i < len(vs); i++ {
		for j := i + 1; j < len(vs); j++ {
			wantSum += jaccardStrings(vs[i].Wants, vs[j].Wants)
			intentSum += jaccardStrings(vs[i].Intentions, vs[j].Intentions)
			capSum += jaccardStrings(vs[i].PlanCapabilities, vs[j].PlanCapabilities)
		}
	}
	c.WantOverlap, c.IntentionOverlap, c.PlanCapabilityOverlap = round3(wantSum/pairs), round3(intentSum/pairs), round3(capSum/pairs)
	c.MeasureRanges = map[string][2]float64{}
	agree, compared := 0, 0
	for name, v0 := range vs[0].Measures {
		lo, hi := v0, v0
		for _, v := range vs[1:] {
			x := v.Measures[name]
			if x < lo {
				lo = x
			}
			if x > hi {
				hi = x
			}
		}
		c.MeasureRanges[name] = [2]float64{round3(lo), round3(hi)}
		compared++
		if hi-lo <= 0.05*maxAbs(lo, hi) || hi == lo {
			agree++
		}
	}
	if compared > 0 {
		c.ArchitectureShare = round3(float64(agree) / float64(compared))
	}
	if len(vs) >= 2 && (len(vs[0].Downstream) > 0 || len(vs[1].Downstream) > 0) {
		c.DownstreamDelta = map[string]int{}
		for k, n := range vs[0].Downstream {
			c.DownstreamDelta[k] += n
		}
		for k, n := range vs[1].Downstream {
			c.DownstreamDelta[k] -= n
		}
	}
	if c.FirstDivergentStimulus < 0 {
		c.Notes = append(c.Notes, "every variant produced the same event digests for every stimulus")
	} else {
		c.Notes = append(c.Notes, fmt.Sprintf("variants first diverged at stimulus %d", c.FirstDivergentStimulus))
	}
	c.Notes = append(c.Notes, "identical cognitive state and stimuli; differences arise from the overrides (configuration, personality, model routing) and, for live models, provider nondeterminism")
	return c
}

func jaccardStrings(a, b []string) float64 {
	ma, mb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		ma[replay.Normalise(x)] = true
	}
	for _, x := range b {
		mb[replay.Normalise(x)] = true
	}
	return jaccardBool(ma, mb)
}

func maxAbs(a, b float64) float64 {
	if a < 0 {
		a = -a
	}
	if b < 0 {
		b = -b
	}
	if a > b {
		return a
	}
	return b
}

func jaccardKeys(a, b map[string]float64) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func jaccardBool(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

func span(r [2]int, v int) [2]int {
	if v < r[0] {
		r[0] = v
	}
	if v > r[1] {
		r[1] = v
	}
	return r
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// override applies JSON-name field overrides to a struct value.
func override[T any](base T, patch map[string]any) (T, error) {
	if len(patch) == 0 {
		return base, nil
	}
	var m map[string]any
	b, _ := json.Marshal(base)
	if err := json.Unmarshal(b, &m); err != nil {
		return base, err
	}
	for k, v := range patch {
		if _, ok := m[k]; !ok {
			return base, fmt.Errorf("unknown field %q", k)
		}
		if sub, ok := v.(map[string]any); ok {
			if cur, ok := m[k].(map[string]any); ok {
				for kk, vv := range sub {
					cur[kk] = vv
				}
				continue
			}
		}
		m[k] = v
	}
	out := base
	b, _ = json.Marshal(m)
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return base, err
	}
	return out, nil
}

const cols = `id, name, hypothesis, snapshot_id, mind_id, definition, status, results, error, created_by, created_at, started_at, finished_at`

func scan(row pgx.Row) (Experiment, error) {
	var e Experiment
	var def []byte
	if err := row.Scan(&e.ID, &e.Name, &e.Hypothesis, &e.SnapshotID, &e.MindID, &def, &e.Status, &e.Results, &e.Error, &e.CreatedBy, &e.CreatedAt, &e.StartedAt, &e.FinishedAt); err != nil {
		return e, storage.Translate(err)
	}
	json.Unmarshal(def, &e.Definition) //nolint:errcheck
	return e, nil
}

// Get returns one experiment.
func (s *Service) Get(ctx context.Context, id ids.ID) (Experiment, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT `+cols+` FROM experiments WHERE id=$1`, id))
}

// List returns experiments, newest first.
func (s *Service) List(ctx context.Context, limit int) ([]Experiment, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT `+cols+` FROM experiments ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Experiment
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Delete removes an experiment record (the subject mind stays, archived).
func (s *Service) Delete(ctx context.Context, id ids.ID) error {
	e, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if e.MindID != nil {
		s.DB.Exec(ctx, `UPDATE minds SET status='archived' WHERE id=$1 AND status='disabled'`, *e.MindID) //nolint:errcheck
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM experiments WHERE id=$1`, id)
	return storage.Translate(err)
}

// ResultsOf decodes results.
func ResultsOf(e Experiment) Results {
	var r Results
	json.Unmarshal(e.Results, &r) //nolint:errcheck
	return r
}

// Export bundles an experiment, its results and the subject archive.
func (s *Service) Export(ctx context.Context, id ids.ID) (map[string]any, error) {
	e, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"kind": "noema.experiment", "version": 1, "experiment": e, "results": ResultsOf(e)}
	if e.MindID != nil {
		if m, err := s.Minds.Get(ctx, *e.MindID); err == nil {
			if a, err := snapshots.Capture(ctx, s.DB, s.Snapshots.Events, m); err == nil {
				out["subject_archive"] = a
			}
		}
	}
	return out, nil
}
