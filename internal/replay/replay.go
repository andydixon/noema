package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/brain"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/identity"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Replay is one recorded re-run.
type Replay struct {
	ID           ids.ID          `json:"id"`
	MindID       ids.ID          `json:"mind_id"`
	SnapshotID   ids.ID          `json:"snapshot_id"`
	ReplayMindID *ids.ID         `json:"replay_mind_id,omitempty"`
	FromSeq      int64           `json:"from_seq"`
	ToSeq        int64           `json:"to_seq"`
	Status       string          `json:"status"`
	Summary      Summary         `json:"summary"`
	Steps        json.RawMessage `json:"steps"`
	Error        string          `json:"error"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	// Reproducibility: the runtime this replay ran under, how it compares
	// with the runtime that took the snapshot, and where the first
	// divergence appeared.
	Identity      identity.Identity `json:"identity"`
	Compatibility string            `json:"compatibility"` // exact-candidate | different-build | incompatible
	IdentityDiff  []string          `json:"identity_diff"`
	Divergence    *Divergence       `json:"divergence,omitempty"`
}

// Divergence describes the first step whose outcome differed and which
// components the differing events belong to. Components are inferred from
// event types and labelled as such; they are a pointer, not a proof.
type Divergence struct {
	Step         int      `json:"step"`
	StimulusID   ids.ID   `json:"stimulus_id"`
	Label        string   `json:"label"`
	Components   []string `json:"components"`
	OnlyRecorded []string `json:"only_recorded"`
	OnlyReplayed []string `json:"only_replayed"`
	RecordedIn   string   `json:"recorded_build"`
	ReplayedIn   string   `json:"replayed_build"`
}

// Summary counts step labels.
type Summary struct {
	Steps       int `json:"steps"`
	Exact       int `json:"exact"`
	Approximate int `json:"approximate"`
	Diverged    int `json:"diverged"`
	LLMLive     int `json:"llm_live"`
	LLMReplayed int `json:"llm_replayed"`
	Cycles      int `json:"cycles"`
	Redacted    int `json:"redacted"` // stimuli whose content was redacted and could not be replayed
}

// Step is the outcome of replaying one stimulus.
type Step struct {
	Index       int      `json:"index"`
	StimulusID  ids.ID   `json:"stimulus_id"`
	InjectedID  ids.ID   `json:"injected_id"`
	Summary     string   `json:"summary"`
	Original    []string `json:"original"`
	Replayed    []string `json:"replayed"`
	Label       string   `json:"label"` // exact | approximate | diverged
	Note        string   `json:"note"`
	Cycles      int      `json:"cycles"`
	LLMLive     int      `json:"llm_live"`
	LLMReplayed int      `json:"llm_replayed"`
}

// Service runs and stores replays.
type Service struct {
	DB        *pgxpool.Pool
	Events    *evstore.Store
	Minds     *minds.Store
	Snapshots *snapshots.Store
	Brain     brain.Config // template; Services.LLM must be a replaying router
	Identity  *identity.Capturer
	Log       *slog.Logger
}

// MaxStimuli bounds one replay.
const MaxStimuli = 200

// Run replays the source mind's perception events in (from, to] starting
// from a snapshot, inside a fresh disabled sandbox mind.
func (s *Service) Run(ctx context.Context, src minds.Mind, snapshotID ids.ID, from, to int64, by string) (Replay, error) {
	r, err := s.Begin(ctx, src, snapshotID, from, to, by)
	if err != nil {
		return r, err
	}
	return s.Execute(ctx, r)
}

// Begin validates and records a replay so callers can run Execute in the
// background and hand the id back immediately.
func (s *Service) Begin(ctx context.Context, src minds.Mind, snapshotID ids.ID, from, to int64, by string) (Replay, error) {
	r := Replay{ID: ids.New(), MindID: src.ID, SnapshotID: snapshotID, FromSeq: from, ToSeq: to, Status: "running", CreatedBy: by, CreatedAt: time.Now(), Steps: []byte("[]"), Summary: Summary{}}
	sn, err := s.Snapshots.Get(ctx, snapshotID)
	if err != nil || sn.MindID != src.ID {
		return r, fmt.Errorf("snapshot not found for this mind")
	}
	if from < sn.EventSeq {
		r.FromSeq = sn.EventSeq
	}
	if to <= r.FromSeq {
		if last, err := s.Events.Query(ctx, evstore.Filter{MindID: src.ID, Limit: 1}); err == nil && len(last) > 0 {
			r.ToSeq = last[0].Seq
		}
	}
	if r.ToSeq <= r.FromSeq {
		return r, fmt.Errorf("nothing to replay after event %d", r.FromSeq)
	}
	r.Identity = s.Identity.Capture(ctx, src.ID)
	r.Compatibility, r.IdentityDiff = identity.Compare(sn.Identity, r.Identity)
	if r.IdentityDiff == nil {
		r.IdentityDiff = []string{}
	}
	idj, _ := json.Marshal(r.Identity)
	_, err = s.DB.Exec(ctx, `INSERT INTO replays (id, mind_id, snapshot_id, from_seq, to_seq, status, created_by, identity, compatibility, identity_diff) VALUES ($1,$2,$3,$4,$5,'running',$6,$7,$8,$9)`, r.ID, src.ID, snapshotID, r.FromSeq, r.ToSeq, by, idj, r.Compatibility, r.IdentityDiff)
	return r, err
}

// Execute performs a replay recorded by Begin.
func (s *Service) Execute(ctx context.Context, r Replay) (Replay, error) {
	src, err := s.Minds.Get(ctx, r.MindID)
	if err != nil {
		return r, err
	}
	snapshotID, from, to, by := r.SnapshotID, r.FromSeq, r.ToSeq, r.CreatedBy
	defer func() {
		if p := recover(); p != nil {
			s.Log.Error("replay panicked", "replay", r.ID, "panic", p)
			s.DB.Exec(context.WithoutCancel(ctx), `UPDATE replays SET status='failed', error='internal error', finished_at=now() WHERE id=$1`, r.ID) //nolint:errcheck
		}
	}()
	finish := func(status, errText string, steps []Step) (Replay, error) {
		if steps == nil {
			steps = []Step{}
		}
		sj, _ := json.Marshal(steps)
		sum, _ := json.Marshal(r.Summary)
		div := []byte("{}")
		if r.Divergence != nil {
			div, _ = json.Marshal(r.Divergence)
		}
		_, err := s.DB.Exec(ctx, `UPDATE replays SET status=$2, error=$3, steps=$4, summary=$5, replay_mind_id=$6, divergence=$7, finished_at=now() WHERE id=$1`, r.ID, status, errText, sj, sum, r.ReplayMindID, div)
		r.Status, r.Error, r.Steps = status, errText, sj
		if err != nil {
			return r, err
		}
		if errText != "" {
			return r, fmt.Errorf("%s", errText)
		}
		return r, nil
	}
	sn, err := s.Snapshots.Get(ctx, snapshotID)
	if err != nil || sn.MindID != src.ID {
		return finish("failed", "snapshot not found for this mind", nil)
	}
	if r.Compatibility == identity.CompatIncompatible {
		// A different schema means the sandbox could not be a faithful
		// reconstruction; say so instead of producing a misleading comparison.
		return finish("incompatible", "schema differs from the one the snapshot was taken under: "+strings.Join(r.IdentityDiff, "; "), nil)
	}
	stimuli, err := s.Events.Query(ctx, evstore.Filter{MindID: src.ID, Categories: StimulusCategories, AfterSeq: from, BeforeSeq: to + 1, Ascending: true, Limit: MaxStimuli + 1})
	if err != nil {
		return finish("failed", err.Error(), nil)
	}
	if len(stimuli) > MaxStimuli {
		return finish("failed", fmt.Sprintf("more than %d stimuli in range; narrow it", MaxStimuli), nil)
	}
	var external []events.Event
	for _, e := range stimuli {
		if isStimulus(e) {
			external = append(external, e)
		}
	}
	if len(external) == 0 {
		return finish("failed", "no perception or operator events in range", nil)
	}
	slug := "replay-" + strings.ToLower(r.ID.String()[len(r.ID.String())-8:])
	sandbox, err := s.Snapshots.Fork(ctx, snapshotID, snapshots.ForkOptions{Slug: slug, DisplayName: "Replay of " + src.DisplayName, Status: minds.StatusDisabled, By: by,
		Notes: fmt.Sprintf("replay %s of %s from event %d to %d (snapshot %s)", r.ID, src.Slug, from, to, sn.Name)})
	if err != nil {
		return finish("failed", "fork sandbox: "+err.Error(), nil)
	}
	r.ReplayMindID = &sandbox.ID
	drv, err := NewDriver(ctx, s.Brain, sandbox, "replay")
	if err != nil {
		return finish("failed", "runtime: "+err.Error(), nil)
	}
	var steps []Step
	for i, stim := range external {
		st := Step{Index: i, StimulusID: stim.ID, Summary: stim.Summary}
		// What the source produced between this stimulus and the next one.
		upper := to + 1
		if i+1 < len(external) {
			upper = external[i+1].Seq
		}
		orig, _ := s.Events.Query(ctx, evstore.Filter{MindID: src.ID, AfterSeq: stim.Seq, BeforeSeq: upper, Ascending: true, Limit: 2000})
		st.Original = Digest(withoutPerception(orig))
		if stim.RedactedAt != nil {
			// The content is gone; replaying a marker would fabricate cognition.
			st.Label, st.Note = "redacted", "stimulus content was redacted under a privacy policy and was not replayed"
			r.Summary.Redacted++
			steps = append(steps, st)
			continue
		}
		startedAt := time.Now()
		inj, err := drv.Inject(ctx, stim, map[string]any{"replay_of": stim.ID, "replay_id": r.ID})
		if err != nil {
			st.Label, st.Note = "diverged", "inject failed: "+err.Error()
			steps = append(steps, st)
			continue
		}
		st.InjectedID = inj.ID
		produced, cycles, err := drv.Settle(ctx)
		st.Cycles = cycles
		r.Summary.Cycles += cycles
		if err != nil {
			st.Note = "cycle error: " + err.Error()
		}
		st.Replayed = Digest(produced)
		st.LLMLive, st.LLMReplayed = s.llmCounts(ctx, sandbox.ID, startedAt)
		r.Summary.LLMLive += st.LLMLive
		r.Summary.LLMReplayed += st.LLMReplayed
		switch {
		case st.LLMLive > 0:
			st.Label = "approximate"
			st.Note = strings.TrimSpace(st.Note + " live model call(s); recorded response not found")
			r.Summary.Approximate++
		case Same(st.Original, st.Replayed):
			st.Label = "exact"
			r.Summary.Exact++
		default:
			st.Label = "diverged"
			st.Note = strings.TrimSpace(st.Note + " deterministic run produced different events; state or code differs from the recording")
			r.Summary.Diverged++
		}
		steps = append(steps, st)
		if st.Label != "exact" && r.Divergence == nil {
			r.Divergence = analyseDivergence(st, sn.Identity, r.Identity)
		}
	}
	r.Summary.Steps = len(steps)
	return finish("done", "", steps)
}

// StimulusCategories are the event categories a mind receives from outside:
// perceptions (messages, integrations, observations) and operator actions.
var StimulusCategories = []events.Category{events.CatPerception, events.CatOperator}

// IsStimulus reports whether an event reached the mind from outside and so
// is replayed as input (experiments reuse the same selection).
func IsStimulus(e events.Event) bool { return isStimulus(e) }

func isStimulus(e events.Event) bool {
	if strings.HasPrefix(e.Source, "brain") {
		return false
	}
	for _, c := range StimulusCategories {
		if e.Category == c {
			return true
		}
	}
	return false
}

func withoutPerception(evs []events.Event) []events.Event {
	var out []events.Event
	for _, e := range evs {
		if !isStimulus(e) {
			out = append(out, e)
		}
	}
	return out
}

func (s *Service) llmCounts(ctx context.Context, mind ids.ID, since time.Time) (live, replayed int) {
	s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE NOT replayed), count(*) FILTER (WHERE replayed) FROM llm_calls WHERE mind_id=$1 AND created_at >= $2`, mind, since).Scan(&live, &replayed) //nolint:errcheck
	return
}

