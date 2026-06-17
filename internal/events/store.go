package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Store appends and queries cognitive events.
type Store struct {
	db      *pgxpool.Pool
	bus     *Bus
	metrics *observability.Metrics
}

// NewStore creates the store. bus and metrics may be nil in tests.
func NewStore(db *pgxpool.Pool, bus *Bus, metrics *observability.Metrics) *Store {
	if bus == nil {
		bus = NewBus(1000)
	}
	return &Store{db: db, bus: bus, metrics: metrics}
}

// Bus exposes the fan-out bus for subscribers.
func (s *Store) Bus() *Bus { return s.bus }

// ErrInvalid marks malformed events.
var ErrInvalid = errors.New("invalid event")

func validate(n *events.New) error {
	if n.MindID.IsZero() {
		return fmt.Errorf("%w: mind id required", ErrInvalid)
	}
	if !n.Type.Valid() {
		return fmt.Errorf("%w: unknown type %q", ErrInvalid, n.Type)
	}
	if strings.TrimSpace(n.Summary) == "" {
		return fmt.Errorf("%w: summary required", ErrInvalid)
	}
	if n.Source == "" {
		n.Source = "unknown"
	}
	if len(n.Summary) > 2000 {
		n.Summary = n.Summary[:2000]
	}
	n.Salience = clamp(n.Salience, 0, 1)
	n.Confidence = clamp(n.Confidence, 0, 1)
	if n.Valence != nil {
		v := clamp(*n.Valence, -1, 1)
		n.Valence = &v
	}
	if n.Arousal != nil {
		a := clamp(*n.Arousal, 0, 1)
		n.Arousal = &a
	}
	if n.OccurredAt.IsZero() {
		n.OccurredAt = time.Now()
	}
	return nil
}

