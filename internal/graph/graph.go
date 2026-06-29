// Package graph maintains a mind's entity graph: people, systems, projects,
// documents, concepts and the typed, evidenced relationships between them
// (spec §24). Extraction elsewhere calls Upsert and Relate; operators can
// merge or separate entities and every such correction is recorded.
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/ids"
)

// Kind of entity.
type Kind string

// Kinds.
const (
	Person       Kind = "person"
	System       Kind = "system"
	Project      Kind = "project"
	Document     Kind = "document"
	Concept      Kind = "concept"
	Repository   Kind = "repository"
	Organisation Kind = "organisation"
	Place        Kind = "place"
	Other        Kind = "other"
)

// AllKinds lists kinds.
var AllKinds = []Kind{Person, System, Project, Document, Concept, Repository, Organisation, Place, Other}

// Valid reports whether k is known.
func (k Kind) Valid() bool {
	for _, x := range AllKinds {
		if x == k {
			return true
		}
	}
	return false
}

// Entity is a node.
type Entity struct {
	ID           ids.ID         `json:"id"`
	MindID       ids.ID         `json:"mind_id"`
	Kind         Kind           `json:"kind"`
	Name         string         `json:"name"`
	Canonical    string         `json:"canonical"`
	Aliases      []string       `json:"aliases"`
	Description  string         `json:"description"`
	Attributes   map[string]any `json:"attributes"`
	MentionCount int            `json:"mention_count"`
	FirstSeen    time.Time      `json:"first_seen"`
	LastSeen     time.Time      `json:"last_seen"`
	MergedInto   ids.ID         `json:"merged_into,omitempty"`
}

// Relationship is an edge.
type Relationship struct {
	ID         ids.ID            `json:"id"`
	FromEntity ids.ID            `json:"from_entity"`
	ToEntity   ids.ID            `json:"to_entity"`
	Relation   concepts.Relation `json:"relation"`
	Confidence float64           `json:"confidence"`
	Source     string            `json:"source"`
	EventID    ids.ID            `json:"event_id,omitempty"`
	Count      int               `json:"count"`
	FirstSeen  time.Time         `json:"first_seen"`
	LastSeen   time.Time         `json:"last_seen"`
}

