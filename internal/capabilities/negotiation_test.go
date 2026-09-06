package capabilities

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/andydixon/noema/pkg/ids"
)

func intp(n int) *int { return &n }

func TestRegistryFreezes(t *testing.T) {
	r := NewRegistry()
	r.Add(Capability{Name: "a"})
	r.Freeze()
	defer func() {
		if recover() == nil {
			t.Fatal("adding to a frozen registry must panic")
		}
	}()
	r.Add(Capability{Name: "shell"})
}

func TestUnknownAccessIsWrite(t *testing.T) {
	r := NewRegistry().Add(Capability{Name: "x"})
	if c, _ := r.Get("x"); c.Access != AccessWrite || c.Timeout != 60*time.Second {
		t.Fatalf("defaults must fail closed: %+v", c)
	}
}

func TestRequireApprovalOnlyTightens(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	// noema.note is ALLOW by default; a commitment-driven invocation must still ask.
	out, err := s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "noema.note", Params: map[string]any{"text": "x"}, RequireApproval: true, Origin: "plan_step:t"})
	if err != nil || out.Status != "pending" || !out.Decision.Tightened {
		t.Fatalf("require approval did not tighten: %v %+v", err, out)
	}
	// It never loosens: DENY stays DENY and locked stays locked.
	if _, err := s.SetPolicy(ctx, Policy{MindID: m.ID, Capability: "test.echo", Effect: Deny}); err != nil {
		t.Fatal(err)
	}
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Params: map[string]any{"text": "x"}, RequireApproval: true})
	if out.Status != "denied" {
		t.Fatalf("deny loosened: %+v", out)
	}
	// A scoped grant does not satisfy a per-request approval requirement.
	if _, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "http.fetch", Duration: time.Hour, By: "andy"}); err != nil {
		t.Fatal(err)
	}
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "http.fetch", Target: "https://example.org", Params: map[string]any{"url": "https://example.org"}, RequireApproval: true})
	if out.Status != "pending" {
		t.Fatalf("grant bypassed a required approval: %+v", out)
	}
}

func TestScopedGrantScopeCountAndExpiry(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	now := time.Now()
	s.Now = func() time.Time { return now }
	// Read-only grants cannot cover a writing capability.
	if _, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "send_email", Access: AccessRead, Duration: time.Hour, By: "andy"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("read grant on a write capability accepted: %v", err)
	}
	if _, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "sudo_shell", Duration: time.Hour, By: "andy"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("locked capability granted")
	}
	if _, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "test.echo", Duration: time.Hour, By: "planning"}); !errors.Is(err, ErrReservedDecider) {
		t.Fatal("a component granted authority")
	}
	g, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "test.echo", TargetPattern: "api-*", ParamConstraints: map[string]string{"text": "ping*"}, Duration: 30 * time.Minute, MaxOperations: intp(2), By: "andy"})
	if err != nil {
		t.Fatal(err)
	}
	try := func(target, text string) string {
		out, err := s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Target: target, Params: map[string]any{"text": text}})
		if err != nil {
			t.Fatal(err)
		}
		return out.Status
	}
	if st := try("db-1", "ping"); st != "pending" {
		t.Fatalf("target restriction ignored: %s", st)
	}
	if st := try("api-1", "rm"); st != "pending" {
		t.Fatalf("parameter restriction ignored: %s", st)
	}
	if st := try("api-1", "ping 1"); st != "executed" {
		t.Fatalf("grant not applied: %s", st)
	}
	if st := try("api-2", "ping 2"); st != "executed" {
		t.Fatalf("second operation refused: %s", st)
	}
	if st := try("api-3", "ping 3"); st != "pending" {
		t.Fatalf("operation cap ignored: %s", st)
	}
	// Used up: the sweep refreshes the self-model once.
	var refreshed []ids.ID
	s.OnSelfModel = func(_ context.Context, mind ids.ID) { refreshed = append(refreshed, mind) }
	if got, _ := s.SweepGrants(ctx); len(got) != 1 || got[0] != m.ID {
		t.Fatalf("sweep: %v", got)
	}
	if got, _ := s.SweepGrants(ctx); len(got) != 0 {
		t.Fatal("sweep notified twice")
	}
	// Expiry.
	g2, _ := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "test.echo", Duration: time.Minute, By: "andy"})
	if st := try("any", "x"); st != "executed" {
		t.Fatalf("second grant: %s", st)
	}
	now = now.Add(2 * time.Minute)
	if st := try("any", "x"); st != "pending" {
		t.Fatalf("expired grant used: %s", st)
	}
	if got, _ := s.SweepGrants(ctx); len(got) != 1 {
		t.Fatalf("expiry not swept: %v", got)
	}
	live, _ := s.ScopedGrants(ctx, m.ID, true)
	if len(live) != 0 || g.ID == g2.ID {
		t.Fatalf("live grants: %+v", live)
	}
}

