package epistemics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/ids"
)

// Assertion is the append-only record that an actor took a stance.
type Assertion struct {
	ID             ids.ID    `json:"id"`
	MindID         ids.ID    `json:"mind_id"`
	Actor          string    `json:"actor"`
	ActorKind      string    `json:"actor_kind"`
	AttributedTo   string    `json:"attributed_to"`
	Proposition    string    `json:"proposition"`
	Subject        string    `json:"subject"`
	Stance         string    `json:"stance"`
	Certainty      string    `json:"certainty"`
	Modality       string    `json:"modality"`
	Negated        bool      `json:"negated"`
	Reported       bool      `json:"reported"`
	ReportedActor  string    `json:"reported_actor,omitempty"`
	Quoted         bool      `json:"quoted"`
	Hypothetical   bool      `json:"hypothetical"`
	Interpretation float64   `json:"interpretation_confidence"`
	Notes          []string  `json:"notes"`
	SourceEvent    ids.ID    `json:"source_event,omitempty"`
	ConversationID ids.ID    `json:"conversation_id,omitempty"`
	MessageID      ids.ID    `json:"message_id,omitempty"`
	Context        string    `json:"context,omitempty"`
	Trust          string    `json:"trust"`
	BeliefID       ids.ID    `json:"belief_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// New is the input to Record.
type New struct {
	MindID         ids.ID
	Actor          string
	ActorKind      string
	Interpretation Interpretation
	Subject        string
	SourceEvent    ids.ID
	ConversationID ids.ID
	MessageID      ids.ID
	Context        string
	Trust          string
	BeliefID       ids.ID
}

// Store persists assertions.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const cols = `id, mind_id, actor, actor_kind, attributed_to, proposition, subject, stance, certainty, modality, negated, reported, reported_actor, quoted, hypothetical, interpretation_confidence, interpretation_notes, source_event, conversation_id, message_id, context, trust, belief_id, created_at`

// Record writes an assertion inside q.
func (s *Store) Record(ctx context.Context, q storage.Querier, n New) (Assertion, error) {
	in := n.Interpretation
	a := Assertion{ID: ids.New(), MindID: n.MindID, Actor: n.Actor, ActorKind: firstNonEmpty(n.ActorKind, "user"), AttributedTo: firstNonEmpty(in.AttributedTo, n.Actor), Proposition: in.Proposition, Subject: n.Subject,
		Stance: in.Stance, Certainty: in.Certainty, Modality: in.Modality, Negated: in.Negated, Reported: in.Reported, ReportedActor: in.ReportedActor, Quoted: in.Quoted, Hypothetical: in.Hypothetical,
		Interpretation: in.Confidence, Notes: in.Notes, SourceEvent: n.SourceEvent, ConversationID: n.ConversationID, MessageID: n.MessageID, Context: n.Context, Trust: firstNonEmpty(n.Trust, "user"), BeliefID: n.BeliefID, CreatedAt: time.Now()}
	if a.Notes == nil {
		a.Notes = []string{}
	}
	if in.SpeakerStance != "" && in.SpeakerStance != a.Stance && a.AttributedTo != n.Actor {
		// The speaker's own stance is recorded separately so "Andy reports that
		// Sarah asserts X" and "Andy doubts X" are both retrievable.
		a.Notes = append(a.Notes, "speaker stance: "+in.SpeakerStance)
	}
	_, err := q.Exec(ctx, `INSERT INTO assertions (`+cols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)`,
		a.ID, a.MindID, a.Actor, a.ActorKind, a.AttributedTo, a.Proposition, a.Subject, a.Stance, a.Certainty, a.Modality, a.Negated, a.Reported, a.ReportedActor, a.Quoted, a.Hypothetical,
		a.Interpretation, a.Notes, nullID(a.SourceEvent), nullID(a.ConversationID), nullID(a.MessageID), a.Context, a.Trust, nullID(a.BeliefID), a.CreatedAt)
	return a, err
}

// Filter narrows a listing.
type Filter struct {
	AttributedTo string
	BeliefID     ids.ID
	Stance       string
	Limit        int
}

// List returns assertions for a mind, newest first.
func (s *Store) List(ctx context.Context, mind ids.ID, f Filter) ([]Assertion, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM assertions WHERE mind_id=$1 AND ($2='' OR lower(attributed_to)=lower($2)) AND ($3::uuid IS NULL OR belief_id=$3) AND ($4='' OR stance=$4) ORDER BY created_at DESC LIMIT $5`,
		mind, f.AttributedTo, nullID(f.BeliefID), f.Stance, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assertion
	for rows.Next() {
		a, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Actors summarises who has asserted things to a mind.
type ActorSummary struct {
	AttributedTo string  `json:"attributed_to"`
	Assertions   int     `json:"assertions"`
	Reported     int     `json:"reported"`
	MeanClarity  float64 `json:"mean_interpretation_confidence"`
}

// Actors lists asserting actors for a mind.
func (s *Store) Actors(ctx context.Context, mind ids.ID) ([]ActorSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT attributed_to, count(*), count(*) FILTER (WHERE reported), avg(interpretation_confidence) FROM assertions WHERE mind_id=$1 GROUP BY attributed_to ORDER BY count(*) DESC LIMIT 200`, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActorSummary
	for rows.Next() {
		var a ActorSummary
		if err := rows.Scan(&a.AttributedTo, &a.Assertions, &a.Reported, &a.MeanClarity); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scan(row pgx.Row) (Assertion, error) {
	var a Assertion
	var src, conv, msg, bel *string
	if err := row.Scan(&a.ID, &a.MindID, &a.Actor, &a.ActorKind, &a.AttributedTo, &a.Proposition, &a.Subject, &a.Stance, &a.Certainty, &a.Modality, &a.Negated, &a.Reported, &a.ReportedActor, &a.Quoted, &a.Hypothetical,
		&a.Interpretation, &a.Notes, &src, &conv, &msg, &a.Context, &a.Trust, &bel, &a.CreatedAt); err != nil {
		return a, storage.Translate(err)
	}
	a.SourceEvent, a.ConversationID, a.MessageID, a.BeliefID = idOf(src), idOf(conv), idOf(msg), idOf(bel)
	return a, nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

func idOf(s *string) ids.ID {
	if s == nil {
		return ids.Nil
	}
	return ids.ID(*s)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