// Link ties an entity to another object.
type Link struct {
	Kind      string    `json:"kind"`
	RefID     ids.ID    `json:"ref_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ErrInvalid marks bad input.
var ErrInvalid = errors.New("graph: invalid")

// Store persists the graph.
type Store struct{ db *pgxpool.Pool }

// NewStore creates a store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

var spaces = regexp.MustCompile(`\s+`)

// Canonical normalises a name for matching: lower-case, collapsed spaces,
// trailing punctuation removed.
func Canonical(name string) string {
	c := strings.ToLower(strings.TrimSpace(name))
	c = strings.Trim(c, ".,;:!?\"'()[]")
	return spaces.ReplaceAllString(c, " ")
}

// GuessKind classifies a mention deterministically. A model may override
// the kind later; this keeps the graph useful without one.
func GuessKind(name string) Kind {
	l := strings.ToLower(name)
	switch {
	case strings.HasSuffix(l, ".pdf"), strings.HasSuffix(l, ".md"), strings.HasSuffix(l, ".txt"), strings.HasSuffix(l, ".docx"):
		return Document
	case regexp.MustCompile(`^[a-z0-9-]+\.[a-z]{2,}(\.[a-z]{2,})*$`).MatchString(l), regexp.MustCompile(`^[a-z]+-?[0-9]{1,3}$`).MatchString(l), regexp.MustCompile(`^[a-z]+(-[a-z0-9]+)+$`).MatchString(l):
		return System
	case strings.Contains(l, "/") && !strings.Contains(l, " "):
		return Repository
	case len(strings.Fields(name)) == 1 && name != "" && name[0] >= 'A' && name[0] <= 'Z' && !strings.ContainsAny(name, "0123456789-."):
		return Person
	}
	return Concept
}

// Upsert finds an entity by canonical name or alias, or creates it, and
// bumps its mention count. Merged entities resolve to their survivor. A
// recorded "entities_different" correction between the exact names prevents
// alias matching from merging them.
func (s *Store) Upsert(ctx context.Context, q storage.Querier, mind ids.ID, kind Kind, name string, seen time.Time) (Entity, error) {
	name = strings.TrimSpace(name)
	canon := Canonical(name)
	if canon == "" || len(canon) > 200 {
		return Entity{}, fmt.Errorf("%w: name required (max 200)", ErrInvalid)
	}
	if !kind.Valid() {
		kind = GuessKind(name)
	}
	if seen.IsZero() {
		seen = time.Now()
	}
	e, err := s.find(ctx, q, mind, canon)
	if err == nil {
		_, err = q.Exec(ctx, `UPDATE entities SET mention_count=mention_count+1, last_seen=GREATEST(last_seen,$2) WHERE id=$1`, e.ID, seen)
		e.MentionCount++
		return e, err
	}
	if !errors.Is(err, storage.ErrNotFound) {
		return Entity{}, err
	}
	e = Entity{ID: ids.New(), MindID: mind, Kind: kind, Name: name, Canonical: canon, Aliases: []string{}, Attributes: map[string]any{}, MentionCount: 1, FirstSeen: seen, LastSeen: seen}
	_, err = q.Exec(ctx, `INSERT INTO entities (id, mind_id, kind, name, canonical, mention_count, first_seen, last_seen) VALUES ($1,$2,$3,$4,$5,1,$6,$6)
		ON CONFLICT (mind_id, canonical) WHERE merged_into IS NULL DO UPDATE SET mention_count=entities.mention_count+1, last_seen=GREATEST(entities.last_seen, excluded.last_seen)`, e.ID, mind, string(kind), name, canon, seen)
	if err != nil {
		return Entity{}, err
	}
	return s.find(ctx, q, mind, canon)
}

func (s *Store) find(ctx context.Context, q storage.Querier, mind ids.ID, canon string) (Entity, error) {
	e, err := scan(q.QueryRow(ctx, `SELECT `+cols+` FROM entities e WHERE e.mind_id=$1 AND e.merged_into IS NULL AND (e.canonical=$2 OR $2 = ANY(e.aliases)) ORDER BY (e.canonical=$2) DESC LIMIT 1`, mind, canon))
	if err != nil {
		return Entity{}, storage.Translate(err)
	}
	return e, nil
}

const cols = `e.id, e.mind_id, e.kind, e.name, e.canonical, e.aliases, e.description, e.attributes, e.mention_count, e.first_seen, e.last_seen, e.merged_into`

func scan(row pgx.Row) (Entity, error) {
	var e Entity
	var kind string
	var attrs []byte
	var merged *string
	if err := row.Scan(&e.ID, &e.MindID, &kind, &e.Name, &e.Canonical, &e.Aliases, &e.Description, &attrs, &e.MentionCount, &e.FirstSeen, &e.LastSeen, &merged); err != nil {
		return e, storage.Translate(err)
	}
	e.Kind = Kind(kind)
	json.Unmarshal(attrs, &e.Attributes) //nolint:errcheck
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	if merged != nil {
		e.MergedInto = ids.ID(*merged)
	}
	return e, nil
}

// Get fetches an entity, following merges.
func (s *Store) Get(ctx context.Context, mind, id ids.ID) (Entity, error) {
	for i := 0; i < 5; i++ {
		e, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM entities e WHERE e.id=$1 AND e.mind_id=$2`, id, mind))
		if err != nil {
			return Entity{}, err
		}
		if e.MergedInto.IsZero() {
			return e, nil
		}
		id = e.MergedInto
	}
	return Entity{}, storage.ErrNotFound
}

