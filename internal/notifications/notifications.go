// Package notifications decides when something warrants human attention and
// records why. Rules are explicit and thresholded; repeats within a cooldown
// are aggregated rather than re-sent (spec §39).
package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Notification is one item for a human.
type Notification struct {
	ID           ids.ID          `json:"id"`
	MindID       ids.ID          `json:"mind_id"`
	Level        string          `json:"level"` // info | warn | alert
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Rule         string          `json:"rule"`
	Reason       json.RawMessage `json:"reason"`
	AggregateKey string          `json:"aggregate_key"`
	Count        int             `json:"count"`
	Status       string          `json:"status"` // new | read | suppressed
	SourceEvent  ids.ID          `json:"source_event,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
	ReadAt       *time.Time      `json:"read_at,omitempty"`
}

// Candidate is a notification a rule proposes before suppression.
type Candidate struct {
	Level        string
	Title        string
	Body         string
	Rule         string
	Reason       map[string]any
	AggregateKey string
	SourceEvent  ids.ID
}

// Rules evaluates one event against the mind's thresholds. It is the whole
// policy of what deserves attention, kept small and legible.
func Rules(ev events.Event, cfg minds.CognitiveConfig, p minds.Personality) []Candidate {
	var out []Candidate
	proactive := p.Behaviour.NotifyProactively
	switch ev.Type {
	case events.PredictionError:
		conf, _ := ev.Metadata["confidence"].(float64)
		if conf >= 0.7 {
			out = append(out, Candidate{Level: "warn", Title: "A confident prediction failed", Body: ev.Summary, Rule: "prediction_error",
				Reason: map[string]any{"confidence": conf, "threshold": 0.7}, AggregateKey: "prediction_error:" + str(ev.Metadata["prediction_id"]), SourceEvent: ev.ID})
		}
	case events.ContradictionDetected:
		if ev.Confidence >= 0.6 {
			out = append(out, Candidate{Level: "warn", Title: "Two beliefs contradict each other", Body: ev.Summary, Rule: "contradiction",
				Reason: map[string]any{"similarity": ev.Confidence, "threshold": 0.6}, AggregateKey: "contradiction:" + str(ev.Metadata["belief_id"]), SourceEvent: ev.ID})
		}
	case events.BeliefCreated, events.BeliefUpdated, events.HypothesisSupported:
		if proactive && ev.Confidence >= cfg.NotifyMinConfidence && ev.Salience >= cfg.NotifyMinSalience {
			out = append(out, Candidate{Level: "info", Title: "A belief became strong", Body: ev.Summary, Rule: "belief_confident",
				Reason: map[string]any{"confidence": ev.Confidence, "min_confidence": cfg.NotifyMinConfidence, "salience": ev.Salience, "min_salience": cfg.NotifyMinSalience}, AggregateKey: "belief:" + str(ev.Metadata["belief_id"]), SourceEvent: ev.ID})
		}
	case events.GoalCompleted:
		if proactive {
			out = append(out, Candidate{Level: "info", Title: "Goal completed", Body: ev.Summary, Rule: "goal_completed", Reason: map[string]any{}, AggregateKey: "goal:" + str(ev.Metadata["goal_id"]), SourceEvent: ev.ID})
		}
	case events.Metacognition:
		if ev.Metadata["check"] == "overconfidence" || ev.Metadata["check"] == "loop" {
			out = append(out, Candidate{Level: "warn", Title: "Metacognitive warning", Body: ev.Summary, Rule: "metacognition", Reason: map[string]any{"check": ev.Metadata["check"]}, AggregateKey: "meta:" + str(ev.Metadata["check"]), SourceEvent: ev.ID})
		}
	case events.ToolRequested:
		out = append(out, Candidate{Level: "alert", Title: "An action needs your approval", Body: ev.Summary, Rule: "tool_requested", Reason: map[string]any{"risk": ev.Metadata["risk"]}, AggregateKey: "approval:" + str(ev.Metadata["approval_id"]), SourceEvent: ev.ID})
	case events.ToolDenied:
		out = append(out, Candidate{Level: "alert", Title: "An action was denied by policy", Body: ev.Summary, Rule: "tool_denied", Reason: map[string]any{}, AggregateKey: "denied:" + str(ev.Metadata["capability"]), SourceEvent: ev.ID})
	case events.Observation:
		if ev.Salience >= 0.95 && proactive {
			out = append(out, Candidate{Level: "alert", Title: "High-salience observation", Body: ev.Summary, Rule: "observation_salient", Reason: map[string]any{"salience": ev.Salience, "threshold": 0.95}, AggregateKey: "obs:" + firstWords(ev.Summary, 4), SourceEvent: ev.ID})
		}
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func firstWords(s string, n int) string {
	f := strings.Fields(strings.ToLower(s))
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

// Store persists notifications.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Deliver records a candidate unless one with the same aggregate key exists
// within the cooldown, in which case that one's count is incremented and the
// candidate is reported as suppressed. Returns the notification and whether
// it was suppressed.
func (s *Store) Deliver(ctx context.Context, q storage.Querier, mind ids.ID, c Candidate, cooldown time.Duration) (Notification, bool, error) {
	if c.AggregateKey != "" {
		var n Notification
		err := q.QueryRow(ctx, `UPDATE notifications SET count=count+1, updated_at=now() WHERE id = (
			SELECT id FROM notifications WHERE mind_id=$1 AND aggregate_key=$2 AND created_at > $3 ORDER BY created_at DESC LIMIT 1) RETURNING id, count`, mind, c.AggregateKey, time.Now().Add(-cooldown)).Scan(&n.ID, &n.Count)
		if err == nil {
			n.MindID, n.AggregateKey, n.Rule, n.Title = mind, c.AggregateKey, c.Rule, c.Title
			return n, true, nil
		}
		if err != pgx.ErrNoRows {
			return Notification{}, false, err
		}
	}
	reason, _ := json.Marshal(c.Reason)
	if c.Reason == nil {
		reason = []byte("{}")
	}
	n := Notification{ID: ids.New(), MindID: mind, Level: c.Level, Title: c.Title, Body: c.Body, Rule: c.Rule, Reason: reason, AggregateKey: c.AggregateKey, Count: 1, Status: "new", SourceEvent: c.SourceEvent, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err := q.Exec(ctx, `INSERT INTO notifications (id, mind_id, level, title, body, rule, reason, aggregate_key, source_event) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		n.ID, n.MindID, n.Level, n.Title, n.Body, n.Rule, reason, n.AggregateKey, nullID(n.SourceEvent))
	return n, false, err
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

