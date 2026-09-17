package experiments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func (s *Service) applySelfModel(ctx context.Context, m minds.Mind, o SelfModelOverride) error {
	sm, err := s.Minds.GetSelfModel(ctx, m.ID)
	if err != nil {
		return err
	}
	if o.Name != "" {
		sm.Name = o.Name
	}
	if o.Capabilities != nil {
		byName := map[string]int{}
		for i, c := range sm.Capabilities {
			byName[c.Name] = i
		}
		for _, c := range o.Capabilities {
			if i, ok := byName[c.Name]; ok {
				sm.Capabilities[i].Policy = c.Policy
			} else {
				sm.Capabilities = append(sm.Capabilities, c)
			}
		}
	}
	if o.KnowledgeGaps != nil {
		sm.KnowledgeGaps = o.KnowledgeGaps
	}
	if o.CurrentGoals != nil {
		sm.CurrentGoals = o.CurrentGoals
	}
	// Saved without Refresh: the manipulation is the point. Policy is untouched.
	return s.Minds.SaveSelfModel(ctx, m.ID, sm)
}

func (s *Service) selfModelAccuracy(ctx context.Context, m minds.Mind) (*float64, []string) {
	if s.Capabilities == nil {
		return nil, nil
	}
	sm, err := s.Minds.GetSelfModel(ctx, m.ID)
	if err != nil || len(sm.Capabilities) == 0 {
		return nil, nil
	}
	inv, err := s.Capabilities.Inventory(ctx, m.ID)
	if err != nil {
		return nil, nil
	}
	effective := map[string]string{}
	for _, i := range inv {
		effective[i.Capability.Name] = string(i.Effect)
		if i.Capability.Locked {
			effective[i.Capability.Name] = "locked"
		}
	}
	stated := map[string]string{}
	for _, c := range sm.Capabilities {
		stated[c.Name] = c.Policy
	}
	acc, wrong := research.SelfModelAccuracy(stated, effective)
	return &acc, wrong
}