// Search lists entities by name prefix/substring and kind, most mentioned first.
func (s *Store) Search(ctx context.Context, mind ids.ID, query string, kind Kind, limit int) ([]Entity, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM entities e WHERE e.mind_id=$1 AND e.merged_into IS NULL AND ($2='' OR e.canonical LIKE '%'||$2||'%') AND ($3='' OR e.kind=$3) ORDER BY e.mention_count DESC, e.last_seen DESC LIMIT $4`, mind, Canonical(query), string(kind), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		e, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Relate adds or reinforces an edge. Repeats increase count and take the
// higher confidence; both endpoints must belong to the mind.
func (s *Store) Relate(ctx context.Context, q storage.Querier, mind, from, to ids.ID, rel concepts.Relation, confidence float64, source string, eventID ids.ID) (Relationship, error) {
	if !rel.Valid() || from == to || from.IsZero() || to.IsZero() {
		return Relationship{}, fmt.Errorf("%w: bad relation", ErrInvalid)
	}
	var ev *string
	if !eventID.IsZero() {
		e := string(eventID)
		ev = &e
	}
	r := Relationship{ID: ids.New(), FromEntity: from, ToEntity: to, Relation: rel, Confidence: clamp01(confidence), Source: source, EventID: eventID, Count: 1, FirstSeen: time.Now(), LastSeen: time.Now()}
	tag, err := q.Exec(ctx, `INSERT INTO entity_relationships (id, mind_id, from_entity, to_entity, relation, confidence, source, event_id)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8 WHERE (SELECT count(*) FROM entities WHERE id IN ($3,$4) AND mind_id=$2) = 2
		ON CONFLICT (from_entity, to_entity, relation) DO UPDATE SET count=entity_relationships.count+1, confidence=GREATEST(entity_relationships.confidence, excluded.confidence), last_seen=now()`,
		r.ID, mind, from, to, string(rel), r.Confidence, source, ev)
	if err != nil {
		return Relationship{}, err
	}
	if tag.RowsAffected() == 0 {
		return Relationship{}, fmt.Errorf("%w: entities not in mind", ErrInvalid)
	}
	return r, nil
}

// LinkRef records that an entity is involved in a memory, belief, event, goal or prediction.
func (s *Store) LinkRef(ctx context.Context, q storage.Querier, mind, entity ids.ID, kind string, ref ids.ID) error {
	_, err := q.Exec(ctx, `INSERT INTO entity_links (id, mind_id, entity_id, kind, ref_id) SELECT $1,$2,$3,$4,$5 WHERE EXISTS (SELECT 1 FROM entities WHERE id=$3 AND mind_id=$2) ON CONFLICT DO NOTHING`, ids.New(), mind, entity, kind, ref)
	return err
}

// Links returns an entity's links, newest first.
func (s *Store) Links(ctx context.Context, mind, entity ids.ID, limit int) ([]Link, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT kind, ref_id, created_at FROM entity_links WHERE mind_id=$1 AND entity_id=$2 ORDER BY created_at DESC LIMIT $3`, mind, entity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.Kind, &l.RefID, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Edge is a relationship with both entities loaded for rendering.
type Edge struct {
	Relationship
	From Entity `json:"from"`
	To   Entity `json:"to"`
}

// Neighbourhood returns the entity's edges with the other endpoints.
func (s *Store) Neighbourhood(ctx context.Context, mind, entity ids.ID, limit int) ([]Edge, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT r.id, r.from_entity, r.to_entity, r.relation, r.confidence, r.source, r.event_id, r.count, r.first_seen, r.last_seen FROM entity_relationships r
		WHERE r.mind_id=$1 AND (r.from_entity=$2 OR r.to_entity=$2) ORDER BY r.count DESC, r.confidence DESC LIMIT $3`, mind, entity, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rels []Relationship
	for rows.Next() {
		r, err := scanRel(rows)
		if err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	rows.Close()
	return s.hydrate(ctx, mind, rels)
}

func scanRel(row pgx.Row) (Relationship, error) {
	var r Relationship
	var rel string
	var ev *string
	if err := row.Scan(&r.ID, &r.FromEntity, &r.ToEntity, &rel, &r.Confidence, &r.Source, &ev, &r.Count, &r.FirstSeen, &r.LastSeen); err != nil {
		return r, err
	}
	r.Relation = concepts.Relation(rel)
	if ev != nil {
		r.EventID = ids.ID(*ev)
	}
	return r, nil
}

func (s *Store) hydrate(ctx context.Context, mind ids.ID, rels []Relationship) ([]Edge, error) {
	cache := map[ids.ID]Entity{}
	get := func(id ids.ID) (Entity, error) {
		if e, ok := cache[id]; ok {
			return e, nil
		}
		e, err := s.Get(ctx, mind, id)
		if err == nil {
			cache[id] = e
		}
		return e, err
	}
	var out []Edge
	for _, r := range rels {
		from, err := get(r.FromEntity)
		if err != nil {
			continue
		}
		to, err := get(r.ToEntity)
		if err != nil {
			continue
		}
		out = append(out, Edge{Relationship: r, From: from, To: to})
	}
	return out, nil
}

// Subgraph returns the most-connected entities (optionally around a focus
// entity or filtered by kind) and the edges among them, bounded by limit so
// the explorer never renders thousands of nodes.
type Subgraph struct {
	Nodes []Entity `json:"nodes"`
	Edges []Edge   `json:"edges"`
	Total int      `json:"total_entities"`
}

// Subgraph builds a bounded view.
func (s *Store) Subgraph(ctx context.Context, mind ids.ID, focus ids.ID, kind Kind, limit int) (Subgraph, error) {
	if limit <= 0 || limit > 300 {
		limit = 80
	}
	var sg Subgraph
	s.db.QueryRow(ctx, `SELECT count(*) FROM entities WHERE mind_id=$1 AND merged_into IS NULL`, mind).Scan(&sg.Total) //nolint:errcheck
	var nodes []Entity
	if !focus.IsZero() {
		f, err := s.Get(ctx, mind, focus)
		if err != nil {
			return sg, err
		}
		nodes = append(nodes, f)
		edges, err := s.Neighbourhood(ctx, mind, f.ID, limit)
		if err != nil {
			return sg, err
		}
		seen := map[ids.ID]bool{f.ID: true}
		for _, e := range edges {
			for _, n := range []Entity{e.From, e.To} {
				if !seen[n.ID] && (kind == "" || n.Kind == kind || n.ID == f.ID) {
					seen[n.ID] = true
					nodes = append(nodes, n)
				}
			}
		}
	} else {
		var err error
		nodes, err = s.Search(ctx, mind, "", kind, limit)
		if err != nil {
			return sg, err
		}
	}
	idList := make([]string, 0, len(nodes))
	for _, n := range nodes {
		idList = append(idList, string(n.ID))
	}
	sg.Nodes = nodes
	if len(idList) == 0 {
		sg.Edges = []Edge{}
		return sg, nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.id, r.from_entity, r.to_entity, r.relation, r.confidence, r.source, r.event_id, r.count, r.first_seen, r.last_seen FROM entity_relationships r
		WHERE r.mind_id=$1 AND r.from_entity = ANY($2) AND r.to_entity = ANY($2) ORDER BY r.count DESC LIMIT 1000`, mind, idList)
	if err != nil {
		return sg, err
	}
	defer rows.Close()
	var rels []Relationship
	for rows.Next() {
		r, err := scanRel(rows)
		if err != nil {
			return sg, err
		}
		rels = append(rels, r)
	}
	sg.Edges, err = s.hydrate(ctx, mind, rels)
	if sg.Edges == nil {
		sg.Edges = []Edge{}
	}
	return sg, err
}

