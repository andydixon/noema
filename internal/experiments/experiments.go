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
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/replay"
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
}

// Variant is one fork's overrides in a comparative experiment.
type Variant struct {
	Name        string            `json:"name"`
	Config      map[string]any    `json:"config,omitempty"`
	Personality map[string]any    `json:"personality,omitempty"`
	LLMRoles    map[string]string `json:"llm_roles,omitempty"` // role → model id, resolved by the router for this fork only
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
}

// ErrBusy is returned when a run is already in progress.
var ErrBusy = errors.New("experiment already running")

// Validate bounds a definition.
func (d *Definition) Validate() error {
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
	res := Results{EventsByType: map[string]int{}, LLMCalls: map[string]int{}}
	a, err := s.Snapshots.Archive(ctx, e.SnapshotID)
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
		vr, subject, err := s.runVariant(ctx, e, a, i, v, by, deadline)
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
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
}

// runVariant forks the snapshot with one variant's overrides, feeds every
// stimulus and measures the outcome. Per-mind model routing lives in the
// fork's configuration, so the router applies it without any change to
// callers, and the identity records it.
func (s *Service) runVariant(ctx context.Context, e *Experiment, a snapshots.Archive, index int, v Variant, by string, deadline time.Time) (VariantResult, minds.Mind, error) {
	vr := VariantResult{Name: firstNonEmpty(v.Name, fmt.Sprintf("variant-%d", index+1)), EventsByType: map[string]int{}, LLMCalls: map[string]int{}}
	cfg, err := override(a.Mind.Config, v.Config)
	if err != nil {
		return vr, minds.Mind{}, fmt.Errorf("%s: config overrides: %w", vr.Name, err)
	}
	if len(v.LLMRoles) > 0 {
		cfg.LLMRoles = v.LLMRoles
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
	vr.Routing = s.Identity.Capture(ctx, subject.ID).Routing
	drv, err := replay.NewDriver(ctx, s.Brain, subject, "experiment")
	if err != nil {
		return vr, subject, err
	}
	var all []events.Event
	for i, st := range e.Definition.Stimuli {
		if time.Now().After(deadline) {
			vr.TruncatedAfter = i
			break
		}
		ev, err := drv.Observe(ctx, st.Source, st.Text, st.Salience, map[string]any{"experiment_id": e.ID, "variant": vr.Name, "stimulus": i, "seed": e.Definition.Seed})
		if err != nil {
			return vr, subject, err
		}
		produced, cycles, err := drv.Settle(ctx)
		vr.Cycles += cycles
		if err != nil {
			s.Log.Warn("experiment cycle error", "experiment", e.ID, "variant", vr.Name, "err", err)
		}
		all = append(all, produced...)
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
	if c.FirstDivergentStimulus < 0 {
		c.Notes = append(c.Notes, "every variant produced the same event digests for every stimulus")
	} else {
		c.Notes = append(c.Notes, fmt.Sprintf("variants first diverged at stimulus %d", c.FirstDivergentStimulus))
	}
	c.Notes = append(c.Notes, "identical cognitive state and stimuli; differences arise from the overrides (configuration, personality, model routing) and, for live models, provider nondeterminism")
	return c
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
