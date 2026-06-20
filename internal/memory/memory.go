// Package memory implements long-term memory: episodic, semantic, procedural
// and autobiographical records with explicit epistemic status, credibility,
// importance and decaying activation, plus deterministic fused retrieval
// (ADR-0003). Nothing here is deleted by decay.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/embeddings"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Status of a memory record. Retracted and superseded memories stay
// retrievable for provenance but are excluded from normal recall.
type Status string

// Statuses.
const (
	StatusActive     Status = "active"
	StatusRetracted  Status = "retracted"
	StatusSuperseded Status = "superseded"
	StatusArchived   Status = "archived"
)

// Memory is one long-term record.
type Memory struct {
	ID              ids.ID              `json:"id"`
	MindID          ids.ID              `json:"mind_id"`
	Kind            concepts.MemoryKind `json:"kind"`
	Epistemic       concepts.Epistemic  `json:"epistemic"`
	Status          Status              `json:"status"`
	Content         string              `json:"content"`
	Detail          map[string]any      `json:"detail"`
	Source          string              `json:"source"`
	Trust           events.Trust        `json:"trust"`
	Credibility     float64             `json:"credibility"`
	Confidence      float64             `json:"confidence"`
	Importance      float64             `json:"importance"`
	Salience        float64             `json:"salience"`
	Activation      float64             `json:"activation"`
	Valence         *float64            `json:"valence,omitempty"`
	Arousal         *float64            `json:"arousal,omitempty"`
	OccurredAt      time.Time           `json:"occurred_at"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
	RetrievalCount  int                 `json:"retrieval_count"`
	LastRetrievedAt *time.Time          `json:"last_retrieved_at,omitempty"`
	OriginEvent     ids.ID              `json:"origin_event,omitempty"`
	Supersedes      ids.ID              `json:"supersedes,omitempty"`
}

// New describes a memory to store.
type New struct {
	MindID      ids.ID
	Kind        concepts.MemoryKind
	Epistemic   concepts.Epistemic
	Content     string
	Detail      map[string]any
	Source      string
	Trust       events.Trust
	Confidence  float64
	Importance  float64
	Salience    float64
	Valence     *float64
	Arousal     *float64
	OccurredAt  time.Time
	OriginEvent ids.ID
	Supersedes  ids.ID
}

// Relationship is an edge between two memories.
type Relationship struct {
	ID         ids.ID            `json:"id"`
	FromMemory ids.ID            `json:"from_memory"`
	ToMemory   ids.ID            `json:"to_memory"`
	Relation   concepts.Relation `json:"relation"`
	Confidence float64           `json:"confidence"`
	Source     string            `json:"source"`
	CreatedAt  time.Time         `json:"created_at"`
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("invalid memory")

// Store persists memories.
type Store struct {
	db       *pgxpool.Pool
	embedder embeddings.Embedder
	vectors  bool // memory_embeddings exists (pgvector available)
}

// NewStore creates a store and detects vector support.
func NewStore(ctx context.Context, db *pgxpool.Pool, embedder embeddings.Embedder) *Store {
	s := &Store{db: db, embedder: embedder}
	if embedder == nil {
		s.embedder = embeddings.None{}
	}
	db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'memory_embeddings')`).Scan(&s.vectors) //nolint:errcheck
	return s
}

// VectorsEnabled reports whether pgvector storage is present.
func (s *Store) VectorsEnabled() bool { return s.vectors }

// SemanticEnabled reports whether semantic (embedding) retrieval can run.
func (s *Store) SemanticEnabled() bool { return s.vectors && s.embedder.Model() != "" }

// SetEmbedder swaps the embedder (providers are configured at runtime).
func (s *Store) SetEmbedder(e embeddings.Embedder) {
	if e == nil {
		e = embeddings.None{}
	}
	s.embedder = e
}

func (n *New) validate() error {
	if n.MindID.IsZero() {
		return fmt.Errorf("%w: mind id required", ErrInvalid)
	}
	if !n.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, n.Kind)
	}
	if !n.Epistemic.Valid() {
		return fmt.Errorf("%w: unknown epistemic kind %q", ErrInvalid, n.Epistemic)
	}
	n.Content = strings.TrimSpace(n.Content)
	if n.Content == "" || len(n.Content) > 20000 {
		return fmt.Errorf("%w: content required (max 20000 chars)", ErrInvalid)
	}
	if !n.Trust.Valid() {
		n.Trust = events.TrustLow
	}
	n.Confidence, n.Importance, n.Salience = clamp(n.Confidence), clamp(n.Importance), clamp(n.Salience)
	if n.OccurredAt.IsZero() {
		n.OccurredAt = time.Now()
	}
	if n.Detail == nil {
		n.Detail = map[string]any{}
	}
	return nil
}