// Merge folds loser into survivor: relationships and links are re-pointed,
// the loser's name becomes an alias, and the loser row stays with
// merged_into set so old references still resolve.
func (s *Store) Merge(ctx context.Context, mind, survivor, loser ids.ID) error {
	if survivor == loser {
		return fmt.Errorf("%w: same entity", ErrInvalid)
	}
	return storage.Tx(ctx, s.db, func(tx pgx.Tx) error {
		sv, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM entities e WHERE e.id=$1 AND e.mind_id=$2 AND e.merged_into IS NULL FOR UPDATE`, survivor, mind))
		if err != nil {
			return err
		}
		lo, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM entities e WHERE e.id=$1 AND e.mind_id=$2 AND e.merged_into IS NULL FOR UPDATE`, loser, mind))
		if err != nil {
			return err
		}
		// Re-point edges; drop those that would become self-loops or duplicates.
		if _, err := tx.Exec(ctx, `DELETE FROM entity_relationships WHERE mind_id=$1 AND ((from_entity=$2 AND to_entity=$3) OR (from_entity=$3 AND to_entity=$2))`, mind, survivor, loser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entity_relationships r SET from_entity=$1 WHERE r.from_entity=$2 AND NOT EXISTS (SELECT 1 FROM entity_relationships x WHERE x.from_entity=$1 AND x.to_entity=r.to_entity AND x.relation=r.relation)`, survivor, loser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entity_relationships r SET to_entity=$1 WHERE r.to_entity=$2 AND NOT EXISTS (SELECT 1 FROM entity_relationships x WHERE x.to_entity=$1 AND x.from_entity=r.from_entity AND x.relation=r.relation)`, survivor, loser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM entity_relationships WHERE from_entity=$1 OR to_entity=$1`, loser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entity_links l SET entity_id=$1 WHERE l.entity_id=$2 AND NOT EXISTS (SELECT 1 FROM entity_links x WHERE x.entity_id=$1 AND x.kind=l.kind AND x.ref_id=l.ref_id)`, survivor, loser); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM entity_links WHERE entity_id=$1`, loser); err != nil {
			return err
		}
		aliases := append(append([]string{}, sv.Aliases...), lo.Canonical)
		aliases = append(aliases, lo.Aliases...)
		if _, err := tx.Exec(ctx, `UPDATE entities SET aliases=$2, mention_count=mention_count+$3, first_seen=LEAST(first_seen,$4), last_seen=GREATEST(last_seen,$5) WHERE id=$1`, survivor, uniq(aliases), lo.MentionCount, lo.FirstSeen, lo.LastSeen); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE entities SET merged_into=$2 WHERE id=$1`, loser, survivor)
		return err
	})
}

