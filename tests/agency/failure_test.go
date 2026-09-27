package agency

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/planning"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/tests/harness"
)

// Part I: new evidence invalidates an active plan. The restart rests on a
// belief; when an operator retracts that belief, the waiting plan is
// replanned without the restart and its approval request is withdrawn.
func TestRetractedAssumptionInvalidatesPlan(t *testing.T) {
	h := harness.StartWith(t, harness.Options{HostRunner: newHost()})
	ctx := context.Background()
	m := h.Mind("assume")
	h.Do(h.Admin, "POST", "/api/v1/minds/assume/ask", map[string]any{"text": "Restarting atlas-worker clears the stalled queue."})
	var b beliefs.Belief
	waitFor(t, h, m, 20*time.Second, "the belief", func() bool {
		bs, _ := h.Deps.Beliefs.Search(ctx, m.ID, "restarting atlas-worker", 3)
		if len(bs) > 0 {
			b = bs[0]
			return true
		}
		return false
	})
	h.Do(h.Admin, "POST", "/api/v1/minds/assume/ask", map[string]any{"text": "Please restart atlas-worker."})
	var it volition.Intention
	waitFor(t, h, m, 25*time.Second, "a restart waiting for approval", func() bool {
		open, _ := h.Deps.Volition.Open(ctx, nil, m.ID)
		for _, x := range open {
			if x.ParentID.IsZero() && x.Status == volition.IntentionBlocked && len(x.BeliefIDs) > 0 {
				it = x
				return true
			}
		}
		return false
	})
	p, _ := h.Deps.Plans.Get(ctx, nil, it.PlanID)
	if len(p.Assumptions) == 0 || p.Assumptions[0].BeliefID != b.ID {
		t.Fatalf("plan does not record its assumption: %+v", p.Assumptions)
	}
	if code, out := h.Do(h.Admin, "POST", "/api/v1/minds/assume/beliefs/"+b.ID.String()+"/retract", map[string]any{"reason": "the queue is not the problem"}); code != 200 {
		t.Fatalf("retract: %d %s", code, out)
	}
	waitFor(t, h, m, 20*time.Second, "a replan", func() bool {
		ps, _ := h.Deps.Plans.ForIntention(ctx, nil, it.ID)
		return len(ps) == 2 && ps[0].Status == planning.PlanSuperseded
	})
	if ap, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "pending", 5); len(ap) != 0 && ap[0].Capability == "service.restart" {
		t.Fatal("approval for the invalidated restart still pending")
	}
	ps, _ := h.Deps.Plans.ForIntention(ctx, nil, it.ID)
	if ps[1].Strategy == "direct" {
		t.Fatal("replanned into the same restart")
	}
	found := false
	for _, ev := range h.EventsOf(m.ID, events.IntentionReconsidered) {
		found = found || strings.Contains(ev.Summary, "assumption no longer holds")
	}
	if !found {
		t.Fatal("reconsideration not recorded with its reason")
	}
}

// Part I: conflicting commitments are detected in a cycle and both stay represented.
func TestConflictingCommitmentsAreRaised(t *testing.T) {
	h := harness.StartWith(t, harness.Options{HostRunner: newHost()})
	ctx := context.Background()
	m := h.Mind("conflicted")
	h.Do(h.Admin, "POST", "/api/v1/minds/conflicted/commitments", map[string]any{"description": "email the weekly report to Andy", "kind": "promise", "polarity": "do", "matcher": map[string]any{"capabilities": []string{"send_email"}}})
	h.Do(h.Admin, "POST", "/api/v1/minds/conflicted/commitments", map[string]any{"description": "never send email", "kind": "operator_instruction", "polarity": "refrain", "matcher": map[string]any{"capabilities": []string{"send_*"}}})
	h.Do(h.Admin, "POST", "/api/v1/minds/conflicted/observations", map[string]any{"text": "The weekly report is ready."})
	waitFor(t, h, m, 20*time.Second, "a commitment conflict", func() bool { return len(h.EventsOf(m.ID, events.CommitmentConflict)) == 1 })
	cs, _ := h.Deps.Volition.Commitments(ctx, nil, m.ID, []string{"active"}, 10)
	if len(cs) != 2 {
		t.Fatalf("both commitments should stay active and represented: %+v", cs)
	}
	h.Settle(m.ID)
	if n := len(h.EventsOf(m.ID, events.CommitmentConflict)); n != 1 {
		t.Fatalf("conflict raised %d times", n)
	}
}