func clamp(v, lo, hi float64) float64 {
	if v != v { // NaN
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Append writes events in one transaction and publishes them after commit.
func (s *Store) Append(ctx context.Context, news ...events.New) ([]events.Event, error) {
	var out []events.Event
	err := storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = s.AppendIn(ctx, tx, news...)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.Publish(out...)
	return out, nil
}

// AppendIn writes events inside the caller's transaction. The caller must call
// Publish after committing so subscribers never see uncommitted events.
func (s *Store) AppendIn(ctx context.Context, q storage.Querier, news ...events.New) ([]events.Event, error) {
	out := make([]events.Event, 0, len(news))
	for i := range news {
		n := &news[i]
		if err := validate(n); err != nil {
			return nil, err
		}
		payload, err := json.Marshal(n.Payload)
		if err != nil {
			return nil, fmt.Errorf("%w: payload: %v", ErrInvalid, err)
		}
		if n.Payload == nil {
			payload = []byte("{}")
		}
		if n.Evidence == nil {
			n.Evidence = []events.EvidenceRef{}
		}
		if n.Metadata == nil {
			n.Metadata = map[string]any{}
		}
		evidence, _ := json.Marshal(n.Evidence)
		metadata, _ := json.Marshal(n.Metadata)
		if n.ParentIDs == nil {
			n.ParentIDs = []ids.ID{}
		}
		ev := events.Event{ID: ids.New(), MindID: n.MindID, Type: n.Type, Category: events.CategoryOf(n.Type), Source: n.Source,
			OccurredAt: n.OccurredAt, ParentIDs: n.ParentIDs, CausationID: n.CausationID, CorrelationID: n.CorrelationID,
			Salience: n.Salience, Confidence: n.Confidence, Valence: n.Valence, Arousal: n.Arousal, Evidence: n.Evidence,
			Summary: n.Summary, Metadata: n.Metadata, Payload: payload}
		err = q.QueryRow(ctx, `INSERT INTO cognitive_events (id, mind_id, type, category, source, occurred_at, parent_ids, causation_id, correlation_id,
			salience, confidence, valence, arousal, evidence, summary, metadata, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING seq, recorded_at`,
			ev.ID, ev.MindID, string(ev.Type), string(ev.Category), ev.Source, ev.OccurredAt, uuidArray(ev.ParentIDs), nullID(ev.CausationID), nullID(ev.CorrelationID),
			ev.Salience, ev.Confidence, ev.Valence, ev.Arousal, evidence, ev.Summary, metadata, payload).Scan(&ev.Seq, &ev.RecordedAt)
		if err != nil {
			return nil, fmt.Errorf("append event: %w", err)
		}
		out = append(out, ev)
	}
	return out, nil
}

// Publish fans committed events out to live subscribers and metrics.
func (s *Store) Publish(evs ...events.Event) {
	if s.metrics != nil {
		for _, ev := range evs {
			s.metrics.Events.WithLabelValues(string(ev.Type)).Inc()
		}
	}
	s.bus.Publish(evs...)
}

func uuidArray(in []ids.ID) []string {
	out := make([]string, len(in))
	for i, id := range in {
		out[i] = string(id)
	}
	return out
}

func nullID(id ids.ID) *string {
	if id.IsZero() {
		return nil
	}
	s := string(id)
	return &s
}

// Filter selects events. Zero values mean "no constraint".
type Filter struct {
	MindID        ids.ID
	Types         []events.Type
	Categories    []events.Category
	Sources       []string
	MinSalience   float64
	MinConfidence float64
	MaxConfidence float64 // 0 = unset
	Since, Until  time.Time
	AfterSeq      int64 // exclusive lower bound (forward paging / cursors)
	BeforeSeq     int64 // exclusive upper bound (backward paging)
	CorrelationID ids.ID
	Search        string // substring of summary, case-insensitive
	Limit         int
	Ascending     bool
}

const cols = `id, seq, mind_id, type, category, source, occurred_at, recorded_at, parent_ids, causation_id, correlation_id, salience, confidence, valence, arousal, evidence, summary, metadata, payload`

// Query returns events matching f ordered by seq (descending unless Ascending).
func (s *Store) Query(ctx context.Context, f Filter) ([]events.Event, error) {
	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if !f.MindID.IsZero() {
		where = append(where, "mind_id = "+arg(f.MindID))
	}
	if len(f.Types) > 0 {
		ts := make([]string, len(f.Types))
		for i, t := range f.Types {
			ts[i] = string(t)
		}
		where = append(where, "type = ANY("+arg(ts)+")")
	}
	if len(f.Categories) > 0 {
		cs := make([]string, len(f.Categories))
		for i, c := range f.Categories {
			cs[i] = string(c)
		}
		where = append(where, "category = ANY("+arg(cs)+")")
	}
	if len(f.Sources) > 0 {
		where = append(where, "source = ANY("+arg(f.Sources)+")")
	}
	if f.MinSalience > 0 {
		where = append(where, "salience >= "+arg(f.MinSalience))
	}
	if f.MinConfidence > 0 {
		where = append(where, "confidence >= "+arg(f.MinConfidence))
	}
	if f.MaxConfidence > 0 {
		where = append(where, "confidence <= "+arg(f.MaxConfidence))
	}
	if !f.Since.IsZero() {
		where = append(where, "occurred_at >= "+arg(f.Since))
	}
	if !f.Until.IsZero() {
		where = append(where, "occurred_at <= "+arg(f.Until))
	}
	if f.AfterSeq > 0 {
		where = append(where, "seq > "+arg(f.AfterSeq))
	}
	if f.BeforeSeq > 0 {
		where = append(where, "seq < "+arg(f.BeforeSeq))
	}
	if !f.CorrelationID.IsZero() {
		where = append(where, "correlation_id = "+arg(f.CorrelationID))
	}
	if q := strings.TrimSpace(f.Search); q != "" {
		where = append(where, "summary ILIKE "+arg("%"+escapeLike(q)+"%"))
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	order := "DESC"
	if f.Ascending {
		order = "ASC"
	}
	sql := "SELECT " + cols + " FROM cognitive_events"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY seq " + order + " LIMIT " + arg(limit)
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		ev, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// Get fetches one event by id.
func (s *Store) Get(ctx context.Context, id ids.ID) (events.Event, error) {
	ev, err := scan(s.db.QueryRow(ctx, "SELECT "+cols+" FROM cognitive_events WHERE id=$1", id))
	return ev, storage.Translate(err)
}

// GetMany fetches events by id, preserving no particular order.
func (s *Store) GetMany(ctx context.Context, idList []ids.ID) ([]events.Event, error) {
	if len(idList) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, "SELECT "+cols+" FROM cognitive_events WHERE id = ANY($1) ORDER BY seq", uuidArray(idList))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		ev, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Children returns events directly caused by, or listing as parent, the given event.
func (s *Store) Children(ctx context.Context, id ids.ID, limit int) ([]events.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, "SELECT "+cols+" FROM cognitive_events WHERE causation_id=$1 OR $1 = ANY(parent_ids) ORDER BY seq LIMIT $2", id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		ev, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// DerivedFrom returns events that cite id as evidence.
func (s *Store) DerivedFrom(ctx context.Context, kind string, id string, limit int) ([]events.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ref, _ := json.Marshal([]map[string]string{{"kind": kind, "id": id}})
	rows, err := s.db.Query(ctx, "SELECT "+cols+" FROM cognitive_events WHERE evidence @> $1::jsonb ORDER BY seq LIMIT $2", ref, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		ev, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func scan(row pgx.Row) (events.Event, error) {
	var ev events.Event
	var typ, cat string
	var parents []string
	var causation, correlation *string
	var evidence, metadata []byte
	err := row.Scan(&ev.ID, &ev.Seq, &ev.MindID, &typ, &cat, &ev.Source, &ev.OccurredAt, &ev.RecordedAt, &parents, &causation, &correlation,
		&ev.Salience, &ev.Confidence, &ev.Valence, &ev.Arousal, &evidence, &ev.Summary, &metadata, &ev.Payload)
	if err != nil {
		return ev, err
	}
	ev.Type, ev.Category = events.Type(typ), events.Category(cat)
	ev.ParentIDs = make([]ids.ID, len(parents))
	for i, p := range parents {
		ev.ParentIDs[i] = ids.ID(p)
	}
	if causation != nil {
		ev.CausationID = ids.ID(*causation)
	}
	if correlation != nil {
		ev.CorrelationID = ids.ID(*correlation)
	}
	json.Unmarshal(evidence, &ev.Evidence) //nolint:errcheck
	json.Unmarshal(metadata, &ev.Metadata) //nolint:errcheck
	if ev.Evidence == nil {
		ev.Evidence = []events.EvidenceRef{}
	}
	if ev.Metadata == nil {
		ev.Metadata = map[string]any{}
	}
	return ev, nil
}

// Cursor returns the last sequence consumed by consumer for a mind.
func (s *Store) Cursor(ctx context.Context, q storage.Querier, mind ids.ID, consumer string) (int64, error) {
	var seq int64
	err := q.QueryRow(ctx, `SELECT last_seq FROM mind_cursors WHERE mind_id=$1 AND consumer=$2`, mind, consumer).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}

// SetCursor advances a consumer cursor (never backwards).
func (s *Store) SetCursor(ctx context.Context, q storage.Querier, mind ids.ID, consumer string, seq int64) error {
	_, err := q.Exec(ctx, `INSERT INTO mind_cursors (mind_id, consumer, last_seq) VALUES ($1,$2,$3)
		ON CONFLICT (mind_id, consumer) DO UPDATE SET last_seq = GREATEST(mind_cursors.last_seq, excluded.last_seq), updated_at = now()`, mind, consumer, seq)
	return err
}

// Stats summarises a mind's events for overview pages.
type Stats struct {
	Total      int64            `json:"total"`
	ByCategory map[string]int64 `json:"by_category"`
	LastSeq    int64            `json:"last_seq"`
	LastAt     *time.Time       `json:"last_at,omitempty"`
}

// Stats counts events for a mind (optionally since a time).
func (s *Store) Stats(ctx context.Context, mind ids.ID, since time.Time) (Stats, error) {
	st := Stats{ByCategory: map[string]int64{}}
	rows, err := s.db.Query(ctx, `SELECT category, count(*), max(seq), max(occurred_at) FROM cognitive_events WHERE mind_id=$1 AND ($2::timestamptz IS NULL OR occurred_at >= $2) GROUP BY category`, mind, nullTime(since))
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var n, maxSeq int64
		var at *time.Time
		if err := rows.Scan(&cat, &n, &maxSeq, &at); err != nil {
			return st, err
		}
		st.ByCategory[cat] = n
		st.Total += n
		if maxSeq > st.LastSeq {
			st.LastSeq, st.LastAt = maxSeq, at
		}
	}
	return st, rows.Err()
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
