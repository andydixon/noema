package capabilities

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

func TestEvaluateSpecificityAndLocks(t *testing.T) {
	mind, other := ids.New(), ids.New()
	c := Capability{Name: "restart_service", DefaultEffect: Ask}
	pols := []Policy{
		{ID: ids.New(), Capability: "*", TargetPattern: "*", ActionKind: "*", Effect: Deny},
		{ID: ids.New(), Capability: "restart_service", TargetPattern: "*", ActionKind: "*", Effect: Ask},
		{ID: ids.New(), MindID: mind, Capability: "restart_service", TargetPattern: "web-*", ActionKind: "*", Effect: Allow},
		{ID: ids.New(), MindID: other, Capability: "restart_service", TargetPattern: "*", ActionKind: "*", Effect: Allow},
	}
	now := time.Now()
	if d := Evaluate(c, pols, mind, "web-01", "*", now); d.Effect != Allow {
		t.Fatalf("most specific mind rule should allow: %+v", d)
	}
	if d := Evaluate(c, pols, mind, "db-01", "*", now); d.Effect != Ask {
		t.Fatalf("capability rule should ask: %+v", d)
	}
	if d := Evaluate(Capability{Name: "send_email", DefaultEffect: Ask}, pols, mind, "x", "*", now); d.Effect != Deny {
		t.Fatalf("global wildcard deny should apply: %+v", d)
	}
	// Tie: two rules with equal specificity, stricter wins.
	tie := []Policy{{ID: ids.New(), Capability: "restart_service", Effect: Allow, TargetPattern: "*", ActionKind: "*"}, {ID: ids.New(), Capability: "restart_service", Effect: Deny, TargetPattern: "*", ActionKind: "*"}}
	if d := Evaluate(c, tie, mind, "x", "*", now); d.Effect != Deny {
		t.Fatalf("tie should deny: %+v", d)
	}
	if d := Evaluate(Capability{Name: "sudo_shell", Locked: true}, []Policy{{Capability: "sudo_shell", Effect: Allow, TargetPattern: "*", ActionKind: "*"}}, mind, "x", "*", now); d.Effect != Deny || !d.Locked {
		t.Fatal("locked capability must deny regardless of policy")
	}
	// Time window.
	from, to := 9, 17
	win := []Policy{{ID: ids.New(), Capability: "restart_service", TargetPattern: "*", ActionKind: "*", Effect: Allow, HoursFrom: &from, HoursTo: &to}}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local)
	off := time.Date(2026, 1, 1, 22, 0, 0, 0, time.Local)
	if Evaluate(c, win, mind, "x", "*", at).Effect != Allow || Evaluate(c, win, mind, "x", "*", off).Effect != Ask {
		t.Fatal("time window wrong")
	}
}

