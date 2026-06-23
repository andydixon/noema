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