const replayCols = `id, mind_id, snapshot_id, replay_mind_id, from_seq, to_seq, status, summary, steps, error, created_by, created_at, finished_at, identity, compatibility, identity_diff, divergence`

func scan(row pgx.Row) (Replay, error) {
	var r Replay
	var sum, idj, div []byte
	if err := row.Scan(&r.ID, &r.MindID, &r.SnapshotID, &r.ReplayMindID, &r.FromSeq, &r.ToSeq, &r.Status, &sum, &r.Steps, &r.Error, &r.CreatedBy, &r.CreatedAt, &r.FinishedAt, &idj, &r.Compatibility, &r.IdentityDiff, &div); err != nil {
		return r, storage.Translate(err)
	}
	json.Unmarshal(sum, &r.Summary)  //nolint:errcheck
	json.Unmarshal(idj, &r.Identity) //nolint:errcheck
	if len(div) > 2 {
		var d Divergence
		if json.Unmarshal(div, &d) == nil {
			r.Divergence = &d
		}
	}
	if r.IdentityDiff == nil {
		r.IdentityDiff = []string{}
	}
	return r, nil
}

// Get returns one replay.
func (s *Service) Get(ctx context.Context, id ids.ID) (Replay, error) {
	return scan(s.DB.QueryRow(ctx, `SELECT `+replayCols+` FROM replays WHERE id=$1`, id))
}

