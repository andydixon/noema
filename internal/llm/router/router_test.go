package router

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/ids"
)

func TestRouterOverrideBudgetRecordingAndReplay(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	st := NewStore(pool)
	sec, _ := secrets.New("")
	r := NewRouter(st, sec, slog.New(slog.DiscardHandler), nil, 2)
	m, _ := minds.NewStore(pool).Create(ctx, minds.CreateParams{Slug: "r"})

	if _, err := r.Complete(ctx, llm.Request{Role: llm.RoleJudge, Messages: []llm.Message{{Role: "user", Content: "x"}}}); !errors.Is(err, llm.ErrNoModel) {
		t.Fatalf("expected no model, got %v", err)
	}
	fake := &llm.Fake{Script: []string{"first", "second"}}
	r.Override = map[llm.Role]llm.LanguageModel{llm.RoleJudge: fake}
	resp, err := r.Complete(ctx, llm.Request{Role: llm.RoleJudge, MindID: m.ID, Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil || resp.Text != "first" || resp.CallID.IsZero() {
		t.Fatalf("override call: %v %+v", err, resp)
	}
	calls, _ := st.ListCalls(ctx, CallFilter{MindID: m.ID})
	if len(calls) != 1 || calls[0].Role != llm.RoleJudge || calls[0].PromptTokens == 0 {
		t.Fatalf("call not recorded: %+v", calls)
	}
	// Replay serves the recording for an identical request without calling the model.
	r.Replay = true
	resp2, err := r.Complete(ctx, llm.Request{Role: llm.RoleJudge, MindID: m.ID, Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil || !resp2.Replayed || resp2.Text != "first" || len(fake.Calls) != 1 {
		t.Fatalf("replay wrong: %v %+v calls=%d", err, resp2, len(fake.Calls))
	}
	r.Replay = false
	// Budget: exhaust and expect refusal.
	r.DailyTokenBudget = 1
	// Override path bypasses configured routing; budget applies to configured models, so configure one.
	r.Override = nil
	p, err := st.CreateProvider(ctx, ProviderInput{Name: "local", Kind: llm.KindOllama, Endpoint: "http://127.0.0.1:1", AllowPrivate: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	mdl, _ := st.CreateModel(ctx, ModelInput{ProviderID: p.ID, Name: "m", Kind: "chat", Enabled: true})
	st.SetRole(ctx, RoleAssignment{Role: llm.RoleJudge, ModelID: &mdl.ID}) //nolint:errcheck
	r.Invalidate()
	if _, err := r.Complete(ctx, llm.Request{Role: llm.RoleJudge, Messages: []llm.Message{{Role: "user", Content: "x"}}}); !errors.Is(err, llm.ErrBudgetExceeded) {
		t.Fatalf("expected budget error, got %v", err)
	}
	r.DailyTokenBudget = 0
	// Provider unreachable → provider error recorded with error text.
	_, err = r.Complete(ctx, llm.Request{Role: llm.RoleJudge, Messages: []llm.Message{{Role: "user", Content: "x"}}, Timeout: time.Second})
	if err == nil {
		t.Fatal("unreachable provider should fail")
	}
	errs, _ := st.ListCalls(ctx, CallFilter{ErrorsOnly: true})
	if len(errs) != 1 {
		t.Fatalf("error not recorded: %d", len(errs))
	}
	// Provider input validation: credentials in headers are rejected; key refs kept on update.
	if _, err := st.CreateProvider(ctx, ProviderInput{Name: "bad", Kind: llm.KindOpenAI, Headers: map[string]string{"Authorization": "x"}}); err == nil {
		t.Fatal("credential header accepted")
	}
	p2, _ := st.UpdateProvider(ctx, p.ID, ProviderInput{Name: "local", Kind: llm.KindOllama, Endpoint: p.Endpoint, APIKeyRef: "env:X", Enabled: true, AllowPrivate: true})
	p3, _ := st.UpdateProvider(ctx, p.ID, ProviderInput{Name: "local", Kind: llm.KindOllama, Endpoint: p.Endpoint, APIKeyRef: "", Enabled: true, AllowPrivate: true})
	if p2.APIKeyRef != "env:X" || p3.APIKeyRef != "env:X" {
		t.Fatalf("key reference handling wrong: %q %q", p2.APIKeyRef, p3.APIKeyRef)
	}
	if h := Hash("m", llm.Request{Role: "a"}); h != Hash("m", llm.Request{Role: "a"}) || h == Hash("m2", llm.Request{Role: "a"}) {
		t.Fatal("hash not stable/discriminating")
	}
	_ = ids.Nil
}

// TestMindRoleOverridesFailClosed: a mind's own routing wins over the daemon
// default for that mind only, and an override naming a missing or disabled
// model errors rather than falling back, so a comparison between providers
// cannot silently become a comparison of one provider with itself.
func TestMindRoleOverridesFailClosed(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	st := NewStore(pool)
	sec, _ := secrets.New("")
	r := NewRouter(st, sec, slog.New(slog.DiscardHandler), nil, 2)
	ms := minds.NewStore(pool)
	a, _ := ms.Create(ctx, minds.CreateParams{Slug: "fork-a"})
	b, _ := ms.Create(ctx, minds.CreateParams{Slug: "fork-b"})
	p, err := st.CreateProvider(ctx, ProviderInput{Name: "local", Kind: llm.KindOllama, Endpoint: "http://127.0.0.1:1", AllowPrivate: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	def, _ := st.CreateModel(ctx, ModelInput{ProviderID: p.ID, Name: "default-model", Kind: "chat", Enabled: true})
	alt, _ := st.CreateModel(ctx, ModelInput{ProviderID: p.ID, Name: "alt-model", Kind: "chat", Enabled: true})
	off, _ := st.CreateModel(ctx, ModelInput{ProviderID: p.ID, Name: "disabled-model", Kind: "chat", Enabled: false})
	st.SetRole(ctx, RoleAssignment{Role: llm.RoleJudge, ModelID: &def.ID}) //nolint:errcheck
	r.Invalidate()
	r.MindRoles = func(_ context.Context, mind ids.ID) map[llm.Role]ids.ID {
		switch mind {
		case b.ID:
			return map[llm.Role]ids.ID{llm.RoleJudge: alt.ID}
		}
		return nil
	}
	c, _ := r.load(ctx)
	if cands, _ := r.candidatesFor(ctx, c, llm.RoleJudge, a.ID); len(cands) != 1 || cands[0].Name != "default-model" {
		t.Fatalf("mind without override should use the default: %+v", cands)
	}
	if cands, _ := r.candidatesFor(ctx, c, llm.RoleJudge, b.ID); len(cands) != 1 || cands[0].Name != "alt-model" {
		t.Fatalf("mind override not honoured: %+v", cands)
	}
	r.MindRoles = func(context.Context, ids.ID) map[llm.Role]ids.ID { return map[llm.Role]ids.ID{llm.RoleJudge: off.ID} }
	if _, err := r.candidatesFor(ctx, c, llm.RoleJudge, b.ID); !errors.Is(err, llm.ErrNoModel) {
		t.Fatalf("disabled override must fail closed, got %v", err)
	}
	r.MindRoles = func(context.Context, ids.ID) map[llm.Role]ids.ID {
		return map[llm.Role]ids.ID{llm.RoleJudge: ids.New()}
	}
	if _, err := r.candidatesFor(ctx, c, llm.RoleJudge, b.ID); !errors.Is(err, llm.ErrNoModel) {
		t.Fatalf("unknown override must fail closed, got %v", err)
	}
	// The configuration layer drops malformed overrides rather than storing them.
	cfg := minds.DefaultConfig()
	cfg.LLMRoles = map[string]string{"judge": string(alt.ID), "bogus_role": string(alt.ID), "respond": "not-an-id"}
	cfg.Normalise()
	if len(cfg.LLMRoles) != 1 || cfg.LLMRoles["judge"] != string(alt.ID) {
		t.Fatalf("normalise: %+v", cfg.LLMRoles)
	}
}
