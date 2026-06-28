// Package reflection reviews a mind's recent history on a schedule: mistakes,
// abandoned hypotheses, belief changes, completed goals, unresolved
// contradictions, prediction errors and operator corrections. It produces an
// explicit reflection event and, where a pattern repeats, a procedural memory
// (spec §18). Findings are computed; a model may only write the prose.
package reflection

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Findings is the structured review.
type Findings struct {
	PeriodStart      time.Time               `json:"period_start"`
	PeriodEnd        time.Time               `json:"period_end"`
	PredictionErrors []string                `json:"prediction_errors"`
	Corrections      []string                `json:"corrections"`
	RejectedBeliefs  []string                `json:"rejected_beliefs"`
	ChangedBeliefs   []string                `json:"changed_beliefs"`
	CompletedGoals   []string                `json:"completed_goals"`
	Contradictions   []string                `json:"contradictions"`
	Calibration      predictions.Calibration `json:"calibration"`
	Patterns         []string                `json:"patterns"` // repeated failure subjects
	Lessons          []string                `json:"lessons"`  // procedural memories written
	Counts           map[string]int          `json:"counts"`
}

// Reflection is a stored review.
type Reflection struct {
	ID          ids.ID    `json:"id"`
	MindID      ids.ID    `json:"mind_id"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	Findings    Findings  `json:"findings"`
	Prose       string    `json:"prose"`
	EventID     ids.ID    `json:"event_id,omitempty"`
	LLMCallID   ids.ID    `json:"llm_call_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Service runs reflections.
type Service struct {
	DB          *pgxpool.Pool
	Events      *evstore.Store
	Beliefs     *beliefs.Store
	Goals       *goals.Store
	Memory      *memory.Store
	Predictions *predictions.Store
	LLM         *router.Router
	Prompts     *prompts.Store
	Log         *slog.Logger
}

// Run reviews the period since the last reflection (or the interval) and
// records the outcome.
func (s *Service) Run(ctx context.Context, m minds.Mind, trigger, by string) (Reflection, error) {
	end := time.Now()
	start := end.Add(-m.Config.ReflectionInterval.D())
	if last, err := s.Latest(ctx, m.ID); err == nil && last.PeriodEnd.After(start) {
		start = last.PeriodEnd
	}
	f := Findings{PeriodStart: start, PeriodEnd: end, Counts: map[string]int{}}
	evs, err := s.Events.Query(ctx, evstore.Filter{MindID: m.ID, Since: start, Until: end, Types: []events.Type{events.PredictionError, events.BeliefRetracted, events.OperatorAction, events.GoalCompleted, events.ContradictionDetected, events.GoalAbandoned}, Ascending: true, Limit: 1000})
	if err != nil {
		return Reflection{}, err
	}
	subjectFailures := map[string]int{}
	for _, ev := range evs {
		f.Counts[string(ev.Type)]++
		switch ev.Type {
		case events.PredictionError:
			f.PredictionErrors = append(f.PredictionErrors, ev.Summary)
			subjectFailures[subjectOf(ev.Summary)]++
		case events.BeliefRetracted:
			f.RejectedBeliefs = append(f.RejectedBeliefs, ev.Summary)
			subjectFailures[subjectOf(ev.Summary)]++
		case events.OperatorAction:
			if strings.HasPrefix(fmt.Sprint(ev.Metadata["action"]), "memory.") || strings.HasPrefix(fmt.Sprint(ev.Metadata["action"]), "correction") || strings.HasPrefix(fmt.Sprint(ev.Metadata["action"]), "belief") {
				f.Corrections = append(f.Corrections, ev.Summary)
			}
		case events.GoalCompleted:
			f.CompletedGoals = append(f.CompletedGoals, ev.Summary)
		case events.ContradictionDetected:
			f.Contradictions = append(f.Contradictions, ev.Summary)
		}
	}
	if ch, err := s.Beliefs.RecentChanges(ctx, m.ID, start, 20); err == nil {
		for _, c := range ch {
			f.ChangedBeliefs = append(f.ChangedBeliefs, fmt.Sprintf("%s: %s → %s (%s)", c.Belief.Proposition, c.FromStatus, c.Belief.Status, c.Reason))
		}
	}
	if contested, err := s.Beliefs.Contested(ctx, m.ID, 20); err == nil {
		for _, b := range contested {
			f.Contradictions = append(f.Contradictions, "still contested: "+b.Proposition)
		}
	}
	f.Calibration, _ = s.Predictions.Calibrate(ctx, m.ID, end.Add(-90*24*time.Hour))
	for subj, n := range subjectFailures {
		if n >= 2 && subj != "" {
			f.Patterns = append(f.Patterns, fmt.Sprintf("%d failures concerning %q", n, subj))
		}
	}
	if f.Calibration.Overconfident {
		f.Patterns = append(f.Patterns, "stated confidence runs ahead of outcomes")
	}
	// Lessons become procedural memories, once per pattern.
	for _, p := range f.Patterns {
		lesson := "Lesson: " + p + ". Seek an independent source before raising confidence on this subject."
		if existing, _ := s.Memory.List(ctx, m.ID, memory.Filter{Kinds: []concepts.MemoryKind{concepts.Procedural}, Search: strings.Trim(p, "\""), Limit: 1}); len(existing) > 0 {
			continue
		}
		if _, err := s.Memory.Create(ctx, s.DB, memory.New{MindID: m.ID, Kind: concepts.Procedural, Epistemic: concepts.Inference, Content: lesson, Source: "reflection", Trust: events.TrustSystem, Confidence: 0.6, Importance: 0.7, Salience: 0.4}); err == nil {
			f.Lessons = append(f.Lessons, lesson)
		}
	}
	prose := Render(f)
	var callID ids.ID
	if s.LLM != nil && s.Prompts != nil && m.Config.ReflectionLLMCalls > 0 && s.LLM.Available(ctx, llm.RoleNarrate) {
		if pr, err := s.Prompts.Get(ctx, "reflect"); err == nil {
			if req, err := pr.Render(map[string]any{"Findings": prose, "Brief": m.Personality.Brief()}); err == nil {
				req.MindID, req.Purpose = m.ID, "reflection prose"
				if resp, err := s.LLM.Complete(ctx, req); err == nil && strings.TrimSpace(resp.Text) != "" {
					prose, callID = strings.TrimSpace(resp.Text), resp.CallID
				}
			}
		}
	}
	total := len(f.PredictionErrors) + len(f.RejectedBeliefs) + len(f.ChangedBeliefs) + len(f.CompletedGoals) + len(f.Contradictions) + len(f.Corrections)
	refs := []events.EvidenceRef{}
	if !callID.IsZero() {
		refs = append(refs, events.EvidenceRef{Kind: "llm_call", ID: string(callID), Note: "prose"})
	}
	evsOut, err := s.Events.Append(ctx, events.New{MindID: m.ID, Type: events.Reflection, Source: "reflection", Summary: fmt.Sprintf("reflected on %s: %d notable items, %d patterns, %d lessons", humanSpan(end.Sub(start)), total, len(f.Patterns), len(f.Lessons)),
		Salience: 0.3 + 0.1*float64(min(len(f.Patterns), 5)), Confidence: 0.8, Evidence: refs, Metadata: map[string]any{"trigger": trigger, "items": total, "patterns": len(f.Patterns)}, Payload: map[string]any{"findings": f, "prose": prose}})
	if err != nil {
		return Reflection{}, err
	}
	r := Reflection{ID: ids.New(), MindID: m.ID, PeriodStart: start, PeriodEnd: end, Findings: f, Prose: prose, EventID: evsOut[0].ID, LLMCallID: callID, CreatedAt: end}
	fj, _ := json.Marshal(f)
	var call *string
	if !callID.IsZero() {
		c := string(callID)
		call = &c
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO reflections (id, mind_id, period_start, period_end, findings, prose, event_id, llm_call_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, r.ID, r.MindID, r.PeriodStart, r.PeriodEnd, fj, r.Prose, string(r.EventID), call)
	return r, err
}

// Render writes the findings as plain prose without a model.
func Render(f Findings) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Reviewing the last %s.\n", humanSpan(f.PeriodEnd.Sub(f.PeriodStart)))
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString(title + ":\n")
		for i, it := range items {
			if i >= 6 {
				fmt.Fprintf(&b, "  … and %d more\n", len(items)-6)
				break
			}
			b.WriteString("  - " + it + "\n")
		}
	}
	section("Predictions that failed", f.PredictionErrors)
	section("Beliefs I had to reject", f.RejectedBeliefs)
	section("Operator corrections", f.Corrections)
	section("Beliefs that changed status", f.ChangedBeliefs)
	section("Goals completed", f.CompletedGoals)
	section("Unresolved contradictions", f.Contradictions)
	section("Patterns", f.Patterns)
	section("Lessons recorded", f.Lessons)
	if f.Calibration.Total > 0 {
		fmt.Fprintf(&b, "Calibration over 90 days: accuracy %.0f%%, Brier %.2f.\n", f.Calibration.Accuracy*100, f.Calibration.Brier)
	}
	if b.Len() < 60 {
		b.WriteString("Nothing notable happened.\n")
	}
	return strings.TrimSpace(b.String())
}

