// Package dreaming implements the optional idle mode "dreaming" (spec §19):
// replay a deterministic sample of memories, find pairs that share vocabulary
// but were never linked, and propose speculative low-confidence hypotheses.
//
// Invariants: dreams run under a forbidden capability context so no action
// can ever be executed from a dream; every hypothesis is an ordinary belief
// with prior 0.25 and weak evidence, so it starts low and goes through the
// same evidence and review mechanisms as everything else; originals are never
// modified.
package dreaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/capabilities"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Dream is one dreaming session.
type Dream struct {
	ID           ids.ID          `json:"id"`
	MindID       ids.ID          `json:"mind_id"`
	Status       string          `json:"status"`
	Trigger      string          `json:"trigger"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
	Sampled      int             `json:"sampled"`
	Associations int             `json:"associations"`
	Hypotheses   int             `json:"hypotheses"`
	LLMCalls     int             `json:"llm_calls"`
	Inputs       json.RawMessage `json:"inputs"`
	Error        string          `json:"error"`
	CreatedBy    string          `json:"created_by"`
	CreatedAt    time.Time       `json:"created_at"`
}

// Hypothesis is a speculative belief proposed by a dream, awaiting review.
type Hypothesis struct {
	ID          ids.ID     `json:"id"`
	DreamID     ids.ID     `json:"dream_id"`
	MindID      ids.ID     `json:"mind_id"`
	BeliefID    ids.ID     `json:"belief_id"`
	MemoryIDs   []ids.ID   `json:"memory_ids"`
	Proposition string     `json:"proposition"`
	Confidence  float64    `json:"confidence"`
	Basis       string     `json:"basis"`
	Wording     string     `json:"wording"`
	Status      string     `json:"status"`
	ReviewedBy  string     `json:"reviewed_by"`
	ReviewNote  string     `json:"review_note"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Pair is a candidate association between two sampled memories.
type Pair struct {
	A, B   memory.Memory
	Shared []string
	Score  float64
}

// Service runs dreams.
type Service struct {
	DB      *pgxpool.Pool
	Memory  *memory.Store
	Beliefs *beliefs.Store
	Events  *evstore.Store
	LLM     *router.Router
	Prompts *prompts.Store
	Log     *slog.Logger
	// Volition, when set, lets a dream leave a want to test what it
	// proposed. Dreams may want; they never intend, plan or act.
	Volition *volition.Store
}

// ErrReviewed is returned when a hypothesis has already been promoted or rejected.
var ErrReviewed = errors.New("hypothesis already reviewed")

var paused = false

// SetPaused toggles the global kill switch. IsPaused reports it.
func SetPaused(p bool) { paused = p }

// IsPaused reports the global kill switch.
func IsPaused() bool { return paused }

// InWindow reports whether now falls inside an "HH-HH" local-hour window.
// Empty means always; a window may wrap midnight ("22-06").
func InWindow(hours string, now time.Time) bool {
	hours = strings.TrimSpace(hours)
	if hours == "" {
		return true
	}
	a, b, ok := strings.Cut(hours, "-")
	from, err1 := strconv.Atoi(strings.TrimSpace(a))
	to, err2 := strconv.Atoi(strings.TrimSpace(b))
	if !ok || err1 != nil || err2 != nil || from < 0 || from > 23 || to < 0 || to > 24 {
		return false // unparseable window: never dream rather than always
	}
	h := now.Hour()
	if from == to {
		return true
	}
	if from < to {
		return h >= from && h < to
	}
	return h >= from || h < to
}

// Sample picks up to n memories deterministically for a given seed: the
// score mixes importance with a hash so the same dream id always replays the
// same memories, while different dreams wander.
func Sample(seed ids.ID, mems []memory.Memory, n int) []memory.Memory {
	type scored struct {
		m memory.Memory
		s float64
	}
	items := make([]scored, 0, len(mems))
	for _, m := range mems {
		h := fnv.New32a()
		h.Write([]byte(seed))
		h.Write([]byte(m.ID))
		jitter := float64(h.Sum32()%1000) / 1000
		items = append(items, scored{m, 0.5*m.Importance + 0.5*jitter})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].s != items[j].s {
			return items[i].s > items[j].s
		}
		return items[i].m.ID < items[j].m.ID
	})
	if n > len(items) {
		n = len(items)
	}
	out := make([]memory.Memory, 0, n)
	for _, it := range items[:n] {
		out = append(out, it.m)
	}
	return out
}

