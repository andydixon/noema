// Package conversation stores conversations and messages and provides the
// deterministic first pass of message understanding. A user message is never
// sent straight to a model: it becomes an observation event that the
// cognitive cycle perceives, recalls against, reasons about and answers.
package conversation

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

// Conversation groups messages with a person, project and channel.
type Conversation struct {
	ID            ids.ID         `json:"id"`
	MindID        ids.ID         `json:"mind_id"`
	Title         string         `json:"title"`
	Channel       string         `json:"channel"`
	Person        string         `json:"person"`
	Project       string         `json:"project"`
	Context       map[string]any `json:"context"`
	CreatedAt     time.Time      `json:"created_at"`
	LastMessageAt time.Time      `json:"last_message_at"`
}

// Message is one turn.
type Message struct {
	ID             ids.ID          `json:"id"`
	ConversationID ids.ID          `json:"conversation_id"`
	MindID         ids.ID          `json:"mind_id"`
	Role           string          `json:"role"` // user | mind | system
	Actor          string          `json:"actor"`
	Content        string          `json:"content"`
	EventID        ids.ID          `json:"event_id,omitempty"`
	ReplyTo        ids.ID          `json:"reply_to,omitempty"`
	Intent         json.RawMessage `json:"intent,omitempty"`
	LLMCallID      ids.ID          `json:"llm_call_id,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("conversation: invalid")

// Store persists conversations.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Create starts a conversation.
func (s *Store) Create(ctx context.Context, mind ids.ID, title, channel, person, project string) (Conversation, error) {
	if channel == "" {
		channel = "web"
	}
	c := Conversation{ID: ids.New(), MindID: mind, Title: strings.TrimSpace(title), Channel: channel, Person: strings.TrimSpace(person), Project: strings.TrimSpace(project), Context: map[string]any{}, CreatedAt: time.Now(), LastMessageAt: time.Now()}
	_, err := s.db.Exec(ctx, `INSERT INTO conversations (id, mind_id, title, channel, person, project) VALUES ($1,$2,$3,$4,$5,$6)`, c.ID, c.MindID, c.Title, c.Channel, c.Person, c.Project)
	return c, err
}

const convCols = `id, mind_id, title, channel, person, project, context, created_at, last_message_at`

func scanConv(row pgx.Row) (Conversation, error) {
	var c Conversation
	var ctxJSON []byte
	if err := row.Scan(&c.ID, &c.MindID, &c.Title, &c.Channel, &c.Person, &c.Project, &ctxJSON, &c.CreatedAt, &c.LastMessageAt); err != nil {
		return c, storage.Translate(err)
	}
	json.Unmarshal(ctxJSON, &c.Context) //nolint:errcheck
	if c.Context == nil {
		c.Context = map[string]any{}
	}
	return c, nil
}

// Get fetches a conversation scoped to a mind.
func (s *Store) Get(ctx context.Context, mind, id ids.ID) (Conversation, error) {
	return scanConv(s.db.QueryRow(ctx, `SELECT `+convCols+` FROM conversations WHERE id=$1 AND mind_id=$2`, id, mind))
}

// List returns a mind's conversations, most recent first.
func (s *Store) List(ctx context.Context, mind ids.ID, limit int) ([]Conversation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+convCols+` FROM conversations WHERE mind_id=$1 ORDER BY last_message_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddMessage appends a message using q (pool or transaction).
func (s *Store) AddMessage(ctx context.Context, q storage.Querier, m Message) (Message, error) {
	m.Content = strings.TrimSpace(m.Content)
	if m.Content == "" || len(m.Content) > 20000 {
		return Message{}, fmt.Errorf("%w: content required (max 20000 chars)", ErrInvalid)
	}
	if m.Role != "user" && m.Role != "mind" && m.Role != "system" {
		return Message{}, fmt.Errorf("%w: bad role", ErrInvalid)
	}
	if m.ID.IsZero() {
		m.ID = ids.New()
	}
	m.CreatedAt = time.Now()
	_, err := q.Exec(ctx, `INSERT INTO messages (id, conversation_id, mind_id, role, actor, content, event_id, reply_to, intent, llm_call_id, created_at)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11 WHERE EXISTS (SELECT 1 FROM conversations WHERE id=$2 AND mind_id=$3)`,
		m.ID, m.ConversationID, m.MindID, m.Role, m.Actor, m.Content, nullID(m.EventID), nullID(m.ReplyTo), nullRaw(m.Intent), nullID(m.LLMCallID), m.CreatedAt)
	if err != nil {
		return Message{}, err
	}
	q.Exec(ctx, `UPDATE conversations SET last_message_at=$2, title=CASE WHEN title='' THEN left($3, 80) ELSE title END WHERE id=$1`, m.ConversationID, m.CreatedAt, m.Content) //nolint:errcheck
	return m, nil
}

// SetEventID links a message to its event after the event is written.
func (s *Store) SetEventID(ctx context.Context, q storage.Querier, id, eventID ids.ID) error {
	_, err := q.Exec(ctx, `UPDATE messages SET event_id=$2 WHERE id=$1`, id, eventID)
	return err
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

func nullRaw(r json.RawMessage) *json.RawMessage {
	if len(r) == 0 {
		return nil
	}
	return &r
}

// Messages lists a conversation's messages in order.
func (s *Store) Messages(ctx context.Context, mind, conv ids.ID, limit int) ([]Message, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.Query(ctx, `SELECT id, conversation_id, mind_id, role, actor, content, event_id, reply_to, intent, llm_call_id, created_at FROM messages
		WHERE conversation_id=$1 AND mind_id=$2 ORDER BY created_at DESC LIMIT $3`, conv, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

func scanMessage(row pgx.Row) (Message, error) {
	var m Message
	var ev, reply, call *string
	var intent *json.RawMessage
	if err := row.Scan(&m.ID, &m.ConversationID, &m.MindID, &m.Role, &m.Actor, &m.Content, &ev, &reply, &intent, &call, &m.CreatedAt); err != nil {
		return m, storage.Translate(err)
	}
	if ev != nil {
		m.EventID = ids.ID(*ev)
	}
	if reply != nil {
		m.ReplyTo = ids.ID(*reply)
	}
	if call != nil {
		m.LLMCallID = ids.ID(*call)
	}
	if intent != nil {
		m.Intent = *intent
	}
	return m, nil
}

// Reply returns the mind's reply to a user message, if any.
func (s *Store) Reply(ctx context.Context, mind, userMessage ids.ID) (Message, error) {
	return scanMessage(s.db.QueryRow(ctx, `SELECT id, conversation_id, mind_id, role, actor, content, event_id, reply_to, intent, llm_call_id, created_at FROM messages WHERE reply_to=$1 AND mind_id=$2 ORDER BY created_at LIMIT 1`, userMessage, mind))
}

// Recent returns the last n messages of a conversation for context.
func (s *Store) Recent(ctx context.Context, mind, conv ids.ID, n int) ([]Message, error) {
	msgs, err := s.Messages(ctx, mind, conv, n)
	return msgs, err
}

// GetMessage fetches one message.
func (s *Store) GetMessage(ctx context.Context, mind, id ids.ID) (Message, error) {
	return scanMessage(s.db.QueryRow(ctx, `SELECT id, conversation_id, mind_id, role, actor, content, event_id, reply_to, intent, llm_call_id, created_at FROM messages WHERE id=$1 AND mind_id=$2`, id, mind))
}
