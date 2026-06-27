// Package motivation holds a mind's drives and affect-like state: named
// scalars updated by deterministic rules from events, decaying towards
// personality-derived baselines. They bias attention and style and are fully
// inspectable and resettable. Nothing here claims the software feels
// anything; see docs/interpretation.md.
package motivation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// DriveNames is the closed set of drives. There is deliberately no
// self-preservation, replication or acquisition drive and no way to add one
// at runtime (spec §13, threat T25).
var DriveNames = []string{"curiosity", "uncertainty_reduction", "task_completion", "consistency", "novelty", "social_coherence", "exploration", "rest"}

// Drive is one motivational scalar with operator-configured range.
type Drive struct {
	Name      string    `json:"name"`
	Value     float64   `json:"value"`
	Min       float64   `json:"min"`
	Max       float64   `json:"max"`
	Baseline  float64   `json:"baseline"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Affect is the emotion-like state vector (spec §14). Valence is in [-1,1];
// everything else in [0,1].
type Affect struct {
	Valence      float64   `json:"valence"`
	Arousal      float64   `json:"arousal"`
	Curiosity    float64   `json:"curiosity"`
	Concern      float64   `json:"concern"`
	Frustration  float64   `json:"frustration"`
	Confidence   float64   `json:"confidence"`
	Uncertainty  float64   `json:"uncertainty"`
	Engagement   float64   `json:"engagement"`
	TaskPressure float64   `json:"task_pressure"`
	Load         float64   `json:"load"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Baselines derives the resting affect from personality traits.
func Baselines(p minds.Personality) Affect {
	t := p.Traits
	return Affect{Valence: 0.1, Arousal: 0.2, Curiosity: t["curiosity"], Concern: 0.1 + 0.2*(1-t["risk_tolerance"]), Frustration: 0.05,
		Confidence: t["confidence"], Uncertainty: 0.3 + 0.3*t["scepticism"], Engagement: 0.3 + 0.3*t["sociability"], TaskPressure: 0.1, Load: 0.1}
}

// DefaultDrives derives drive baselines from personality.
func DefaultDrives(p minds.Personality) []Drive {
	t, v := p.Traits, p.Values
	base := map[string]float64{"curiosity": t["curiosity"], "uncertainty_reduction": 0.3 + 0.5*v["accuracy"], "task_completion": 0.3 + 0.5*v["persistence"], "consistency": 0.3 + 0.5*v["evidence"],
		"novelty": t["novelty_preference"], "social_coherence": 0.2 + 0.6*t["sociability"], "exploration": 0.2 + 0.6*t["curiosity"]*t["risk_tolerance"], "rest": 0.3}
	out := make([]Drive, 0, len(DriveNames))
	for _, n := range DriveNames {
		out = append(out, Drive{Name: n, Value: clamp01(base[n]), Min: 0, Max: 1, Baseline: clamp01(base[n]), UpdatedAt: time.Now()})
	}
	return out
}

// State bundles drives and affect for a mind.
type State struct {
	Drives []Drive `json:"drives"`
	Affect Affect  `json:"affect"`
}

// Store persists motivation state.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Load returns the state, initialising it from personality on first use.
func (s *Store) Load(ctx context.Context, q storage.Querier, m minds.Mind) (State, error) {
	var st State
	rows, err := q.Query(ctx, `SELECT name, value, min, max, baseline, updated_at FROM drives WHERE mind_id=$1`, m.ID)
	if err != nil {
		return st, err
	}
	byName := map[string]Drive{}
	for rows.Next() {
		var d Drive
		if err := rows.Scan(&d.Name, &d.Value, &d.Min, &d.Max, &d.Baseline, &d.UpdatedAt); err != nil {
			rows.Close()
			return st, err
		}
		byName[d.Name] = d
	}
	rows.Close()
	defaults := DefaultDrives(m.Personality)
	for _, d := range defaults {
		if have, ok := byName[d.Name]; ok {
			st.Drives = append(st.Drives, have)
			continue
		}
		if _, err := q.Exec(ctx, `INSERT INTO drives (mind_id, name, value, min, max, baseline) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.ID, d.Name, d.Value, d.Min, d.Max, d.Baseline); err != nil {
			return st, err
		}
		st.Drives = append(st.Drives, d)
	}
	var raw []byte
	err = q.QueryRow(ctx, `SELECT state FROM affect_states WHERE mind_id=$1`, m.ID).Scan(&raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		st.Affect = Baselines(m.Personality)
		st.Affect.UpdatedAt = time.Now()
		if err := s.SaveAffect(ctx, q, m.ID, st.Affect); err != nil {
			return st, err
		}
	case err != nil:
		return st, err
	default:
		json.Unmarshal(raw, &st.Affect) //nolint:errcheck
	}
	return st, nil
}

// SaveAffect stores the affect vector.
func (s *Store) SaveAffect(ctx context.Context, q storage.Querier, mind ids.ID, a Affect) error {
	raw, _ := json.Marshal(a)
	_, err := q.Exec(ctx, `INSERT INTO affect_states (mind_id, state) VALUES ($1,$2) ON CONFLICT (mind_id) DO UPDATE SET state=excluded.state, updated_at=now()`, mind, raw)
	return err
}

// SaveDrive stores one drive value (and optionally its range/baseline).
func (s *Store) SaveDrive(ctx context.Context, q storage.Querier, mind ids.ID, d Drive) error {
	_, err := q.Exec(ctx, `INSERT INTO drives (mind_id, name, value, min, max, baseline, updated_at) VALUES ($1,$2,$3,$4,$5,$6,now())
		ON CONFLICT (mind_id, name) DO UPDATE SET value=excluded.value, min=excluded.min, max=excluded.max, baseline=excluded.baseline, updated_at=now()`, mind, d.Name, clampRange(d.Value, d.Min, d.Max), d.Min, d.Max, d.Baseline)
	return err
}

// Reset returns affect to baseline and drives to their baselines.
func (s *Store) Reset(ctx context.Context, q storage.Querier, m minds.Mind) (State, error) {
	if _, err := q.Exec(ctx, `UPDATE drives SET value=baseline, updated_at=now() WHERE mind_id=$1`, m.ID); err != nil {
		return State{}, err
	}
	a := Baselines(m.Personality)
	a.UpdatedAt = time.Now()
	if err := s.SaveAffect(ctx, q, m.ID, a); err != nil {
		return State{}, err
	}
	return s.Load(ctx, q, m)
}

// Delta is a change to apply to the state, produced by rules.
type Delta struct {
	Affect map[string]float64 // field → additive change
	Drives map[string]float64 // drive → additive change
	Reason string
}

// Rules maps an event to state changes. The table is the whole model: small,
// explicit, testable.
func Rules(ev events.Event) (Delta, bool) {
	d := Delta{Affect: map[string]float64{}, Drives: map[string]float64{}}
	switch ev.Type {
	case events.ContradictionDetected:
		d.Affect["concern"], d.Affect["uncertainty"], d.Affect["arousal"] = 0.15, 0.15, 0.1
		d.Drives["uncertainty_reduction"], d.Drives["consistency"] = 0.1, 0.1
		d.Reason = "contradiction"
	case events.PredictionError:
		d.Affect["arousal"], d.Affect["confidence"], d.Affect["uncertainty"], d.Affect["curiosity"] = 0.2, -0.15, 0.15, 0.1
		d.Drives["uncertainty_reduction"], d.Drives["curiosity"] = 0.15, 0.1
		d.Reason = "prediction error"
	case events.PredictionResolved:
		if ev.Metadata["outcome"] == "confirmed" {
			d.Affect["confidence"], d.Affect["valence"] = 0.1, 0.1
			d.Reason = "prediction confirmed"
		} else {
			return d, false
		}
	case events.GoalCompleted:
		d.Affect["valence"], d.Affect["task_pressure"], d.Affect["frustration"] = 0.2, -0.2, -0.1
		d.Drives["task_completion"], d.Drives["rest"] = -0.1, 0.1
		d.Reason = "goal completed"
	case events.GoalCreated:
		d.Affect["task_pressure"], d.Affect["engagement"] = 0.1, 0.1
		d.Drives["task_completion"] = 0.05
		d.Reason = "new goal"
	case events.GoalAbandoned:
		d.Affect["frustration"], d.Affect["valence"] = 0.1, -0.1
		d.Reason = "goal abandoned"
	case events.BeliefRetracted:
		d.Affect["uncertainty"], d.Affect["confidence"], d.Affect["frustration"] = 0.1, -0.1, 0.05
		d.Drives["consistency"] = 0.1
		d.Reason = "belief corrected by operator"
	case events.Observation:
		d.Affect["engagement"], d.Affect["arousal"] = 0.05, 0.05*ev.Salience
		if ev.Valence != nil {
			d.Affect["valence"] = 0.2 * *ev.Valence
		}
		d.Drives["novelty"] = 0.02
		d.Reason = "observation"
	case events.HypothesisCreated:
		d.Affect["curiosity"] = 0.1
		d.Drives["curiosity"], d.Drives["exploration"] = 0.05, 0.05
		d.Reason = "hypothesis"
	case events.Metacognition:
		d.Affect["uncertainty"], d.Affect["load"] = 0.05, 0.05
		d.Reason = "metacognitive warning"
	case events.CycleCompleted:
		if n, ok := ev.Metadata["candidates"].(int); ok && n > 8 {
			d.Affect["load"] = 0.1
			d.Reason = "busy cycle"
		} else {
			return d, false
		}
	default:
		return d, false
	}
	return d, true
}

// Apply adds a delta to the state and clamps.
func Apply(st *State, d Delta) {
	a := &st.Affect
	for k, v := range d.Affect {
		switch k {
		case "valence":
			a.Valence = clamp(a.Valence+v, -1, 1)
		case "arousal":
			a.Arousal = clamp01(a.Arousal + v)
		case "curiosity":
			a.Curiosity = clamp01(a.Curiosity + v)
		case "concern":
			a.Concern = clamp01(a.Concern + v)
		case "frustration":
			a.Frustration = clamp01(a.Frustration + v)
		case "confidence":
			a.Confidence = clamp01(a.Confidence + v)
		case "uncertainty":
			a.Uncertainty = clamp01(a.Uncertainty + v)
		case "engagement":
			a.Engagement = clamp01(a.Engagement + v)
		case "task_pressure":
			a.TaskPressure = clamp01(a.TaskPressure + v)
		case "load":
			a.Load = clamp01(a.Load + v)
		}
	}
	for i := range st.Drives {
		if v, ok := d.Drives[st.Drives[i].Name]; ok {
			st.Drives[i].Value = clampRange(st.Drives[i].Value+v, st.Drives[i].Min, st.Drives[i].Max)
		}
	}
}

// Decay moves state towards baselines by a fraction that depends on elapsed
// time (half-life one hour for affect, six hours for drives).
func Decay(st *State, base Affect, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	fa := 1 - math.Pow(0.5, elapsed.Hours())
	fd := 1 - math.Pow(0.5, elapsed.Hours()/6)
	a := &st.Affect
	a.Valence += (base.Valence - a.Valence) * fa
	a.Arousal += (base.Arousal - a.Arousal) * fa
	a.Curiosity += (base.Curiosity - a.Curiosity) * fa
	a.Concern += (base.Concern - a.Concern) * fa
	a.Frustration += (base.Frustration - a.Frustration) * fa
	a.Confidence += (base.Confidence - a.Confidence) * fa
	a.Uncertainty += (base.Uncertainty - a.Uncertainty) * fa
	a.Engagement += (base.Engagement - a.Engagement) * fa
	a.TaskPressure += (base.TaskPressure - a.TaskPressure) * fa
	a.Load += (base.Load - a.Load) * fa
	for i := range st.Drives {
		d := &st.Drives[i]
		d.Value += (d.Baseline - d.Value) * fd
	}
}

// Modulate scales attention weights by drives and affect. The mapping is
// explicit so the workspace page can show effective weights next to the
// configured ones.
func Modulate(weights map[string]float64, st State) map[string]float64 {
	out := make(map[string]float64, len(weights))
	for k, v := range weights {
		out[k] = v
	}
	drive := map[string]float64{}
	for _, d := range st.Drives {
		drive[d.Name] = d.Value
	}
	scale := func(key string, factor float64) {
		if v, ok := out[key]; ok {
			out[key] = round3(v * factor)
		}
	}
	scale("novelty", 0.6+0.8*drive["curiosity"]) // 0.6× .. 1.4×
	scale("goal_relevance", 0.6+0.8*drive["task_completion"])
	scale("contradiction", 0.6+0.8*drive["uncertainty_reduction"])
	scale("affect", 0.6+0.8*st.Affect.Arousal)
	scale("urgency", 0.8+0.4*st.Affect.Concern)
	scale("context_relevance", 0.7+0.6*(1-drive["exploration"]))
	return out
}

// Significant reports whether two affect vectors differ enough to emit an event.
func Significant(a, b Affect) (string, bool) {
	fields := map[string][2]float64{"valence": {a.Valence, b.Valence}, "arousal": {a.Arousal, b.Arousal}, "curiosity": {a.Curiosity, b.Curiosity}, "concern": {a.Concern, b.Concern},
		"frustration": {a.Frustration, b.Frustration}, "confidence": {a.Confidence, b.Confidence}, "uncertainty": {a.Uncertainty, b.Uncertainty}, "engagement": {a.Engagement, b.Engagement},
		"task_pressure": {a.TaskPressure, b.TaskPressure}, "load": {a.Load, b.Load}}
	best, bestKey := 0.0, ""
	for k, v := range fields {
		if d := math.Abs(v[0] - v[1]); d > best {
			best, bestKey = d, k
		}
	}
	return bestKey, best >= 0.1
}

func clamp01(v float64) float64 { return clamp(v, 0, 1) }

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) || v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampRange(v, lo, hi float64) float64 {
	if hi < lo {
		hi = lo
	}
	return clamp(v, lo, hi)
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
