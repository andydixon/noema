// Package goals stores a mind's goal tree: what it is trying to do, why, how
// far it has got and what blocks it. Goals bias attention through the
// goal-relevance feature and are created by operators, curiosity and
// reflection; they never grant capabilities.
package goals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Status of a goal.
type Status string

// Statuses.
const (
	StatusActive    Status = "active"
	StatusBlocked   Status = "blocked"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusAbandoned Status = "abandoned"
)

// Kind of goal.
type Kind string

// Kinds. Investigation and question goals come from curiosity; task goals
// from operators; maintenance from reflection.
const (
	KindTask          Kind = "task"
	KindInvestigation Kind = "investigation"
	KindQuestion      Kind = "question"
	KindMaintenance   Kind = "maintenance"
)

// Goal is one node of the tree.
type Goal struct {
	ID             ids.ID     `json:"id"`
	MindID         ids.ID     `json:"mind_id"`
	ParentID       ids.ID     `json:"parent_id,omitempty"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Kind           Kind       `json:"kind"`
	Status         Status     `json:"status"`
	Priority       float64    `json:"priority"`
	Progress       float64    `json:"progress"`
	Owner          string     `json:"owner"`
	Blockers       []string   `json:"blockers"`
	OriginEvent    ids.ID     `json:"origin_event,omitempty"`
	Subject        string     `json:"subject"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	LastProgressAt time.Time  `json:"last_progress_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("goals: invalid")

// Store persists goals.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// New describes a goal to create.
type New struct {
	MindID      ids.ID
	ParentID    ids.ID
	Title       string
	Description string
	Kind        Kind
	Priority    float64
	Owner       string
	Subject     string
	OriginEvent ids.ID
	DueAt       *time.Time
}

// Create inserts a goal using q.
func (s *Store) Create(ctx context.Context, q storage.Querier, n New) (Goal, error) {
	n.Title = strings.TrimSpace(n.Title)
	if n.MindID.IsZero() || n.Title == "" || len(n.Title) > 300 {
		return Goal{}, fmt.Errorf("%w: title required (max 300 chars)", ErrInvalid)
	}
	switch n.Kind {
	case KindTask, KindInvestigation, KindQuestion, KindMaintenance:
	default:
		n.Kind = KindTask
	}
	if n.Owner == "" {
		n.Owner = "mind"
	}
	if n.Priority <= 0 || n.Priority > 1 {
		n.Priority = 0.5
	}
	g := Goal{ID: ids.New(), MindID: n.MindID, ParentID: n.ParentID, Title: n.Title, Description: n.Description, Kind: n.Kind, Status: StatusActive, Priority: n.Priority, Owner: n.Owner,
		Blockers: []string{}, OriginEvent: n.OriginEvent, Subject: n.Subject, DueAt: n.DueAt, LastProgressAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	_, err := q.Exec(ctx, `INSERT INTO goals (id, mind_id, parent_id, title, description, kind, status, priority, owner, origin_event, subject, due_at)
		SELECT $1,$2,$3,$4,$5,$6,'active',$7,$8,$9,$10,$11 WHERE $3::uuid IS NULL OR EXISTS (SELECT 1 FROM goals WHERE id=$3 AND mind_id=$2)`,
		g.ID, g.MindID, nullID(g.ParentID), g.Title, g.Description, string(g.Kind), g.Priority, g.Owner, nullID(g.OriginEvent), g.Subject, g.DueAt)
	if err != nil {
		return Goal{}, err
	}
	return g, nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

const cols = `id, mind_id, parent_id, title, description, kind, status, priority, progress, owner, blockers, origin_event, subject, due_at, last_progress_at, created_at, updated_at, completed_at`

func scan(row pgx.Row) (Goal, error) {
	var g Goal
	var parent, origin *string
	var kind, status string
	var blockers []byte
	if err := row.Scan(&g.ID, &g.MindID, &parent, &g.Title, &g.Description, &kind, &status, &g.Priority, &g.Progress, &g.Owner, &blockers, &origin, &g.Subject, &g.DueAt, &g.LastProgressAt, &g.CreatedAt, &g.UpdatedAt, &g.CompletedAt); err != nil {
		return g, storage.Translate(err)
	}
	g.Kind, g.Status = Kind(kind), Status(status)
	if parent != nil {
		g.ParentID = ids.ID(*parent)
	}
	if origin != nil {
		g.OriginEvent = ids.ID(*origin)
	}
	json.Unmarshal(blockers, &g.Blockers) //nolint:errcheck
	if g.Blockers == nil {
		g.Blockers = []string{}
	}
	return g, nil
}

// Get fetches a goal scoped to a mind.
func (s *Store) Get(ctx context.Context, mind, id ids.ID) (Goal, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM goals WHERE id=$1 AND mind_id=$2`, id, mind))
}