// List returns replays for a mind, newest first.
func (s *Service) List(ctx context.Context, mind ids.ID, limit int) ([]Replay, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT `+replayCols+` FROM replays WHERE mind_id=$1 ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Replay
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// StepsOf decodes the steps of a replay.
func StepsOf(r Replay) []Step {
	var out []Step
	json.Unmarshal(r.Steps, &out) //nolint:errcheck
	return out
}

// componentOf maps an event type onto the deterministic subsystem that
// produces it, so a divergence can point at a component.
func componentOf(digest string) string {
	t, _, _ := strings.Cut(digest, ":")
	switch t {
	case "memory_retrieved":
		return "retrieval"
	case "thought_selected", "thought_candidate", "attention_shift":
		return "attention"
	case "memory_created", "memory_reinforced":
		return "encoding"
	case "belief_created", "belief_updated", "belief_challenged", "belief_retracted", "contradiction_detected":
		return "beliefs"
	case "hypothesis_created", "hypothesis_supported", "hypothesis_weakened":
		return "association"
	case "prediction_created", "prediction_resolved", "prediction_error":
		return "predictions"
	case "goal_created", "goal_updated", "goal_completed", "goal_abandoned":
		return "goals"
	case "response_generated":
		return "response"
	case "narrative_updated":
		return "narrative"
	case "drive_changed", "emotion_changed":
		return "motivation"
	case "metacognition":
		return "metacognition"
	case "tool_requested", "tool_denied", "policy_decision":
		return "capabilities"
	}
	return t
}

// analyseDivergence records the first non-exact step: what only the
// recording had, what only the replay had, and the components involved.
func analyseDivergence(st Step, recorded, current identity.Identity) *Divergence {
	d := &Divergence{Step: st.Index, StimulusID: st.StimulusID, Label: st.Label, RecordedIn: shortBuild(recorded), ReplayedIn: shortBuild(current)}
	have := map[string]bool{}
	for _, x := range st.Replayed {
		have[x] = true
	}
	comps := map[string]bool{}
	for _, x := range st.Original {
		if !have[x] {
			d.OnlyRecorded = append(d.OnlyRecorded, x)
			comps[componentOf(x)] = true
		}
	}
	seen := map[string]bool{}
	for _, x := range st.Original {
		seen[x] = true
	}
	for _, x := range st.Replayed {
		if !seen[x] {
			d.OnlyReplayed = append(d.OnlyReplayed, x)
			comps[componentOf(x)] = true
		}
	}
	for c := range comps {
		d.Components = append(d.Components, c)
	}
	sort.Strings(d.Components)
	if st.LLMLive > 0 {
		d.Components = append(d.Components, "model (live call; provider nondeterminism)")
	}
	if d.OnlyRecorded == nil {
		d.OnlyRecorded = []string{}
	}
	if d.OnlyReplayed == nil {
		d.OnlyReplayed = []string{}
	}
	return d
}

func shortBuild(id identity.Identity) string {
	if len(id.Build.Commit) >= 8 {
		if id.Build.Modified {
			return id.Build.Commit[:8] + "+dirty"
		}
		return id.Build.Commit[:8]
	}
	if id.Build.Version != "" {
		return id.Build.Version
	}
	return "unrecorded"
}