func humanSpan(d time.Duration) string {
	switch {
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// subjectOf extracts a crude subject (first hostname-like or long token) from a summary.
func subjectOf(s string) string {
	for _, w := range strings.Fields(strings.ToLower(s)) {
		w = strings.Trim(w, ".,:;()\"'")
		if strings.ContainsAny(w, "-.") && len(w) >= 5 {
			return w
		}
	}
	return ""
}

// Latest returns the most recent reflection.
func (s *Service) Latest(ctx context.Context, mind ids.ID) (Reflection, error) {
	var r Reflection
	var fj []byte
	var ev, call *string
	err := s.DB.QueryRow(ctx, `SELECT id, mind_id, period_start, period_end, findings, prose, event_id, llm_call_id, created_at FROM reflections WHERE mind_id=$1 ORDER BY created_at DESC LIMIT 1`, mind).
		Scan(&r.ID, &r.MindID, &r.PeriodStart, &r.PeriodEnd, &fj, &r.Prose, &ev, &call, &r.CreatedAt)
	if err != nil {
		return r, err
	}
	json.Unmarshal(fj, &r.Findings) //nolint:errcheck
	if ev != nil {
		r.EventID = ids.ID(*ev)
	}
	if call != nil {
		r.LLMCallID = ids.ID(*call)
	}
	return r, nil
}

// List returns recent reflections.
func (s *Service) List(ctx context.Context, mind ids.ID, limit int) ([]Reflection, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.DB.Query(ctx, `SELECT id, mind_id, period_start, period_end, findings, prose, event_id, llm_call_id, created_at FROM reflections WHERE mind_id=$1 ORDER BY created_at DESC LIMIT $2`, mind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Reflection
	for rows.Next() {
		var r Reflection
		var fj []byte
		var ev, call *string
		if err := rows.Scan(&r.ID, &r.MindID, &r.PeriodStart, &r.PeriodEnd, &fj, &r.Prose, &ev, &call, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(fj, &r.Findings) //nolint:errcheck
		if ev != nil {
			r.EventID = ids.ID(*ev)
		}
		if call != nil {
			r.LLMCallID = ids.ID(*call)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