func TestValidateParamsAndHash(t *testing.T) {
	c := Capability{Name: "f", Params: map[string]Param{"url": {Type: "url", Required: true}, "n": {Type: "int"}, "p": {Type: "path"}}}
	if _, err := c.ValidateParams(map[string]any{"url": "ftp://x"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("non-http url accepted")
	}
	if _, err := c.ValidateParams(map[string]any{"url": "https://x", "extra": 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown param accepted")
	}
	if _, err := c.ValidateParams(map[string]any{"url": "https://x", "p": "../etc/passwd"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("traversal accepted")
	}
	p, err := c.ValidateParams(map[string]any{"url": "https://x", "n": float64(3)})
	if err != nil || p["n"] != 3 {
		t.Fatalf("validate: %v %v", err, p)
	}
	m := ids.New()
	if Hash(m, "f", "t", p) != Hash(m, "f", "t", map[string]any{"url": "https://x", "n": 3}) || Hash(m, "f", "t", p) == Hash(m, "f", "t2", p) {
		t.Fatal("hash not stable/discriminating")
	}
}

func setup(t *testing.T) (*Service, minds.Mind, *evstore.Store) {
	t.Helper()
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "cap"})
	es := evstore.NewStore(pool, evstore.NewBus(8), nil)
	reg := NewRegistry()
	for _, c := range Builtins() {
		reg.Add(c)
	}
	calls := 0
	reg.Add(Capability{Name: "test.echo", Description: "echo", Risk: RiskLow, TargetKind: "none", Params: map[string]Param{"text": {Type: "string", Required: true}}, DefaultEffect: Ask,
		Executor: func(_ context.Context, inv Invocation) (Result, error) {
			calls++
			return Result{Summary: "echoed", Output: inv.Params["text"].(string)}, nil
		}})
	reg.Add(Capability{Name: "test.boom", Risk: RiskLow, DefaultEffect: Allow, Executor: func(context.Context, Invocation) (Result, error) { panic("bad tool") }})
	return &Service{Registry: reg, DB: pool, Events: es, Log: slog.New(slog.DiscardHandler)}, m, es
}

func TestInvokePaths(t *testing.T) {
	s, m, es := setup(t)
	ctx := context.Background()
	// Default ALLOW executes and its output becomes a tool-trust observation.
	out, err := s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "noema.note", Params: map[string]any{"text": "hello"}, Reason: "test"})
	if err != nil || out.Status != "executed" || out.Result == nil {
		t.Fatalf("allow path: %v %+v", err, out)
	}
	// Default ASK creates an approval and executes only after approval.
	out, err = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Target: "t1", Params: map[string]any{"text": "ping"}, Reason: "need it"})
	if err != nil || out.Status != "pending" || out.ApprovalID.IsZero() {
		t.Fatalf("ask path: %v %+v", err, out)
	}
	pend, _ := s.Approvals(ctx, m.ID, "pending", 10)
	if len(pend) != 1 || pend[0].Risk != RiskLow {
		t.Fatalf("pending: %+v", pend)
	}
	res, err := s.Approve(ctx, out.ApprovalID, "andy", "ok", true, nil)
	if err != nil || res.Status != "executed" || res.Result.Output != "ping" {
		t.Fatalf("approve: %v %+v", err, res)
	}
	obs, _ := es.Query(ctx, evstore.Filter{MindID: m.ID, Types: []events.Type{events.Observation}})
	if len(obs) == 0 || fmt.Sprint(obs[0].Metadata["trust"]) != string(events.TrustTool) {
		t.Fatalf("tool output should be a tool-trust observation: %+v", obs)
	}
	// Session grant now allows the same target without asking; a different target still asks.
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Target: "t1", Params: map[string]any{"text": "again"}})
	if out.Status != "executed" {
		t.Fatalf("session grant not applied: %+v", out)
	}
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Target: "t2", Params: map[string]any{"text": "x"}})
	if out.Status != "pending" {
		t.Fatalf("other target should still ask: %+v", out)
	}
	// Deny with note; double decision refused.
	if _, err := s.Deny(ctx, out.ApprovalID, "andy", "not now"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(ctx, out.ApprovalID, "andy", "", false, nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("denied request approved")
	}
	// Policy DENY, locked capability, and forbidden context.
	if _, err := s.SetPolicy(ctx, Policy{MindID: m.ID, Capability: "test.echo", Effect: Deny, Reason: "no echo here"}); err != nil {
		t.Fatal(err)
	}
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.echo", Target: "t1", Params: map[string]any{"text": "y"}})
	if out.Status != "denied" {
		t.Fatalf("policy deny not applied: %+v", out)
	}
	if _, err := s.SetPolicy(ctx, Policy{Capability: "sudo_shell", Effect: Allow}); !errors.Is(err, ErrInvalid) {
		t.Fatal("locked capability relaxed")
	}
	out, _ = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "sudo_shell", Params: map[string]any{"command": "rm -rf /"}})
	if out.Status != "denied" || !out.Decision.Locked {
		t.Fatalf("locked not denied: %+v", out)
	}
	if _, err := s.Invoke(WithForbidden(ctx, "dreaming"), Invocation{MindID: m.ID, Capability: "noema.note", Params: map[string]any{"text": "x"}}); !errors.Is(err, ErrForbiddenContext) {
		t.Fatal("dreaming context allowed an action")
	}
	// A panicking executor is a failed execution, not a crash.
	out, err = s.Invoke(ctx, Invocation{MindID: m.ID, Capability: "test.boom"})
	if err != nil || out.Status != "failed" {
		t.Fatalf("panic handling: %v %+v", err, out)
	}
	// Decisions are append-only.
	if _, err := s.DB.Exec(ctx, `DELETE FROM approval_decisions`); err == nil {
		t.Fatal("decisions must be append-only")
	}
	inv, _ := s.Inventory(ctx, m.ID)
	var sawLocked bool
	for _, i := range inv {
		if i.Capability.Name == "sudo_shell" && i.Effect == Deny {
			sawLocked = true
		}
	}
	if !sawLocked {
		t.Fatal("inventory missing locked deny")
	}
	execs, _ := s.Executions(ctx, m.ID, 10)
	if len(execs) < 3 {
		t.Fatalf("executions not recorded: %d", len(execs))
	}
}