func clamp(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Create stores a memory using q (a pool or transaction). Embedding happens
// best-effort after the row exists so a slow provider never blocks encoding.
func (s *Store) Create(ctx context.Context, q storage.Querier, n New) (Memory, error) {
	if err := n.validate(); err != nil {
		return Memory{}, err
	}
	m := Memory{ID: ids.New(), MindID: n.MindID, Kind: n.Kind, Epistemic: n.Epistemic, Status: StatusActive, Content: n.Content, Detail: n.Detail, Source: n.Source,
		Trust: n.Trust, Credibility: n.Trust.Credibility(), Confidence: n.Confidence, Importance: n.Importance, Salience: n.Salience, Activation: 1,
		Valence: n.Valence, Arousal: n.Arousal, OccurredAt: n.OccurredAt, CreatedAt: time.Now(), UpdatedAt: time.Now(), OriginEvent: n.OriginEvent, Supersedes: n.Supersedes}
	detail, _ := json.Marshal(n.Detail)
	_, err := q.Exec(ctx, `INSERT INTO memories (id, mind_id, kind, epistemic, content, detail, source, trust, credibility, confidence, importance, salience, valence, arousal, occurred_at, origin_event, supersedes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		m.ID, m.MindID, string(m.Kind), string(m.Epistemic), m.Content, detail, m.Source, string(m.Trust), m.Credibility, m.Confidence, m.Importance, m.Salience, m.Valence, m.Arousal, m.OccurredAt, nullID(m.OriginEvent), nullID(m.Supersedes))
	if err != nil {
		return Memory{}, storage.Translate(err)
	}
	if !n.Supersedes.IsZero() {
		q.Exec(ctx, `UPDATE memories SET status='superseded', updated_at=now() WHERE id=$1 AND mind_id=$2`, n.Supersedes, n.MindID) //nolint:errcheck
	}
	return m, nil
}

// Embed computes and stores the embedding for a memory if a provider exists.
// Safe to call from a background task; errors are returned for logging only.
func (s *Store) Embed(ctx context.Context, m Memory) error {
	if !s.SemanticEnabled() {
		return nil
	}
	vecs, err := s.embedder.Embed(ctx, []string{m.Content})
	if err != nil || len(vecs) != 1 {
		return fmt.Errorf("embed: %w", err)
	}
	_, err = s.db.Exec(ctx, `INSERT INTO memory_embeddings (memory_id, model, dims, embedding) VALUES ($1,$2,$3,$4::vector)
		ON CONFLICT (memory_id, model) DO UPDATE SET embedding=excluded.embedding, dims=excluded.dims, created_at=now()`, m.ID, s.embedder.Model(), len(vecs[0]), embeddings.Format(vecs[0]))
	return err
}

// EmbedMissing embeds up to limit memories lacking a vector for the current model.
func (s *Store) EmbedMissing(ctx context.Context, limit int) (int, error) {
	if !s.SemanticEnabled() {
		return 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM memories m WHERE status='active' AND NOT EXISTS (SELECT 1 FROM memory_embeddings e WHERE e.memory_id=m.id AND e.model=$1) ORDER BY created_at DESC LIMIT $2`, s.embedder.Model(), limit)
	if err != nil {
		return 0, err
	}
	var list []Memory
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, m)
	}
	rows.Close()
	n := 0
	for _, m := range list {
		if err := s.Embed(ctx, m); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

const cols = `m.id, m.mind_id, m.kind, m.epistemic, m.status, m.content, m.detail, m.source, m.trust, m.credibility, m.confidence, m.importance, m.salience, m.activation, m.valence, m.arousal, m.occurred_at, m.created_at, m.updated_at, m.retrieval_count, m.last_retrieved_at, m.origin_event, m.supersedes`

func scan(row pgx.Row) (Memory, error) {
	var m Memory
	var kind, epi, status, trust string
	var detail []byte
	var origin, supersedes *string
	err := row.Scan(&m.ID, &m.MindID, &kind, &epi, &status, &m.Content, &detail, &m.Source, &trust, &m.Credibility, &m.Confidence, &m.Importance, &m.Salience, &m.Activation,
		&m.Valence, &m.Arousal, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt, &m.RetrievalCount, &m.LastRetrievedAt, &origin, &supersedes)
	if err != nil {
		return m, storage.Translate(err)
	}
	m.Kind, m.Epistemic, m.Status, m.Trust = concepts.MemoryKind(kind), concepts.Epistemic(epi), Status(status), events.Trust(trust)
	json.Unmarshal(detail, &m.Detail) //nolint:errcheck
	if m.Detail == nil {
		m.Detail = map[string]any{}
	}
	if origin != nil {
		m.OriginEvent = ids.ID(*origin)
	}
	if supersedes != nil {
		m.Supersedes = ids.ID(*supersedes)
	}
	return m, nil
}

// Get fetches a memory scoped to a mind (cross-mind reads are impossible by construction).
func (s *Store) Get(ctx context.Context, mind, id ids.ID) (Memory, error) {
	return scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM memories m WHERE m.id=$1 AND m.mind_id=$2`, id, mind))
}

// Filter selects memories for listing.
type Filter struct {
	Kinds         []concepts.MemoryKind
	Epistemic     []concepts.Epistemic
	Statuses      []Status // default: active
	MinConfidence float64
	MinImportance float64
	Source        string
	Since, Until  time.Time
	Search        string // plain text search (websearch syntax)
	Before        time.Time
	Limit         int
}

// List returns memories newest first (by occurred_at) with keyset paging on Before.
func (s *Store) List(ctx context.Context, mind ids.ID, f Filter) ([]Memory, error) {
	where := []string{"m.mind_id = $1"}
	args := []any{mind}
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if len(f.Kinds) > 0 {
		where = append(where, "m.kind = ANY("+arg(strs(f.Kinds))+")")
	}
	if len(f.Epistemic) > 0 {
		where = append(where, "m.epistemic = ANY("+arg(strs(f.Epistemic))+")")
	}
	if len(f.Statuses) > 0 {
		where = append(where, "m.status = ANY("+arg(strs(f.Statuses))+")")
	} else {
		where = append(where, "m.status = 'active'")
	}
	if f.MinConfidence > 0 {
		where = append(where, "m.confidence >= "+arg(f.MinConfidence))
	}
	if f.MinImportance > 0 {
		where = append(where, "m.importance >= "+arg(f.MinImportance))
	}
	if f.Source != "" {
		where = append(where, "m.source = "+arg(f.Source))
	}
	if !f.Since.IsZero() {
		where = append(where, "m.occurred_at >= "+arg(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "m.occurred_at <= "+arg(f.Until))
	}
	if !f.Before.IsZero() {
		where = append(where, "m.occurred_at < "+arg(f.Before))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		where = append(where, "m.search @@ websearch_to_tsquery('english', "+arg(q)+")")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM memories m WHERE `+strings.Join(where, " AND ")+` ORDER BY m.occurred_at DESC, m.id DESC LIMIT `+arg(limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		m, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// SetStatus changes status (retract, archive). It never deletes.
func (s *Store) SetStatus(ctx context.Context, mind, id ids.ID, st Status) error {
	tag, err := s.db.Exec(ctx, `UPDATE memories SET status=$3, updated_at=now() WHERE id=$1 AND mind_id=$2`, id, mind, string(st))
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// UpdateConfidence sets a new confidence (used by consolidation and corrections).
func (s *Store) UpdateConfidence(ctx context.Context, q storage.Querier, mind, id ids.ID, confidence float64) error {
	tag, err := q.Exec(ctx, `UPDATE memories SET confidence=$3, updated_at=now() WHERE id=$1 AND mind_id=$2`, id, mind, clamp(confidence))
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// Reinforce records a retrieval: count, timestamp and activation towards 1.
func (s *Store) Reinforce(ctx context.Context, q storage.Querier, mind, id ids.ID) error {
	_, err := q.Exec(ctx, `UPDATE memories SET retrieval_count = retrieval_count + 1, last_retrieved_at = now(), activation = LEAST(1, activation + (1 - activation) * 0.5), updated_at = now() WHERE id=$1 AND mind_id=$2`, id, mind)
	return err
}

// Decay lowers activation of memories not retrieved recently. Importance slows
// decay; nothing is deleted. Returns the number of rows touched.
func (s *Store) Decay(ctx context.Context, mind ids.ID, halfLife time.Duration) (int64, error) {
	if halfLife <= 0 {
		halfLife = 14 * 24 * time.Hour
	}
	// activation *= 0.5 ^ (age / (halfLife * (1 + importance)))
	tag, err := s.db.Exec(ctx, `UPDATE memories SET activation = GREATEST(0.01, activation * power(0.5, EXTRACT(EPOCH FROM (now() - coalesce(last_retrieved_at, created_at))) / ($2 * (1 + importance)))), updated_at = now()
		WHERE mind_id=$1 AND status='active' AND activation > 0.01 AND coalesce(last_retrieved_at, created_at) < now() - interval '1 hour'`, mind, halfLife.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Relate adds an edge between two memories of the same mind.
func (s *Store) Relate(ctx context.Context, q storage.Querier, mind, from, to ids.ID, rel concepts.Relation, confidence float64, source string) (Relationship, error) {
	if !rel.Valid() {
		return Relationship{}, fmt.Errorf("%w: unknown relation %q", ErrInvalid, rel)
	}
	if from == to {
		return Relationship{}, fmt.Errorf("%w: self relation", ErrInvalid)
	}
	r := Relationship{ID: ids.New(), FromMemory: from, ToMemory: to, Relation: rel, Confidence: clamp(confidence), Source: source, CreatedAt: time.Now()}
	// Both endpoints must belong to the mind; the subquery enforces it.
	tag, err := q.Exec(ctx, `INSERT INTO memory_relationships (id, mind_id, from_memory, to_memory, relation, confidence, source)
		SELECT $1, $2, $3, $4, $5, $6, $7 WHERE (SELECT count(*) FROM memories WHERE id IN ($3,$4) AND mind_id=$2) = 2
		ON CONFLICT (from_memory, to_memory, relation) DO UPDATE SET confidence = GREATEST(memory_relationships.confidence, excluded.confidence)`,
		r.ID, mind, from, to, string(rel), r.Confidence, source)
	if err != nil {
		return Relationship{}, err
	}
	if tag.RowsAffected() == 0 {
		return Relationship{}, fmt.Errorf("%w: memories not found in mind", ErrInvalid)
	}
	return r, nil
}

// Related lists edges touching a memory, with the other endpoint loaded.
type Related struct {
	Relationship
	Other     Memory `json:"other"`
	Direction string `json:"direction"` // out | in
}

// Relationships returns edges in both directions.
func (s *Store) Relationships(ctx context.Context, mind, id ids.ID) ([]Related, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id, r.from_memory, r.to_memory, r.relation, r.confidence, r.source, r.created_at, CASE WHEN r.from_memory=$1 THEN 'out' ELSE 'in' END, `+cols+`
		FROM memory_relationships r JOIN memories m ON m.id = CASE WHEN r.from_memory=$1 THEN r.to_memory ELSE r.from_memory END
		WHERE r.mind_id=$2 AND (r.from_memory=$1 OR r.to_memory=$1) ORDER BY r.created_at`, id, mind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Related
	for rows.Next() {
		var r Related
		var rel string
		var kind, epi, status, trust string
		var detail []byte
		var origin, supersedes *string
		m := &r.Other
		if err := rows.Scan(&r.ID, &r.FromMemory, &r.ToMemory, &rel, &r.Confidence, &r.Source, &r.CreatedAt, &r.Direction,
			&m.ID, &m.MindID, &kind, &epi, &status, &m.Content, &detail, &m.Source, &trust, &m.Credibility, &m.Confidence, &m.Importance, &m.Salience, &m.Activation,
			&m.Valence, &m.Arousal, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt, &m.RetrievalCount, &m.LastRetrievedAt, &origin, &supersedes); err != nil {
			return nil, err
		}
		r.Relation = concepts.Relation(rel)
		m.Kind, m.Epistemic, m.Status, m.Trust = concepts.MemoryKind(kind), concepts.Epistemic(epi), Status(status), events.Trust(trust)
		json.Unmarshal(detail, &m.Detail) //nolint:errcheck
		if origin != nil {
			m.OriginEvent = ids.ID(*origin)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Stats summarises a mind's memory for overview pages.
type Stats struct {
	Total  int64            `json:"total"`
	ByKind map[string]int64 `json:"by_kind"`
}

// Stats counts active memories by kind.
func (s *Store) Stats(ctx context.Context, mind ids.ID) (Stats, error) {
	st := Stats{ByKind: map[string]int64{}}
	rows, err := s.db.Query(ctx, `SELECT kind, count(*) FROM memories WHERE mind_id=$1 AND status='active' GROUP BY kind`, mind)
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
		st.ByKind[k] = n
		st.Total += n
	}
	return st, rows.Err()
}
