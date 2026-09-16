package research

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Measurement is one recorded value of a measure.
type Measurement struct {
	ID           ids.ID         `json:"id"`
	MindID       ids.ID         `json:"mind_id"`
	Measure      string         `json:"measure"`
	Value        *float64       `json:"value,omitempty"`
	Label        string         `json:"label"`
	SubjectKind  string         `json:"subject_kind"`
	SubjectID    string         `json:"subject_id"`
	ExperimentID ids.ID         `json:"experiment_id,omitempty"`
	EventID      ids.ID         `json:"event_id,omitempty"`
	Detail       map[string]any `json:"detail"`
	CreatedAt    time.Time      `json:"created_at"`
}

// Store persists measurements.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

// Record appends a measurement using q (nil = pool).
func (s *Store) Record(ctx context.Context, q storage.Querier, m Measurement) (Measurement, error) {
	if q == nil {
		q = s.db
	}
	m.ID = ids.New()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	if m.Detail == nil {
		m.Detail = map[string]any{}
	}
	detail, _ := json.Marshal(m.Detail)
	_, err := q.Exec(ctx, `INSERT INTO research_measurements (id, mind_id, measure, value, label, subject_kind, subject_id, experiment_id, event_id, detail, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		m.ID, m.MindID, m.Measure, m.Value, m.Label, m.SubjectKind, m.SubjectID, nullID(m.ExperimentID), nullID(m.EventID), detail, m.CreatedAt)
	return m, err
}

// List returns measurements of a mind (measure empty = all), newest first.
func (s *Store) List(ctx context.Context, mind ids.ID, measure string, since time.Time, limit int) ([]Measurement, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	rows, err := s.db.Query(ctx, `SELECT id, mind_id, measure, value, label, subject_kind, subject_id, experiment_id, event_id, detail, created_at FROM research_measurements
		WHERE mind_id=$1 AND ($2='' OR measure=$2) AND created_at >= $3 ORDER BY created_at DESC LIMIT $4`, mind, measure, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Measurement
	for rows.Next() {
		var m Measurement
		var exp, ev *string
		var detail []byte
		if err := rows.Scan(&m.ID, &m.MindID, &m.Measure, &m.Value, &m.Label, &m.SubjectKind, &m.SubjectID, &exp, &ev, &detail, &m.CreatedAt); err != nil {
			return nil, err
		}
		if exp != nil {
			m.ExperimentID = ids.ID(*exp)
		}
		if ev != nil {
			m.EventID = ids.ID(*ev)
		}
		json.Unmarshal(detail, &m.Detail) //nolint:errcheck
		out = append(out, m)
	}
	return out, rows.Err()
}

// Float returns a pointer for a measurement value.
func Float(v float64) *float64 { return &v }