// Associate finds pairs that share vocabulary (Jaccard within [min, 0.8)) but
// are not near-duplicates, come from different moments, and are not already
// related. Deterministic and sorted by score.
func Associate(mems []memory.Memory, related func(a, b ids.ID) bool, min float64) []Pair {
	terms := make([][]string, len(mems))
	for i, m := range mems {
		terms[i] = workspace.Tokenise(m.Content)
	}
	var out []Pair
	for i := range mems {
		for j := i + 1; j < len(mems); j++ {
			a, b := mems[i], mems[j]
			if a.OccurredAt.Sub(b.OccurredAt).Abs() < time.Hour && a.Source == b.Source {
				continue // same episode, consolidation's job
			}
			jac := beliefs.Jaccard(terms[i], terms[j])
			if jac < min || jac >= 0.8 {
				continue
			}
			if related != nil && related(a.ID, b.ID) {
				continue
			}
			shared := intersect(terms[i], terms[j])
			if len(shared) == 0 {
				continue
			}
			// Distance in time and difference of source make a link more "hidden", hence more interesting.
			score := jac
			if a.Source != b.Source {
				score += 0.15
			}
			if a.OccurredAt.Sub(b.OccurredAt).Abs() > 24*time.Hour {
				score += 0.1
			}
			out = append(out, Pair{A: a, B: b, Shared: shared, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].A.ID+out[i].B.ID < out[j].A.ID+out[j].B.ID
	})
	return out
}

// stop lists function words that make a shared term meaningless.
var stop = map[string]bool{"the": true, "and": true, "for": true, "not": true, "with": true, "that": true, "this": true, "from": true, "said": true, "may": true, "are": true, "was": true, "has": true, "have": true, "but": true, "you": true, "its": true, "were": true, "been": true, "into": true, "than": true, "then": true, "also": true, "about": true, "definitely": true}

func intersect(a, b []string) []string {
	set := map[string]bool{}
	for _, t := range a {
		if len(t) >= 3 && !stop[t] {
			set[t] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range b {
		if set[t] && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// Proposition is the deterministic wording for a pair.
func Proposition(p Pair) string {
	return fmt.Sprintf("%q and %q may be connected through %s", excerpt(p.A.Content, 70), excerpt(p.B.Content, 70), strings.Join(p.Shared, ", "))
}

// Run performs one dream. The context is marked forbidden for capabilities
// before anything else happens, so nothing reachable from here can act.
func (s *Service) Run(ctx context.Context, m minds.Mind, trigger, by string) (Dream, error) {
	ctx = capabilities.WithForbidden(ctx, "dreaming")
	d := Dream{ID: ids.New(), MindID: m.ID, Status: "running", Trigger: trigger, CreatedBy: by, CreatedAt: time.Now()}
	now := time.Now()
	d.StartedAt = &now
	if _, err := s.DB.Exec(ctx, `INSERT INTO dreams (id, mind_id, status, trigger, started_at, created_by) VALUES ($1,$2,'running',$3,now(),$4)`, d.ID, m.ID, trigger, by); err != nil {
		return d, err
	}
	finish := func(status, errText string, inputs []ids.ID) (Dream, error) {
		in, _ := json.Marshal(inputs)
		if inputs == nil {
			in = []byte("[]")
		}
		_, err := s.DB.Exec(ctx, `UPDATE dreams SET status=$2, error=$3, sampled=$4, associations=$5, hypotheses=$6, llm_calls=$7, inputs=$8, finished_at=now() WHERE id=$1`, d.ID, status, errText, d.Sampled, d.Associations, d.Hypotheses, d.LLMCalls, in)
		d.Status, d.Error, d.Inputs = status, errText, in
		return d, err
	}
	if paused && trigger == "schedule" {
		return finish("rejected", "dreaming is paused", nil)
	}
	if trigger == "schedule" && !InWindow(m.Config.DreamHours, now) {
		return finish("rejected", "outside allowed hours "+m.Config.DreamHours, nil)
	}
	mems, err := s.Memory.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Episodic, concepts.Semantic}, Since: now.Add(-90 * 24 * time.Hour), Limit: 400})
	if err != nil {
		return finish("failed", err.Error(), nil)
	}
	sample := Sample(d.ID, mems, m.Config.DreamMaxMemories)
	d.Sampled = len(sample)
	var inputs []ids.ID
	for _, sm := range sample {
		inputs = append(inputs, sm.ID)
	}
	related := func(a, b ids.ID) bool {
		var n int
		s.DB.QueryRow(ctx, `SELECT count(*) FROM memory_relationships WHERE (from_memory=$1 AND to_memory=$2) OR (from_memory=$2 AND to_memory=$1)`, a, b).Scan(&n) //nolint:errcheck
		return n > 0
	}
	pairs := Associate(sample, related, 0.12)
	if len(pairs) > 10 {
		pairs = pairs[:10]
	}
	for _, p := range pairs {
		if err := s.associate(ctx, m, d.ID, p); err != nil {
			s.Log.Warn("dream association failed", "mind", m.Slug, "err", err)
			continue
		}
		d.Associations++
	}
	maxH := m.Config.DreamMaxHypotheses
	if maxH > len(pairs) {
		maxH = len(pairs)
	}
	props := s.wordings(ctx, m, &d, pairs[:maxH])
	for i, p := range pairs[:maxH] {
		w := props[i]
		if w.Proposition == "" {
			continue
		}
		if err := s.hypothesise(ctx, m, d.ID, p, w); err != nil {
			s.Log.Warn("dream hypothesis failed", "mind", m.Slug, "err", err)
			continue
		}
		d.Hypotheses++
	}
	return finish("done", "", inputs)
}

type wording struct {
	Proposition, Rationale, Wording string
	CallID                          ids.ID
}

// wordings returns one wording per pair: deterministic by default, replaced
// by the model's when a budgeted call succeeds and refers to a valid pair.
func (s *Service) wordings(ctx context.Context, m minds.Mind, d *Dream, pairs []Pair) []wording {
	out := make([]wording, len(pairs))
	for i, p := range pairs {
		out[i] = wording{Proposition: Proposition(p), Rationale: "shared vocabulary: " + strings.Join(p.Shared, ", "), Wording: "deterministic"}
	}
	if len(pairs) == 0 || s.LLM == nil || s.Prompts == nil || d.LLMCalls >= m.Config.DreamLLMCalls || !s.LLM.Available(ctx, llm.RoleHypothesis) {
		return out
	}
	pr, err := s.Prompts.Get(ctx, "dream")
	if err != nil {
		return out
	}
	type pv struct {
		Index                      int
		Shared, A, B, ADate, BDate string
		ASource, BSource           string
	}
	var view []pv
	for i, p := range pairs {
		view = append(view, pv{Index: i, Shared: strings.Join(p.Shared, ", "), A: excerpt(p.A.Content, 300), B: excerpt(p.B.Content, 300), ADate: p.A.OccurredAt.Format("2006-01-02"), BDate: p.B.OccurredAt.Format("2006-01-02"), ASource: p.A.Source, BSource: p.B.Source})
	}
	req, err := pr.Render(map[string]any{"Pairs": view, "Max": len(pairs)})
	if err != nil {
		return out
	}
	req.MindID, req.Purpose = m.ID, "dream "+d.ID.String()
	d.LLMCalls++
	resp, err := s.LLM.Complete(ctx, req)
	if err != nil || len(resp.JSON) == 0 {
		return out
	}
	var parsed struct {
		Hypotheses []struct {
			Pair        int    `json:"pair"`
			Proposition string `json:"proposition"`
			Rationale   string `json:"rationale"`
		} `json:"hypotheses"`
	}
	if json.Unmarshal(resp.JSON, &parsed) != nil {
		return out
	}
	for _, h := range parsed.Hypotheses {
		p := strings.TrimSpace(h.Proposition)
		if h.Pair < 0 || h.Pair >= len(pairs) || len(p) < 8 || len(p) > 500 {
			continue
		}
		out[h.Pair] = wording{Proposition: p, Rationale: excerpt(h.Rationale, 300), Wording: "model", CallID: resp.CallID}
	}
	return out
}

func (s *Service) associate(ctx context.Context, m minds.Mind, dream ids.ID, p Pair) error {
	return storage.Tx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := s.Memory.Relate(ctx, tx, m.ID, p.A.ID, p.B.ID, concepts.SimilarTo, 0.3*p.Score, "dreaming"); err != nil {
			return err
		}
		evs, err := s.Events.AppendIn(ctx, tx, events.New{MindID: m.ID, Type: events.DreamAssociation, Source: "dreaming", Summary: "dream linked: " + excerpt(p.A.Content, 60) + " ↔ " + excerpt(p.B.Content, 60),
			Salience: 0.15, Confidence: 0.3 * p.Score, Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(p.A.ID)}, {Kind: "memory", ID: string(p.B.ID)}},
			Metadata: map[string]any{"dream_id": dream, "shared": strings.Join(p.Shared, " "), "score": p.Score}, Payload: map[string]any{"a": p.A.ID, "b": p.B.ID, "shared": p.Shared, "score": p.Score}})
		if err != nil {
			return err
		}
		s.Events.Publish(evs...)
		return nil
	})
}