func TestGrantOperationsAreNotOverspent(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	if _, err := s.Grant(ctx, GrantInput{MindID: m.ID, Capability: "test.echo", Duration: time.Hour, MaxOperations: intp(1), By: "andy"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	executed := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _ := s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Params: map[string]any{"text": "x"}})
			if out.Status == "executed" {
				mu.Lock()
				executed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if executed != 1 {
		t.Fatalf("one-operation grant executed %d times", executed)
	}
}

func TestCapabilityRequestLifecycle(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	s.MaxPendingRequests = 2
	base := CapabilityRequest{MindID: m.ID, Capability: "test.echo", TargetPattern: "api-*", DurationSeconds: 1800, MaxOperations: intp(20),
		Reason: "metrics cannot distinguish deployment failure from downstream failure", Risk: "logs may contain personal identifiers", Mitigation: "PII redaction", Fallback: "ask the operator to query manually",
		Evidence: []EvidenceRef{{Kind: "event", ID: string(ids.New()), Note: "deployment at 07:12"}}, IntentionID: ids.New()}
	if _, err := s.RequestCapability(ctx, CapabilityRequest{MindID: m.ID, Capability: "sudo_shell", DurationSeconds: 60, Reason: "x"}); !errors.Is(err, ErrNotRequestable) {
		t.Fatalf("locked capability requestable: %v", err)
	}
	if _, err := s.RequestCapability(ctx, CapabilityRequest{MindID: m.ID, Capability: "invented.tool", DurationSeconds: 60, Reason: "x"}); !errors.Is(err, ErrUnknown) {
		t.Fatal("unknown capability requestable")
	}
	if _, err := s.RequestCapability(WithForbidden(ctx, "dreaming"), base); !errors.Is(err, ErrForbiddenContext) {
		t.Fatal("dream requested authority")
	}
	r1, err := s.RequestCapability(ctx, base)
	if err != nil || r1.Status != "pending" || r1.BatchKey != "intention:"+string(base.IntentionID) {
		t.Fatalf("request: %v %+v", err, r1)
	}
	r2, _ := s.RequestCapability(ctx, base)
	if r2.BatchKey != r1.BatchKey {
		t.Fatal("requests from one intention should batch")
	}
	if _, err := s.RequestCapability(ctx, base); !errors.Is(err, ErrAttentionBudget) {
		t.Fatalf("attention budget not enforced: %v", err)
	}
	// A component cannot decide; an operator can narrow.
	if _, err := s.DecideCapabilityRequest(ctx, r1.ID, "mind:"+string(m.ID), true, "", Narrowing{}); !errors.Is(err, ErrReservedDecider) {
		t.Fatal("mind approved its own request")
	}
	got, err := s.DecideCapabilityRequest(ctx, r1.ID, "andy", true, "ok for the incident", Narrowing{DurationSeconds: 600, MaxOperations: intp(5)})
	if err != nil || got.Status != "granted" || got.GrantID.IsZero() {
		t.Fatalf("grant: %v %+v", err, got)
	}
	gs, _ := s.ScopedGrants(ctx, m.ID, true)
	if len(gs) != 1 || *gs[0].MaxOperations != 5 || time.Until(gs[0].ExpiresAt) > 11*time.Minute || gs[0].RequestID != r1.ID {
		t.Fatalf("grant not narrowed: %+v", gs)
	}
	if _, err := s.DecideCapabilityRequest(ctx, r1.ID, "andy", true, "", Narrowing{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("request granted twice")
	}
	if _, err := s.DecideCapabilityRequest(ctx, r2.ID, "andy", false, "no", Narrowing{}); err != nil {
		t.Fatal(err)
	}
	r3, _ := s.RequestCapability(ctx, base)
	if r3.History["granted"] != 1 || r3.History["denied"] != 1 {
		t.Fatalf("explicit history: %+v", r3.History)
	}
	if err := s.WithdrawCapabilityRequest(ctx, m.ID, r3.ID, "no longer needed"); err != nil {
		t.Fatal(err)
	}
}