const cols = `id, mind_id, level, title, body, rule, reason, aggregate_key, count, status, source_event, created_at, updated_at, read_at`

func scan(row pgx.Row) (Notification, error) {
	var n Notification
	var src *string
	if err := row.Scan(&n.ID, &n.MindID, &n.Level, &n.Title, &n.Body, &n.Rule, &n.Reason, &n.AggregateKey, &n.Count, &n.Status, &src, &n.CreatedAt, &n.UpdatedAt, &n.ReadAt); err != nil {
		return n, storage.Translate(err)
	}
	if src != nil {
		n.SourceEvent = ids.ID(*src)
	}
	return n, nil
}

// List returns notifications newest first; mind Nil means all minds.
func (s *Store) List(ctx context.Context, mind ids.ID, status string, limit int) ([]Notification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM notifications WHERE ($1::uuid IS NULL OR mind_id=$1) AND ($2='' OR status=$2) ORDER BY updated_at DESC LIMIT $3`, nullID(mind), status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead sets status read.
func (s *Store) MarkRead(ctx context.Context, mind, id ids.ID) error {
	tag, err := s.db.Exec(ctx, `UPDATE notifications SET status='read', read_at=now(), updated_at=now() WHERE id=$1 AND ($2::uuid IS NULL OR mind_id=$2) AND status='new'`, id, nullID(mind))
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// MarkAllRead marks a mind's new notifications read.
func (s *Store) MarkAllRead(ctx context.Context, mind ids.ID) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE notifications SET status='read', read_at=now(), updated_at=now() WHERE ($1::uuid IS NULL OR mind_id=$1) AND status='new'`, nullID(mind))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CountNew counts unread notifications (all minds when Nil).
func (s *Store) CountNew(ctx context.Context, mind ids.ID) (int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE ($1::uuid IS NULL OR mind_id=$1) AND status='new'`, nullID(mind)).Scan(&n)
	return n, err
}
