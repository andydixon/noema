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
	Subject        ids.ID           `json:"subject_mind_id"`
	SubjectSlug    string           `json:"subject_slug"`
	Steps          []StepResult     `json:"steps"`
	EventsByType   map[string]int   `json:"events_by_type"`
	Beliefs        []BeliefRow      `json:"beliefs"`
	Memories       int              `json:"memories"`
	Goals          int              `json:"goals"`
	Expectations   []ExpectResult   `json:"expectations"`
	LLMCalls       map[string]int   `json:"llm_calls"` // live / replayed
	Routing        []map[string]any `json:"routing,omitempty"`
	DurationMS     int64            `json:"duration_ms"`
	Cycles         int              `json:"cycles"`
	TruncatedAfter int              `json:"truncated_after_stimulus,omitempty"`
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
	cfg, err := override(a.Mind.Config, e.Definition.Config)
	if err != nil {
		return res, fmt.Errorf("config overrides: %w", err)
	}
	pers, err := override(a.Mind.Personality, e.Definition.Personality)
	if err != nil {
		return res, fmt.Errorf("personality overrides: %w", err)
	}
	slug := "exp-" + strings.ToLower(e.ID.String()[len(e.ID.String())-8:])
	subject, err := s.Snapshots.Fork(ctx, e.SnapshotID, snapshots.ForkOptions{Slug: slug, DisplayName: "Experiment: " + e.Name, Status: minds.StatusDisabled, Config: &cfg, Personality: &pers, By: by,
		Notes: "experiment " + e.ID.String() + ": " + e.Hypothesis})
	if err != nil {
		return res, fmt.Errorf("fork subject: %w", err)
	}
	e.MindID = &subject.ID
	res.Subject, res.SubjectSlug = subject.ID, subject.Slug
	if s.Routing != nil {
		res.Routing = s.Routing(ctx)
	}
	drv, err := replay.NewDriver(ctx, s.Brain, subject, "experiment")
	if err != nil {
		return res, err
	}
	deadline := start.Add(time.Duration(e.Definition.Duration) * time.Second)
	var all []events.Event
	for i, st := range e.Definition.Stimuli {
		if time.Now().After(deadline) {
			res.TruncatedAfter = i
			break
		}
		ev, err := drv.Observe(ctx, st.Source, st.Text, st.Salience, map[string]any{"experiment_id": e.ID, "stimulus": i, "seed": e.Definition.Seed})
		if err != nil {
			return res, err
		}
		produced, cycles, err := drv.Settle(ctx)
		res.Cycles += cycles
		if err != nil {
			s.Log.Warn("experiment cycle error", "experiment", e.ID, "err", err)
		}
		all = append(all, produced...)
		res.Steps = append(res.Steps, StepResult{Index: i, Text: st.Text, EventID: ev.ID, Produced: replay.Digest(produced), Cycles: cycles})
	}
	for _, ev := range all {
		res.EventsByType[string(ev.Type)]++
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
		res.Expectations = append(res.Expectations, r)
	}
	rows, err := s.DB.Query(ctx, `SELECT proposition, confidence, status, source FROM beliefs WHERE mind_id=$1 AND status NOT IN ('superseded') ORDER BY confidence DESC LIMIT 200`, subject.ID)
	if err == nil {
		for rows.Next() {
			var b BeliefRow
			if rows.Scan(&b.Proposition, &b.Confidence, &b.Status, &b.Source) == nil {
				res.Beliefs = append(res.Beliefs, b)
			}
		}
		rows.Close()
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM memories WHERE mind_id=$1 AND status='active'`, subject.ID).Scan(&res.Memories) //nolint:errcheck
	s.DB.QueryRow(ctx, `SELECT count(*) FROM goals WHERE mind_id=$1 AND status='active'`, subject.ID).Scan(&res.Goals)       //nolint:errcheck
	var live, replayed int
	s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT replayed), count(*) FILTER (WHERE replayed) FROM llm_calls WHERE mind_id=$1`, subject.ID).Scan(&live, &replayed) //nolint:errcheck
	res.LLMCalls["live"], res.LLMCalls["replayed"] = live, replayed
	res.DurationMS = time.Since(start).Milliseconds()
	return res, nil
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
