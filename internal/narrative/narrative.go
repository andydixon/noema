// Package narrative keeps a mind's concise internal narrative: what it is
// doing, why, what happened recently, what it is trying to resolve and what
// might happen next. It is built deterministically from state; a model may
// rephrase it but never adds content (spec §17).
package narrative

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Structured is the machine-readable narrative.
type Structured struct {
	Doing       []string  `json:"doing"`
	Why         []string  `json:"why"`
	Recently    []string  `json:"recently"`
	Resolving   []string  `json:"resolving"`
	Next        []string  `json:"next"`
	Mode        string    `json:"mode"`
	Cycle       int64     `json:"cycle"`
	GeneratedAt time.Time `json:"generated_at"`
}

// Narrative is the stored record.
type Narrative struct {
	MindID     ids.ID     `json:"mind_id"`
	Text       string     `json:"text"`
	Structured Structured `json:"structured"`
	EventID    ids.ID     `json:"event_id,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Render turns the structured narrative into plain first-person prose.
func Render(s Structured) string {
	var b strings.Builder
	line := func(label string, items []string, empty string) {
		if len(items) == 0 {
			if empty != "" {
				b.WriteString(label + " " + empty + "\n")
			}
			return
		}
		b.WriteString(label + " " + strings.Join(items, "; ") + ".\n")
	}
	line("I am", s.Doing, "not attending to anything in particular.")
	line("Because", s.Why, "")
	line("Recently:", s.Recently, "nothing notable.")
	line("Trying to resolve:", s.Resolving, "nothing open.")
	line("Next:", s.Next, "waiting for input.")
	return strings.TrimSpace(b.String())
}

// Store persists narratives.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Save upserts the narrative.
func (s *Store) Save(ctx context.Context, q storage.Querier, mind ids.ID, text string, st Structured, eventID ids.ID) error {
	raw, _ := json.Marshal(st)
	var ev *string
	if !eventID.IsZero() {
		e := string(eventID)
		ev = &e
	}
	_, err := q.Exec(ctx, `INSERT INTO narratives (mind_id, text, structured, event_id) VALUES ($1,$2,$3,$4)
		ON CONFLICT (mind_id) DO UPDATE SET text=excluded.text, structured=excluded.structured, event_id=excluded.event_id, updated_at=now()`, mind, text, raw, ev)
	return err
}

// Get returns the current narrative.
func (s *Store) Get(ctx context.Context, mind ids.ID) (Narrative, error) {
	var n Narrative
	var raw []byte
	var ev *string
	err := s.db.QueryRow(ctx, `SELECT mind_id, text, structured, event_id, updated_at FROM narratives WHERE mind_id=$1`, mind).Scan(&n.MindID, &n.Text, &raw, &ev, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Narrative{MindID: mind, Text: "No narrative yet."}, nil
		}
		return n, storage.Translate(err)
	}
	json.Unmarshal(raw, &n.Structured) //nolint:errcheck
	if ev != nil {
		n.EventID = ids.ID(*ev)
	}
	return n, nil
}

// Changed reports whether the new structure differs enough to record.
func Changed(old, new Structured) bool {
	return fmt.Sprint(old.Doing) != fmt.Sprint(new.Doing) || fmt.Sprint(old.Resolving) != fmt.Sprint(new.Resolving) || fmt.Sprint(old.Next) != fmt.Sprint(new.Next)
}