func (s *Service) hypothesise(ctx context.Context, m minds.Mind, dream ids.ID, p Pair, w wording) error {
	return storage.Tx(ctx, s.DB, func(tx pgx.Tx) error {
		emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
			out, err := s.Events.AppendIn(ctx, q, n)
			if err != nil {
				return events.Event{}, err
			}
			return out[0], nil
		}
		ev := []beliefs.EvidenceInput{
			{Direction: "for", Kind: "memory", RefID: string(p.A.ID), Source: "dreaming/" + p.A.Source, Weight: 0.1, Credibility: p.A.Credibility, Note: "dream replay: " + excerpt(p.A.Content, 80),
				Derivation: beliefs.DerivationDream, Family: s.familyOf(ctx, p.A), RootIDs: rootsOf(p.A)},
			{Direction: "for", Kind: "memory", RefID: string(p.B.ID), Source: "dreaming/" + p.B.Source, Weight: 0.1, Credibility: p.B.Credibility, Note: "dream replay: " + excerpt(p.B.Content, 80),
				Derivation: beliefs.DerivationDream, Family: s.familyOf(ctx, p.B), RootIDs: rootsOf(p.B)},
		}
		detail := map[string]any{"dream_id": dream, "wording": w.Wording, "rationale": w.Rationale, "shared": p.Shared}
		if !w.CallID.IsZero() {
			detail["llm_call_id"] = w.CallID
		}
		res, err := s.Beliefs.Create(ctx, tx, emit, beliefs.New{MindID: m.ID, Proposition: w.Proposition, Subject: "dream:" + strings.Join(p.Shared, "+"), Epistemic: concepts.Hypothesis, Source: "dreaming",
			Evidence: ev, Alternatives: []string{"the two memories are unrelated; the shared vocabulary is coincidence"}, Detail: detail})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO dream_hypotheses (id, dream_id, mind_id, belief_id, memory_ids, proposition, confidence, basis, wording) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			ids.New(), dream, m.ID, res.Belief.ID, []ids.ID{p.A.ID, p.B.ID}, w.Proposition, res.Belief.Confidence, w.Rationale, w.Wording); err != nil {
			return err
		}
		published := append([]events.Event{}, res.Events...)
		if s.Volition != nil {
			rec := volition.Recorder{Q: tx, Source: "dreaming", Emit: func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
				ev, err := emit(ctx, q, n)
				published = append(published, ev)
				return ev, err
			}}
			if _, _, err := s.Volition.UpsertWant(ctx, rec, m.ID, volition.Candidate{Subject: "test:belief:" + string(res.Belief.ID), Description: "find out whether " + excerpt(w.Proposition, 200) + " (proposed in a dream)",
				Origin: volition.OriginDream, OriginRef: "belief:" + string(res.Belief.ID), Strength: 0.4, Urgency: 0.1, Values: []string{"curiosity"}, Drives: []string{"curiosity"},
				Proposal: &volition.Proposal{Kind: "investigate", Subject: w.Proposition}, Terms: map[string]float64{"base": 0.4}}); err != nil {
				return err
			}
		}
		s.Events.Publish(published...)
		return nil
	})
}

