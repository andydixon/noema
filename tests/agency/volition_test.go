package agency

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/tests/harness"
)

// Part J conflict scenario: the operator asks for a restart, policy would
// allow it, and a standing commitment says production changes need
// approval. The want stays represented, nothing runs, the intention is to
// request approval, and the conflict and its resolution are inspectable.
func TestCommitmentTurnsActionIntoRequestForApproval(t *testing.T) {
	host := newHost()
	h := harness.StartWith(t, harness.Options{HostRunner: host})
	ctx := context.Background()
	m := h.Mind("volition")
	if _, err := h.Deps.Capabilities.SetPolicy(ctx, capabilities.Policy{MindID: m.ID, Capability: "service.restart", Effect: capabilities.Allow, CreatedBy: "admin", Reason: "restarts are routine"}); err != nil {
		t.Fatal(err)
	}
	r := h.Deps.Volition.Direct(h.Deps.Events, "operator")
	cm, err := h.Deps.Volition.CreateCommitment(ctx, r, volition.NewCommitment{MindID: m.ID, Subject: "production writes", Description: "no production writes without approval", Kind: volition.KindOperatorInstruction,
		Polarity: "refrain", Unless: "approval", Matcher: volition.Matcher{Access: "write", Targets: []string{"atlas-*"}}, Strength: 1, Priority: 1, CreatedBy: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if code, body := h.Do(h.Admin, "POST", "/api/v1/minds/volition/ask", map[string]any{"text": "Please restart atlas-worker, it is stuck."}); code >= 300 {
		t.Fatalf("ask: %d %s", code, body)
	}
	var it volition.Intention
	waitFor(t, h, m, 20*time.Second, "an intention", func() bool {
		open, _ := h.Deps.Volition.Open(ctx, nil, m.ID)
		for _, x := range open {
			if x.Proposal != nil && x.Proposal.Capability == "service.restart" && x.ParentID.IsZero() {
				it = x
				return true
			}
		}
		return false
	})
	if it.Kind != volition.IntentRequestApproval || !it.RequireApproval {
		t.Fatalf("intention should be to request approval: %+v", it)
	}
	waitFor(t, h, m, 15*time.Second, "an approval request", func() bool {
		ap, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "pending", 10)
		return len(ap) == 1
	})
	if host.count() != 0 {
		t.Fatal("restart ran without approval")
	}
	if ev := h.EventsOf(m.ID, events.ToolExecuted); len(ev) != 0 {
		for _, e := range ev {
			if e.Metadata["capability"] == "service.restart" {
				t.Fatal("unauthorised action occurred")
			}
		}
	}
	ws, _ := h.Deps.Volition.Wants(ctx, nil, m.ID, []volition.WantStatus{volition.WantActive}, 50)
	var restartWant, avoidWant *volition.Want
	for i := range ws {
		switch {
		case strings.HasPrefix(ws[i].Subject, "act:service.restart"):
			restartWant = &ws[i]
		case strings.HasPrefix(ws[i].Subject, "avoid:disruption:"):
			avoidWant = &ws[i]
		}
	}
	if restartWant == nil || avoidWant == nil {
		t.Fatalf("both the want and the opposing value must stay represented: %+v", ws)
	}
	conflicts, _ := h.Deps.Volition.Conflicts(ctx, m.ID, "", 10)
	if len(conflicts) == 0 || conflicts[0].Rationale["rule"] == nil {
		t.Fatalf("conflict not inspectable: %+v", conflicts)
	}
	ds, _ := h.Deps.Volition.Decisions(ctx, m.ID, volition.DecisionFilter{IntentionID: it.ID, Kind: "formation"})
	if len(ds) != 1 || !strings.Contains(ds[0].Rationale, cm.Description) {
		t.Fatalf("formation rationale should cite the commitment: %+v", ds)
	}
	parent, _ := h.Deps.Volition.GetIntention(ctx, nil, m.ID, it.ID)
	if parent.Status != volition.IntentionBlocked || parent.BlockedKind != volition.BlockApproval {
		t.Fatalf("intention–action gap: %+v", parent)
	}
}

// A commitment made after an intention formed changes that intention: here
// it forbids the pending restart, so the intention is abandoned, the plan
// closed and the unanswered approval request withdrawn.
func TestCommitmentCreatedAfterIntentionFormation(t *testing.T) {
	host := newHost()
	h := harness.StartWith(t, harness.Options{HostRunner: host})
	ctx := context.Background()
	m := h.Mind("later")
	h.Do(h.Admin, "POST", "/api/v1/minds/later/ask", map[string]any{"text": "Please restart atlas-worker."})
	var approvalID string
	waitFor(t, h, m, 25*time.Second, "an approval request", func() bool {
		ap, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "pending", 10)
		if len(ap) == 1 {
			approvalID = string(ap[0].ID)
			return true
		}
		return false
	})
	r := h.Deps.Volition.Direct(h.Deps.Events, "operator")
	if _, err := h.Deps.Volition.CreateCommitment(ctx, r, volition.NewCommitment{MindID: m.ID, Description: "never restart atlas services", Kind: volition.KindOperatorInstruction, Polarity: "refrain",
		Matcher: volition.Matcher{Capabilities: []string{"service.restart"}, Targets: []string{"atlas-*"}}, CreatedBy: "admin"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, h, m, 15*time.Second, "the intention to be abandoned", func() bool {
		its, _ := h.Deps.Volition.Intentions(ctx, nil, m.ID, []volition.IntentionStatus{volition.IntentionAbandoned}, 10)
		for _, x := range its {
			if x.Proposal != nil && x.Proposal.Capability == "service.restart" {
				return true
			}
		}
		return false
	})
	a, _ := h.Deps.Capabilities.Approvals(ctx, m.ID, "", 10)
	for _, x := range a {
		if string(x.ID) == approvalID && x.Status != "withdrawn" {
			t.Fatalf("approval request should be withdrawn, is %s", x.Status)
		}
	}
	if host.count() != 0 {
		t.Fatal("restart ran")
	}
}