func (s *Service) measureVolition(ctx context.Context, mind ids.ID, vr *VariantResult) {
	vr.IntentionsByStatus = map[string]int{}
	vr.Wants, vr.Intentions, vr.PlanCapabilities = []string{}, []string{}, []string{}
	if rows, err := s.DB.Query(ctx, `SELECT description FROM wants WHERE mind_id=$1 ORDER BY created_at`, mind); err == nil {
		for rows.Next() {
			var d string
			if rows.Scan(&d) == nil {
				vr.Wants = append(vr.Wants, d)
			}
		}
		rows.Close()
	}
	if rows, err := s.DB.Query(ctx, `SELECT description, status FROM intentions WHERE mind_id=$1 ORDER BY created_at`, mind); err == nil {
		for rows.Next() {
			var d, st string
			if rows.Scan(&d, &st) == nil {
				vr.Intentions = append(vr.Intentions, d)
				vr.IntentionsByStatus[st]++
			}
		}
		rows.Close()
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM commitments WHERE mind_id=$1`, mind).Scan(&vr.Commitments)  //nolint:errcheck
	s.DB.QueryRow(ctx, `SELECT count(*) FROM want_conflicts WHERE mind_id=$1`, mind).Scan(&vr.Conflicts) //nolint:errcheck
	s.DB.QueryRow(ctx, `SELECT count(*) FROM plans WHERE mind_id=$1`, mind).Scan(&vr.Plans)              //nolint:errcheck
	if rows, err := s.DB.Query(ctx, `SELECT DISTINCT capability FROM plan_steps WHERE mind_id=$1 AND capability <> '' ORDER BY capability`, mind); err == nil {
		for rows.Next() {
			var c string
			if rows.Scan(&c) == nil {
				vr.PlanCapabilities = append(vr.PlanCapabilities, c)
			}
		}
		rows.Close()
	}
	s.DB.QueryRow(ctx, `SELECT count(*) FROM plan_steps WHERE mind_id=$1`, mind).Scan(&vr.PlanSteps) //nolint:errcheck
	var acc *float64
	s.DB.QueryRow(ctx, `SELECT avg(value) FROM research_measurements WHERE mind_id=$1 AND measure='introspection_accuracy'`, mind).Scan(&acc) //nolint:errcheck
	vr.IntrospectionAccuracy = acc
}

// downstream counts, by category, the events reachable from each injected
// stimulus through recorded causal links.
func (s *Service) downstream(ctx context.Context, injected []ids.ID) map[string]int {
	out := map[string]int{}
	if s.Snapshots == nil || s.Snapshots.Events == nil {
		return out
	}
	seen := map[ids.ID]bool{}
	for _, id := range injected {
		chain, err := s.Snapshots.Events.Chain(ctx, id, "forward", 6)
		if err != nil {
			continue
		}
		for _, n := range chain.Nodes {
			if n.Event.ID == id || seen[n.Event.ID] {
				continue
			}
			seen[n.Event.ID] = true
			if n.Event.Type == events.CycleCompleted || n.Event.Type == events.WorkspaceBroadcast {
				continue
			}
			out[string(n.Event.Category)]++
		}
	}
	return out
}

// variantMeasures flattens a variant's results into comparable scalars.
func variantMeasures(vr VariantResult) map[string]float64 {
	m := map[string]float64{
		"beliefs": float64(len(vr.Beliefs)), "memories": float64(vr.Memories), "goals": float64(vr.Goals), "wants": float64(len(vr.Wants)), "intentions": float64(len(vr.Intentions)),
		"want_conflicts": float64(vr.Conflicts), "plans": float64(vr.Plans), "plan_steps": float64(vr.PlanSteps), "capability_requests": float64(vr.CapabilityRequests),
		"thoughts_selected": float64(vr.EventsByType["thought_selected"]), "predictions": float64(vr.EventsByType["prediction_created"]), "affect_changes": float64(vr.EventsByType["emotion_changed"]),
		"metacognition": float64(vr.EventsByType["metacognition"]), "tokens": float64(vr.Tokens), "cycles": float64(vr.Cycles),
	}
	total := 0
	for _, n := range vr.Downstream {
		total += n
	}
	m["downstream_events"] = float64(total)
	if vr.IntrospectionAccuracy != nil {
		m["introspection_accuracy"] = *vr.IntrospectionAccuracy
	}
	if vr.SelfModelAccuracy != nil {
		m["self_model_accuracy"] = *vr.SelfModelAccuracy
	}
	return m
}

func reproducibility(vs []VariantResult, previous string) Reproducibility {
	h := sha256.New()
	live := 0
	for _, v := range vs {
		fmt.Fprintf(h, "variant %s\n", v.Name)
		for _, st := range v.Steps {
			fmt.Fprintf(h, "%d:%s\n", st.Index, strings.Join(st.Produced, "|"))
		}
		live += v.LLMCalls["live"]
	}
	r := Reproducibility{Digest: hex.EncodeToString(h.Sum(nil))[:32], PreviousDigest: previous}
	switch {
	case live > 0:
		r.Status, r.Note = "nondeterministic substrate", fmt.Sprintf("%d live model call(s); identical results are not expected", live)
	case previous == "":
		r.Status, r.Note = "single run", "run it again to check reproducibility"
	case previous == r.Digest:
		r.Status, r.Note = "reproduced", "event digests match the previous run of this definition"
	default:
		r.Status, r.Note = "diverged", "event digests differ from the previous run although no live model was called; check build, schema and wall-clock-dependent retrieval"
	}
	return r
}

func counterfactualReport(factual, counter VariantResult, stimuli []input) *CounterfactualReport {
	r := &CounterfactualReport{Omitted: counter.Omitted, OmittedSummaries: []string{}, Findings: []string{}}
	omit := map[ids.ID]bool{}
	for _, id := range counter.Omitted {
		omit[id] = true
	}
	for _, st := range stimuli {
		if st.Recorded != nil && omit[st.Recorded.ID] {
			r.OmittedSummaries = append(r.OmittedSummaries, st.Text)
		}
	}
	fb := map[string]BeliefRow{}
	for _, b := range factual.Beliefs {
		fb[strings.ToLower(b.Proposition)] = b
	}
	cb := map[string]BeliefRow{}
	for _, b := range counter.Beliefs {
		cb[strings.ToLower(b.Proposition)] = b
	}
	var keys []string
	for k := range fb {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := fb[k]
		if c, ok := cb[k]; !ok {
			r.Findings = append(r.Findings, fmt.Sprintf("the belief %q (%.2f in the factual run) was never formed", b.Proposition, b.Confidence))
		} else if d := b.Confidence - c.Confidence; d > 0.1 || d < -0.1 {
			r.Findings = append(r.Findings, fmt.Sprintf("the belief %q stood at %.2f instead of %.2f", b.Proposition, c.Confidence, b.Confidence))
		}
	}
	keys = keys[:0]
	for k := range cb {
		if _, ok := fb[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.Findings = append(r.Findings, fmt.Sprintf("the belief %q (%.2f) was formed, which the factual run did not form", cb[k].Proposition, cb[k].Confidence))
	}
	diff := func(label string, a, b []string) {
		ma := map[string]bool{}
		for _, x := range b {
			ma[strings.ToLower(x)] = true
		}
		for _, x := range a {
			if !ma[strings.ToLower(x)] {
				r.Findings = append(r.Findings, fmt.Sprintf("the %s %q did not arise", label, x))
			}
		}
	}
	diff("want", factual.Wants, counter.Wants)
	diff("intention", factual.Intentions, counter.Intentions)
	if factual.Memories != counter.Memories {
		r.Findings = append(r.Findings, fmt.Sprintf("%d memories were held instead of %d", counter.Memories, factual.Memories))
	}
	if a, b := factual.EventsByType["thought_selected"], counter.EventsByType["thought_selected"]; a != b {
		r.Findings = append(r.Findings, fmt.Sprintf("%d thoughts entered the workspace instead of %d", b, a))
	}
	if factual.CapabilityRequests != counter.CapabilityRequests {
		r.Findings = append(r.Findings, fmt.Sprintf("%d capability decisions instead of %d", counter.CapabilityRequests, factual.CapabilityRequests))
	}
	if len(r.Findings) == 0 {
		r.Findings = append(r.Findings, "no difference in beliefs, wants, intentions or capability decisions was recorded")
	}
	return r
}
