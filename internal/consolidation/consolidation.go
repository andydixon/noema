// Package consolidation turns clusters of related episodes into semantic
// memories and beliefs (spec §9). Clustering and confidence are
// deterministic; a model may only propose the wording of a generalisation,
// and every output links back to its episodes. Originals are never touched.
package consolidation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Job is one consolidation run.
type Job struct {
	ID         ids.ID          `json:"id"`
	MindID     ids.ID          `json:"mind_id"`
	Status     string          `json:"status"`
	Trigger    string          `json:"trigger"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Clusters   int             `json:"clusters"`
	Produced   int             `json:"produced"`
	Inputs     json.RawMessage `json:"inputs"`
	Outputs    json.RawMessage `json:"outputs"`
	Error      string          `json:"error"`
	CreatedBy  string          `json:"created_by"`
	CreatedAt  time.Time       `json:"created_at"`
}

// Output is one generalisation produced by a job.
type Output struct {
	MemoryID    ids.ID   `json:"memory_id"`
	BeliefID    ids.ID   `json:"belief_id"`
	Proposition string   `json:"proposition"`
	Confidence  float64  `json:"confidence"`
	Episodes    []ids.ID `json:"episodes"`
	Key         string   `json:"key"`
	Wording     string   `json:"wording"` // model | deterministic
}

// Cluster is a group of episodes sharing distinctive terms.
type Cluster struct {
	Key      string
	Terms    []string
	Episodes []memory.Memory
	Sources  map[string]bool
}

// Service runs consolidation.
type Service struct {
	DB      *pgxpool.Pool
	Memory  *memory.Store
	Beliefs *beliefs.Store
	Events  *evstore.Store
	LLM     *router.Router
	Prompts *prompts.Store
	Minds   *minds.Store
	Log     *slog.Logger
}

// Paused reports whether consolidation is globally paused (operator switch).
var paused = false

// SetPaused toggles the global pause.
func SetPaused(p bool) { paused = p }

// IsPaused reports the switch.
func IsPaused() bool { return paused }

// Clusterise groups episodes by shared distinctive terms. Deterministic:
// terms are stemmed crudely, sorted, and a cluster key is the sorted set of
// the two or three most frequent shared terms.
func Clusterise(eps []memory.Memory, minEpisodes int) []Cluster {
	type ep struct {
		m     memory.Memory
		terms map[string]bool
	}
	items := make([]ep, 0, len(eps))
	df := map[string]int{}
	for _, m := range eps {
		terms := termSet(stripSpeaker(m.Content))
		items = append(items, ep{m, terms})
		for t := range terms {
			df[t]++
		}
	}
	// Candidate keys: pairs of terms appearing in >= minEpisodes episodes.
	var freq []string
	for t, n := range df {
		if n >= minEpisodes && len(t) >= 2 {
			freq = append(freq, t)
		}
	}
	sort.Strings(freq)
	used := map[ids.ID]bool{}
	var out []Cluster
	for i := 0; i < len(freq); i++ {
		for j := i + 1; j < len(freq); j++ {
			a, b := freq[i], freq[j]
			var members []memory.Memory
			sources := map[string]bool{}
			for _, it := range items {
				if used[it.m.ID] || !it.terms[a] || !it.terms[b] {
					continue
				}
				members = append(members, it.m)
				sources[it.m.Source+"/"+string(it.m.Trust)] = true
			}
			if len(members) < minEpisodes {
				continue
			}
			// Extend the key with a third shared term when all members share it.
			key := []string{a, b}
			for _, t := range freq {
				if t == a || t == b {
					continue
				}
				all := true
				for _, m := range members {
					if !termSet(stripSpeaker(m.Content))[t] {
						all = false
						break
					}
				}
				if all {
					key = append(key, t)
					break
				}
			}
			sort.Strings(key)
			for _, m := range members {
				used[m.ID] = true
			}
			sort.Slice(members, func(x, y int) bool { return members[x].OccurredAt.Before(members[y].OccurredAt) })
			out = append(out, Cluster{Key: strings.Join(key, "+"), Terms: key, Episodes: members, Sources: sources})
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].Episodes) > len(out[j].Episodes) })
	return out
}

var stop = map[string]bool{"of": true, "to": true, "in": true, "on": true, "at": true, "an": true, "is": true, "it": true, "as": true, "by": true, "or": true, "if": true, "so": true, "no": true, "up": true, "do": true, "be": true, "we": true, "he": true, "me": true, "my": true, "us": true, "am": true, "the": true, "and": true, "for": true, "that": true, "this": true, "with": true, "from": true, "said": true, "was": true, "were": true, "has": true, "have": true, "had": true, "are": true, "its": true, "into": true, "about": true, "again": true, "after": true, "before": true, "when": true, "then": true, "than": true, "very": true, "just": true, "also": true, "been": true, "will": true, "would": true, "could": true, "should": true, "there": true, "their": true, "they": true, "them": true, "what": true, "which": true, "some": true, "more": true, "most": true, "over": true, "under": true, "your": true, "you": true, "not": true, "but": true, "all": true, "any": true, "can": true, "did": true, "does": true, "one": true, "two": true}

// stripSpeaker removes a leading "someone said: " so the speaker of a
// conversational episode does not become the pattern.
func stripSpeaker(text string) string {
	if i := strings.Index(text, " said: "); i >= 0 && i < 40 {
		return text[i+7:]
	}
	return text
}

// termSet extracts crude stems (lower-cased words with trailing s/ed/ing
// removed) so "deployments" and "deployment" cluster together.
func termSet(text string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		w := strings.Trim(cur.String(), ".-_'")
		cur.Reset()
		if len(w) < 2 || stop[w] {
			return
		}
		for _, suf := range []string{"ing", "ed", "es", "s"} {
			if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
				w = strings.TrimSuffix(w, suf)
				break
			}
		}
		out[w] = true
	}
	for _, r := range strings.ToLower(text) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r == '_' || r == '\'' {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// Proposition renders a deterministic generalisation for a cluster.
func Proposition(c Cluster) string {
	first, last := c.Episodes[0].OccurredAt, c.Episodes[len(c.Episodes)-1].OccurredAt
	span := "over " + humanSpan(last.Sub(first))
	if last.Sub(first) < time.Hour {
		span = "within an hour"
	}
	return fmt.Sprintf("Recurring pattern (%d episodes %s): %s", len(c.Episodes), span, strings.Join(c.Terms, ", "))
}

func humanSpan(d time.Duration) string {
	switch {
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%d weeks", int(d.Hours()/24/7))
	}
}

// Run consolidates a mind: cluster recent episodes, propose a generalisation
// per cluster, compute its confidence from the episodes, store a semantic
// memory and a belief linked to them, and record the job.
func (s *Service) Run(ctx context.Context, m minds.Mind, trigger, by string) (Job, error) {
	job := Job{ID: ids.New(), MindID: m.ID, Status: "running", Trigger: trigger, CreatedBy: by, CreatedAt: time.Now()}
	now := time.Now()
	job.StartedAt = &now
	if _, err := s.DB.Exec(ctx, `INSERT INTO consolidation_jobs (id, mind_id, status, trigger, started_at, created_by) VALUES ($1,$2,'running',$3,now(),$4)`, job.ID, m.ID, trigger, by); err != nil {
		return job, err
	}
	finish := func(status, errText string, clusters, produced int, inputs, outputs any) (Job, error) {
		in, _ := json.Marshal(inputs)
		out, _ := json.Marshal(outputs)
		if inputs == nil {
			in = []byte("[]")
		}
		if outputs == nil {
			out = []byte("[]")
		}
		_, err := s.DB.Exec(ctx, `UPDATE consolidation_jobs SET status=$2, error=$3, clusters=$4, produced=$5, inputs=$6, outputs=$7, finished_at=now() WHERE id=$1`, job.ID, status, errText, clusters, produced, in, out)
		job.Status, job.Error, job.Clusters, job.Produced, job.Inputs, job.Outputs = status, errText, clusters, produced, in, out
		return job, err
	}
	if paused && trigger == "schedule" {
		return finish("rejected", "consolidation is paused", 0, 0, nil, nil)
	}
	since := now.Add(-30 * 24 * time.Hour)
	eps, err := s.Memory.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Episodic}, Since: since, Limit: 500})
	if err != nil {
		return finish("failed", err.Error(), 0, 0, nil, nil)
	}
	// Skip episodes already consolidated (they have a derived_from edge into a semantic memory).
	var fresh []memory.Memory
	var inputs []ids.ID
	for _, e := range eps {
		var n int
		s.DB.QueryRow(ctx, `SELECT count(*) FROM memory_relationships WHERE to_memory=$1 AND relation='derived_from'`, e.ID).Scan(&n) //nolint:errcheck
		if n == 0 {
			fresh = append(fresh, e)
			inputs = append(inputs, e.ID)
		}
	}
	clusters := Clusterise(fresh, m.Config.ConsolidationMinEpisodes)
	llmBudget := m.Config.ConsolidationLLMCalls
	var outputs []Output
	for _, c := range clusters {
		out, err := s.consolidate(ctx, m, c, &llmBudget)
		if err != nil {
			s.Log.Warn("consolidation cluster failed", "mind", m.Slug, "key", c.Key, "err", err)
			continue
		}
		outputs = append(outputs, out)
	}
	return finish("done", "", len(clusters), len(outputs), inputs, outputs)
}

func (s *Service) consolidate(ctx context.Context, m minds.Mind, c Cluster, llmBudget *int) (Output, error) {
	prop := Proposition(c)
	wording := "deterministic"
	var callID ids.ID
	if s.LLM != nil && s.Prompts != nil && *llmBudget > 0 && s.LLM.Available(ctx, llm.RoleSummarise) {
		if pr, err := s.Prompts.Get(ctx, "consolidate"); err == nil {
			var lines []string
			for _, e := range c.Episodes {
				lines = append(lines, e.OccurredAt.Format("2006-01-02")+": "+e.Content)
			}
			if req, err := pr.Render(map[string]any{"Episodes": lines, "Terms": c.Terms}); err == nil {
				req.MindID, req.Purpose = m.ID, "consolidate cluster "+c.Key
				*llmBudget--
				if resp, err := s.LLM.Complete(ctx, req); err == nil && len(resp.JSON) > 0 {
					var out struct {
						Proposition string `json:"proposition"`
						Supported   bool   `json:"supported"`
					}
					if json.Unmarshal(resp.JSON, &out) == nil && out.Supported && len(strings.TrimSpace(out.Proposition)) > 8 {
						prop, wording, callID = strings.TrimSpace(out.Proposition), "model", resp.CallID
					}
				}
			}
		}
	}
	var output Output
	err := storage.Tx(ctx, s.DB, func(tx pgx.Tx) error {
		var ev []beliefs.EvidenceInput
		var refs []events.EvidenceRef
		var epIDs []ids.ID
		for _, e := range c.Episodes {
			ev = append(ev, beliefs.EvidenceInput{Direction: "for", Kind: "memory", RefID: string(e.ID), Source: e.Source + "/" + string(e.Trust), Weight: 0.3, Credibility: e.Credibility, Note: "episode: " + truncate(e.Content, 80)})
			refs = append(refs, events.EvidenceRef{Kind: "memory", ID: string(e.ID), Note: "episode"})
			epIDs = append(epIDs, e.ID)
		}
		emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
			out, err := s.Events.AppendIn(ctx, q, n)
			if err != nil {
				return events.Event{}, err
			}
			return out[0], nil
		}
		res, err := s.Beliefs.Create(ctx, tx, emit, beliefs.New{MindID: m.ID, Proposition: prop, Subject: c.Key, Epistemic: concepts.Inference, Source: "consolidation", Evidence: ev, Detail: map[string]any{"cluster": c.Key, "episodes": len(c.Episodes), "wording": wording}})
		if err != nil {
			return err
		}
		mem, err := s.Memory.Create(ctx, tx, memory.New{MindID: m.ID, Kind: concepts.Semantic, Epistemic: concepts.Inference, Content: prop, Source: "consolidation", Trust: events.TrustSystem,
			Confidence: res.Belief.Confidence, Importance: 0.5 + 0.1*float64(min(len(c.Episodes), 5)), Salience: 0.4, Detail: map[string]any{"belief_id": res.Belief.ID, "cluster": c.Key, "wording": wording}})
		if err != nil {
			return err
		}
		for _, e := range c.Episodes {
			if _, err := s.Memory.Relate(ctx, tx, m.ID, mem.ID, e.ID, concepts.DerivedFrom, res.Belief.Confidence, "consolidation"); err != nil {
				return err
			}
		}
		if !callID.IsZero() {
			refs = append(refs, events.EvidenceRef{Kind: "llm_call", ID: string(callID), Note: "wording"})
		}
		refs = append(refs, events.EvidenceRef{Kind: "memory", ID: string(mem.ID), Note: "generalisation"}, events.EvidenceRef{Kind: "belief", ID: string(res.Belief.ID)})
		evs, err := s.Events.AppendIn(ctx, tx, events.New{MindID: m.ID, Type: events.MemoryConsolidated, Source: "consolidation", Summary: fmt.Sprintf("consolidated %d episodes (%.2f): %s", len(c.Episodes), res.Belief.Confidence, truncate(prop, 120)),
			Salience: 0.5, Confidence: res.Belief.Confidence, Evidence: refs, Metadata: map[string]any{"memory_id": mem.ID, "belief_id": res.Belief.ID, "episodes": len(c.Episodes), "wording": wording},
			Payload: map[string]any{"proposition": prop, "cluster": c.Key, "episodes": epIDs, "breakdown": res.Breakdown}})
		if err != nil {
			return err
		}
		s.Events.Publish(append(res.Events, evs...)...)
		output = Output{MemoryID: mem.ID, BeliefID: res.Belief.ID, Proposition: prop, Confidence: res.Belief.Confidence, Episodes: epIDs, Key: c.Key, Wording: wording}
		return nil
	})
	return output, err
}

// Jobs lists recent jobs for a mind.
func (s *Service) Jobs(ctx context.Context, mind ids.ID, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, status, trigger, started_at, finished_at, clusters, produced, inputs, outputs, error, created_by, created_at FROM consolidation_jobs WHERE mind_id=$1 ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.MindID, &j.Status, &j.Trigger, &j.StartedAt, &j.FinishedAt, &j.Clusters, &j.Produced, &j.Inputs, &j.Outputs, &j.Error, &j.CreatedBy, &j.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Reject undoes a generalisation: the memory is retracted, the belief pinned
// rejected, and an operator action recorded. Episodes are untouched.
func (s *Service) Reject(ctx context.Context, m minds.Mind, memoryID ids.ID, reason, by string) error {
	mem, err := s.Memory.Get(ctx, m.ID, memoryID)
	if err != nil {
		return err
	}
	if err := s.Memory.SetStatus(ctx, m.ID, memoryID, memory.StatusRetracted); err != nil {
		return err
	}
	if bid, ok := mem.Detail["belief_id"].(string); ok && bid != "" {
		emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
			out, err := s.Events.AppendIn(ctx, q, n)
			if err != nil {
				return events.Event{}, err
			}
			s.Events.Publish(out...)
			return out[0], nil
		}
		if _, err := s.Beliefs.Retract(ctx, emit, m.ID, ids.ID(bid), "consolidation rejected: "+reason, by, false); err != nil {
			s.Log.Warn("reject consolidation belief failed", "err", err)
		}
	}
	s.Events.OperatorAction(ctx, m.ID, by, "consolidation.reject", "Operator "+by+" rejected a consolidation: "+reason, map[string]any{"memory_id": memoryID, "reason": reason})
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
