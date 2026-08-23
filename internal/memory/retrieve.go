package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/embeddings"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Retrieval is one recalled memory with the arithmetic behind its score.
type Retrieval struct {
	Memory     Memory             `json:"memory"`
	Score      float64            `json:"score"`
	Components map[string]float64 `json:"components"` // text, vector, recency, activation, importance, credibility
}

// Query describes what to recall.
type Query struct {
	Text     string
	Kinds    []concepts.MemoryKind
	Limit    int
	Now      time.Time
	Semantic bool // use embeddings when available
	Strict   bool // websearch syntax with AND semantics; default matches any term
}

// Weights for fusion. When vectors are unavailable the vector weight is
// redistributed to text so totals stay comparable.
var weights = map[string]float64{"text": 0.40, "vector": 0.25, "recency": 0.10, "activation": 0.10, "importance": 0.10, "credibility": 0.05}

// Retrieve performs fused retrieval: full-text rank, optional vector
// similarity, recency, activation, importance and credibility, each recorded.
func (s *Store) Retrieve(ctx context.Context, mind ids.ID, q Query) ([]Retrieval, error) {
	if q.Limit <= 0 || q.Limit > 50 {
		q.Limit = 10
	}
	if q.Now.IsZero() {
		q.Now = time.Now()
	}
	text := strings.TrimSpace(q.Text)
	cands := map[ids.ID]*Retrieval{}
	add := func(m Memory) *Retrieval {
		r, ok := cands[m.ID]
		if !ok {
			r = &Retrieval{Memory: m, Components: map[string]float64{}}
			cands[m.ID] = r
		}
		return r
	}
	kindFilter := "TRUE"
	args := []any{mind}
	if len(q.Kinds) > 0 {
		kindFilter = "m.kind = ANY($2)"
		args = append(args, strs(q.Kinds))
	}
	// Full-text candidates.
	if text != "" {
		tsq := "websearch_to_tsquery('english', $%d)"
		queryText := text
		if !q.Strict {
			tsq = "to_tsquery('english', $%d)"
			queryText = anyTermsQuery(text)
		}
		targs := append(append([]any{}, args...), queryText)
		tsq = strings.ReplaceAll(tsq, "%d", itoa(len(targs)))
		rows, err := s.db.Query(ctx, `SELECT `+cols+`, ts_rank_cd(m.search, `+tsq+`) AS rank
			FROM memories m WHERE m.mind_id=$1 AND m.status='active' AND `+kindFilter+` AND m.search @@ `+tsq+`
			ORDER BY rank DESC LIMIT 50`, targs...)
		if err != nil {
			return nil, err
		}
		var maxRank float64
		type ranked struct {
			m    Memory
			rank float64
		}
		var list []ranked
		for rows.Next() {
			var m Memory
			var rank float64
			var kind, epi, status, trust string
			var detail []byte
			var origin, supersedes *string
			if err := rows.Scan(&m.ID, &m.MindID, &kind, &epi, &status, &m.Content, &detail, &m.Source, &trust, &m.Credibility, &m.Confidence, &m.Importance, &m.Salience, &m.Activation,
				&m.Valence, &m.Arousal, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt, &m.RetrievalCount, &m.LastRetrievedAt, &origin, &supersedes, &m.RedactedAt, &rank); err != nil {
				rows.Close()
				return nil, err
			}
			m.Kind, m.Epistemic, m.Status, m.Trust = concepts.MemoryKind(kind), concepts.Epistemic(epi), Status(status), events.Trust(trust)
			if origin != nil {
				m.OriginEvent = ids.ID(*origin)
			}
			list = append(list, ranked{m, rank})
			if rank > maxRank {
				maxRank = rank
			}
		}
		rows.Close()
		for _, r := range list {
			c := add(r.m)
			if maxRank > 0 {
				c.Components["text"] = round4(r.rank / maxRank)
			}
		}
	}
	// Vector candidates.
	vectorUsed := false
	if text != "" && q.Semantic && s.SemanticEnabled() {
		if vecs, err := s.embedder.Embed(ctx, []string{text}); err == nil && len(vecs) == 1 {
			vargs := append(append([]any{}, args...), s.embedder.Model(), embeddings.Format(vecs[0]))
			n := len(vargs)
			rows, err := s.db.Query(ctx, `SELECT `+cols+`, 1 - (e.embedding <=> $`+itoa(n)+`::vector) AS sim
				FROM memories m JOIN memory_embeddings e ON e.memory_id = m.id AND e.model = $`+itoa(n-1)+`
				WHERE m.mind_id=$1 AND m.status='active' AND `+kindFilter+` ORDER BY e.embedding <=> $`+itoa(n)+`::vector LIMIT 50`, vargs...)
			if err == nil {
				vectorUsed = true
				for rows.Next() {
					var m Memory
					var sim float64
					var kind, epi, status, trust string
					var detail []byte
					var origin, supersedes *string
					if err := rows.Scan(&m.ID, &m.MindID, &kind, &epi, &status, &m.Content, &detail, &m.Source, &trust, &m.Credibility, &m.Confidence, &m.Importance, &m.Salience, &m.Activation,
						&m.Valence, &m.Arousal, &m.OccurredAt, &m.CreatedAt, &m.UpdatedAt, &m.RetrievalCount, &m.LastRetrievedAt, &origin, &supersedes, &m.RedactedAt, &sim); err != nil {
						break
					}
					m.Kind, m.Epistemic, m.Status, m.Trust = concepts.MemoryKind(kind), concepts.Epistemic(epi), Status(status), events.Trust(trust)
					if origin != nil {
						m.OriginEvent = ids.ID(*origin)
					}
					c := add(m)
					c.Components["vector"] = round4(math.Max(0, sim))
				}
				rows.Close()
			}
		}
	}
	// No text: recall by importance/activation only (e.g. idle association).
	if text == "" {
		list, err := s.List(ctx, mind, Filter{Kinds: q.Kinds, Limit: 50})
		if err != nil {
			return nil, err
		}
		for _, m := range list {
			add(m)
		}
	}
	// Fuse.
	w := map[string]float64{}
	for k, v := range weights {
		w[k] = v
	}
	if !vectorUsed {
		w["text"] += w["vector"]
		w["vector"] = 0
	}
	if text == "" {
		w["text"], w["vector"] = 0, 0
	}
	var sum float64
	for _, v := range w {
		sum += v
	}
	out := make([]Retrieval, 0, len(cands))
	for _, r := range cands {
		m := r.Memory
		age := q.Now.Sub(m.OccurredAt).Hours() / 24
		r.Components["recency"] = round4(math.Exp(-age / 30))
		r.Components["activation"] = round4(m.Activation)
		r.Components["importance"] = round4(m.Importance)
		r.Components["credibility"] = round4(m.Credibility)
		var total float64
		for k, wk := range w {
			total += r.Components[k] * wk
		}
		r.Score = round4(total / sum)
		out = append(out, *r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Memory.ID < out[j].Memory.ID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// anyTermsQuery builds an OR tsquery from the alphanumeric words of text so
// recall favours breadth; ranking then orders by how many terms matched.
func anyTermsQuery(text string) string {
	var terms []string
	seen := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		w := cur.String()
		cur.Reset()
		if len(w) >= 2 && !seen[w] {
			seen[w] = true
			terms = append(terms, w)
		}
	}
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	if len(terms) > 32 {
		terms = terms[:32]
	}
	if len(terms) == 0 {
		return "zzzznomatch"
	}
	return strings.Join(terms, " | ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