// List returns goals, optionally filtered by status, priority descending.
func (s *Store) List(ctx context.Context, mind ids.ID, statuses []Status, limit int) ([]Goal, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ss := make([]string, len(statuses))
	for i, st := range statuses {
		ss[i] = string(st)
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM goals WHERE mind_id=$1 AND (cardinality($2::text[])=0 OR status = ANY($2)) ORDER BY priority DESC, created_at LIMIT $3`, mind, ss, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Active returns live goals (active or blocked) for attention scoring.
func (s *Store) Active(ctx context.Context, mind ids.ID) ([]Goal, error) {
	return s.List(ctx, mind, []Status{StatusActive, StatusBlocked}, 100)
}

// Update changes mutable fields. Nil pointers leave a field untouched.
type Update struct {
	Status   *Status
	Progress *float64
	Priority *float64
	Blockers *[]string
	Title    *string
	Desc     *string
}

// Apply updates a goal and returns the new row; status transitions set timestamps.
func (s *Store) Apply(ctx context.Context, q storage.Querier, mind, id ids.ID, u Update) (Goal, error) {
	g, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM goals WHERE id=$1 AND mind_id=$2`, id, mind))
	if err != nil {
		return Goal{}, err
	}
	if u.Status != nil {
		switch *u.Status {
		case StatusActive, StatusBlocked, StatusPaused, StatusCompleted, StatusAbandoned:
			g.Status = *u.Status
		default:
			return Goal{}, fmt.Errorf("%w: unknown status", ErrInvalid)
		}
		if g.Status == StatusCompleted {
			now := time.Now()
			g.CompletedAt, g.Progress = &now, 1
		}
	}
	if u.Progress != nil {
		g.Progress = clamp(*u.Progress)
		g.LastProgressAt = time.Now()
	}
	if u.Priority != nil {
		g.Priority = clamp(*u.Priority)
	}
	if u.Blockers != nil {
		g.Blockers = *u.Blockers
		if len(g.Blockers) > 0 && g.Status == StatusActive {
			g.Status = StatusBlocked
		} else if len(g.Blockers) == 0 && g.Status == StatusBlocked {
			g.Status = StatusActive
		}
	}
	if u.Title != nil && strings.TrimSpace(*u.Title) != "" {
		g.Title = strings.TrimSpace(*u.Title)
	}
	if u.Desc != nil {
		g.Description = *u.Desc
	}
	blockers, _ := json.Marshal(g.Blockers)
	_, err = q.Exec(ctx, `UPDATE goals SET status=$3, progress=$4, priority=$5, blockers=$6, title=$7, description=$8, last_progress_at=$9, completed_at=$10, updated_at=now() WHERE id=$1 AND mind_id=$2`,
		id, mind, string(g.Status), g.Progress, g.Priority, blockers, g.Title, g.Description, g.LastProgressAt, g.CompletedAt)
	return g, err
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Stalled returns active goals without progress for longer than d.
func (s *Store) Stalled(ctx context.Context, mind ids.ID, d time.Duration) ([]Goal, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM goals WHERE mind_id=$1 AND status='active' AND last_progress_at < $2 ORDER BY priority DESC LIMIT 20`, mind, time.Now().Add(-d))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// FindBySubject returns a live goal about a subject, for de-duplication.
func (s *Store) FindBySubject(ctx context.Context, q storage.Querier, mind ids.ID, kind Kind, subject string) (Goal, error) {
	return scan(q.QueryRow(ctx, `SELECT `+cols+` FROM goals WHERE mind_id=$1 AND kind=$2 AND subject=$3 AND status IN ('active','blocked','paused') ORDER BY created_at DESC LIMIT 1`, mind, string(kind), subject))
}

// Stats counts goals by status.
type Stats struct {
	ByStatus map[string]int64 `json:"by_status"`
	Total    int64            `json:"total"`
}

// Stats returns counts.
func (s *Store) Stats(ctx context.Context, mind ids.ID) (Stats, error) {
	st := Stats{ByStatus: map[string]int64{}}
	rows, err := s.db.Query(ctx, `SELECT status, count(*) FROM goals WHERE mind_id=$1 GROUP BY status`, mind)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return st, err
		}
		st.ByStatus[k] = n
		st.Total += n
	}
	return st, rows.Err()
}

// Relevance scores how much text relates to the active goals: the best token
// overlap with any goal, weighted by that goal's priority. Deterministic and
// cheap; it feeds the goal_relevance attention feature.
func Relevance(text string, active []Goal) (float64, ids.ID) {
	toks := tokenSet(text)
	if len(toks) == 0 {
		return 0, ids.Nil
	}
	var best float64
	var bestID ids.ID
	for _, g := range active {
		gt := tokenSet(g.Title + " " + g.Subject + " " + g.Description)
		if len(gt) == 0 {
			continue
		}
		inter := 0
		for w := range toks {
			if gt[w] {
				inter++
			}
		}
		if inter == 0 {
			continue
		}
		score := float64(inter) / float64(min(len(toks), len(gt))) * (0.5 + 0.5*g.Priority)
		if score > best {
			best, bestID = score, g.ID
		}
	}
	if best > 1 {
		best = 1
	}
	return best, bestID
}

var stop = map[string]bool{"the": true, "a": true, "an": true, "is": true, "are": true, "of": true, "to": true, "in": true, "on": true, "at": true, "for": true, "and": true, "or": true, "that": true, "this": true, "it": true, "with": true, "why": true, "what": true, "how": true, "did": true, "does": true, "do": true}

func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		w := strings.Trim(cur.String(), ".-_")
		cur.Reset()
		if len(w) >= 3 && !stop[w] {
			out[w] = true
		}
	}
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}