// Dreams lists recent dreams for a mind.
func (s *Service) Dreams(ctx context.Context, mind ids.ID, limit int) ([]Dream, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, status, trigger, started_at, finished_at, sampled, associations, hypotheses, llm_calls, inputs, error, created_by, created_at FROM dreams WHERE mind_id=$1 ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Dream
	for rows.Next() {
		var d Dream
		if err := rows.Scan(&d.ID, &d.MindID, &d.Status, &d.Trigger, &d.StartedAt, &d.FinishedAt, &d.Sampled, &d.Associations, &d.Hypotheses, &d.LLMCalls, &d.Inputs, &d.Error, &d.CreatedBy, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Hypotheses lists dream hypotheses, optionally filtered by status.
func (s *Service) Hypotheses(ctx context.Context, mind ids.ID, status string, limit int) ([]Hypothesis, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.Query(ctx, `SELECT id, dream_id, mind_id, belief_id, memory_ids, proposition, confidence, basis, wording, status, reviewed_by, review_note, reviewed_at, created_at FROM dream_hypotheses WHERE mind_id=$1 AND ($2='' OR status=$2) ORDER BY created_at DESC LIMIT $3`, mind, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hypothesis
	for rows.Next() {
		var h Hypothesis
		if err := rows.Scan(&h.ID, &h.DreamID, &h.MindID, &h.BeliefID, &h.MemoryIDs, &h.Proposition, &h.Confidence, &h.Basis, &h.Wording, &h.Status, &h.ReviewedBy, &h.ReviewNote, &h.ReviewedAt, &h.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Service) hypothesis(ctx context.Context, mind, id ids.ID) (Hypothesis, error) {
	var h Hypothesis
	err := s.DB.QueryRow(ctx, `SELECT id, dream_id, mind_id, belief_id, memory_ids, proposition, confidence, basis, wording, status, reviewed_by, review_note, reviewed_at, created_at FROM dream_hypotheses WHERE mind_id=$1 AND id=$2`, mind, id).
		Scan(&h.ID, &h.DreamID, &h.MindID, &h.BeliefID, &h.MemoryIDs, &h.Proposition, &h.Confidence, &h.Basis, &h.Wording, &h.Status, &h.ReviewedBy, &h.ReviewNote, &h.ReviewedAt, &h.CreatedAt)
	return h, err
}

func (s *Service) emitter() beliefs.Emitter {
	return func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
		out, err := s.Events.AppendIn(ctx, q, n)
		if err != nil {
			return events.Event{}, err
		}
		s.Events.Publish(out...)
		return out[0], nil
	}
}

// Promote records operator endorsement as evidence on the hypothesis's
// belief. It does not set confidence directly: the endorsement is one more
// weighted piece of evidence and the arithmetic stays inspectable.
func (s *Service) Promote(ctx context.Context, m minds.Mind, id ids.ID, note, by string) (Hypothesis, error) {
	h, err := s.hypothesis(ctx, m.ID, id)
	if err != nil {
		return h, err
	}
	if h.Status != "proposed" {
		return h, fmt.Errorf("%w: %s", ErrReviewed, h.Status)
	}
	res, err := s.Beliefs.AddEvidence(ctx, s.DB, s.emitter(), m.ID, h.BeliefID, beliefs.EvidenceInput{Direction: "for", Kind: "operator", RefID: by, Source: "operator", Weight: 0.6, Credibility: 1, Note: "promoted from dream: " + note}, "operator promoted dream hypothesis", ids.Nil, ids.Nil)
	if err != nil {
		return h, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE dream_hypotheses SET status='promoted', reviewed_by=$2, review_note=$3, reviewed_at=now(), confidence=$4 WHERE id=$1`, id, by, note, res.Belief.Confidence); err != nil {
		return h, err
	}
	s.Events.OperatorAction(ctx, m.ID, by, "dream.promote", "Operator "+by+" promoted a dream hypothesis: "+excerpt(h.Proposition, 100), map[string]any{"hypothesis_id": id, "belief_id": h.BeliefID, "note": note, "confidence": res.Belief.Confidence})
	h.Status, h.ReviewedBy, h.ReviewNote, h.Confidence = "promoted", by, note, res.Belief.Confidence
	return h, nil
}

// Reject retracts the hypothesis's belief. Memories and associations stay.
func (s *Service) Reject(ctx context.Context, m minds.Mind, id ids.ID, reason, by string) (Hypothesis, error) {
	h, err := s.hypothesis(ctx, m.ID, id)
	if err != nil {
		return h, err
	}
	if h.Status != "proposed" {
		return h, fmt.Errorf("%w: %s", ErrReviewed, h.Status)
	}
	if _, err := s.Beliefs.Retract(ctx, s.emitter(), m.ID, h.BeliefID, "dream hypothesis rejected: "+reason, by, false); err != nil {
		return h, err
	}
	if _, err := s.DB.Exec(ctx, `UPDATE dream_hypotheses SET status='rejected', reviewed_by=$2, review_note=$3, reviewed_at=now() WHERE id=$1`, id, by, reason); err != nil {
		return h, err
	}
	s.Events.OperatorAction(ctx, m.ID, by, "dream.reject", "Operator "+by+" rejected a dream hypothesis: "+reason, map[string]any{"hypothesis_id": id, "belief_id": h.BeliefID, "reason": reason})
	h.Status, h.ReviewedBy, h.ReviewNote = "rejected", by, reason
	return h, nil
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// familyOf traces a memory to the actor or channel behind its origin event.
func (s *Service) familyOf(ctx context.Context, m memory.Memory) string {
	if !m.OriginEvent.IsZero() && s.Events != nil {
		if ev, err := s.Events.Get(ctx, m.OriginEvent); err == nil {
			if f := beliefs.FamilyFromEvent(ev); f != "" {
				return f
			}
		}
	}
	return "memory:" + m.Source
}

func rootsOf(m memory.Memory) []string {
	if !m.OriginEvent.IsZero() {
		return []string{string(m.OriginEvent)}
	}
	return []string{string(m.ID)}
}