// SetKind and description let operators correct classification.
func (s *Store) Update(ctx context.Context, mind, id ids.ID, kind Kind, description string) error {
	if !kind.Valid() {
		return fmt.Errorf("%w: unknown kind", ErrInvalid)
	}
	tag, err := s.db.Exec(ctx, `UPDATE entities SET kind=$3, description=$4 WHERE id=$1 AND mind_id=$2`, id, mind, string(kind), description)
	if err == nil && tag.RowsAffected() == 0 {
		return storage.ErrNotFound
	}
	return err
}

// Stats counts entities by kind and edges.
type Stats struct {
	Entities int64            `json:"entities"`
	Edges    int64            `json:"edges"`
	ByKind   map[string]int64 `json:"by_kind"`
}

// Stats returns counts.
func (s *Store) Stats(ctx context.Context, mind ids.ID) (Stats, error) {
	st := Stats{ByKind: map[string]int64{}}
	rows, err := s.db.Query(ctx, `SELECT kind, count(*) FROM entities WHERE mind_id=$1 AND merged_into IS NULL GROUP BY kind`, mind)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.ByKind[k] = n
		st.Entities += n
	}
	rows.Close()
	err = s.db.QueryRow(ctx, `SELECT count(*) FROM entity_relationships WHERE mind_id=$1`, mind).Scan(&st.Edges)
	return st, err
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
