// Package m1 is the acceptance test for the first usable milestone: start,
// log in, create a mind, shape its personality, configure a model, chat,
// watch events, inspect working memory, the workspace, memories, beliefs and
// provenance, restart, and continue the same mind.
package m1

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/andydixon/noema/internal/api"
	"github.com/andydixon/noema/internal/app"
	"github.com/andydixon/noema/internal/audit"
	"github.com/andydixon/noema/internal/auth"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/cognition"
	"github.com/andydixon/noema/internal/config"
	"github.com/andydixon/noema/internal/consolidation"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/dreaming"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/goals"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/integrations"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/narrative"
	"github.com/andydixon/noema/internal/notifications"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/predictions"
	"github.com/andydixon/noema/internal/reflection"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/internal/snapshots"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/migrations"
	"github.com/andydixon/noema/pkg/events"
)

type harness struct {
	t     *testing.T
	deps  app.Deps
	group *scheduler.Group
	srv   *httptest.Server
	token string
}

// start assembles the daemon the way cmd/noemad does, with a fake model.
func start(t *testing.T, deps app.Deps, perceive, respond llm.LanguageModel) *harness {
	t.Helper()
	ctx := context.Background()
	group := scheduler.NewGroup(ctx, deps.Log)
	registry := brain.NewRegistry().AddProcess(cognition.Attend{}).AddProcess(cognition.Retrieve{}).AddProcess(cognition.Associate{}).AddProcess(cognition.Perceive{}).
		AddProcess(cognition.GoalMonitor{}).AddProcess(cognition.Curiosity{}).AddProcess(cognition.Metacognition{}).AddProcess(cognition.Act{}).
		AddHook(cognition.MotivationHook{Store: deps.Motivation}).AddHook(cognition.NarrativeHook{Store: deps.Narrative}).AddHook(cognition.NotifyHook{Store: deps.Notifications})
	cognition.Handlers{Memory: deps.Memory, Beliefs: deps.Beliefs, Conversations: deps.Conversations, Graph: deps.Graph}.Register(registry)
	deps.LLM.Override = map[llm.Role]llm.LanguageModel{llm.RolePerceive: perceive, llm.RoleRespond: respond}
	deps.Brain = brain.NewManager(brain.Config{DB: deps.DB, Events: deps.Events, Minds: deps.Minds, Registry: registry, Pool: scheduler.NewPool(4), Log: deps.Log, Metrics: deps.Metrics,
		Services: brain.Services{Events: deps.Events, Memory: deps.Memory, Beliefs: deps.Beliefs, LLM: deps.LLM, Prompts: deps.Prompts, Goals: deps.Goals, Motivation: deps.Motivation, Predictions: deps.Predictions, Capabilities: deps.Capabilities}}, group)
	if err := deps.Brain.Start(ctx); err != nil {
		t.Fatal(err)
	}
	s := api.New(deps, nil)
	h := &harness{t: t, deps: deps, group: group, srv: httptest.NewServer(s.Handler())}
	return h
}

func (h *harness) stop() {
	h.srv.Close()
	h.group.Stop()
}

func (h *harness) do(method, path string, body any, out any) int {
	h.t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(res.Body).Decode(out) //nolint:errcheck
	}
	return res.StatusCode
}

func TestFirstMilestone(t *testing.T) {
	pool := storage.TestPool(t, migrations.FS)
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	cfg := config.Defaults()
	cfg.DatabaseURL = "postgres://test"
	sec, _ := secrets.New("")
	llmStore := router.NewStore(pool)
	deps := app.Deps{Config: cfg, Log: log, DB: pool, Auth: auth.NewService(pool, time.Hour, 2*time.Hour), Audit: audit.New(pool, log), Metrics: observability.NewMetrics(),
		Minds: minds.NewStore(pool), Events: evstore.NewStore(pool, evstore.NewBus(64), nil), Memory: memory.NewStore(ctx, pool, nil), Beliefs: beliefs.NewStore(pool),
		Conversations: conversation.NewStore(pool), Goals: goals.NewStore(pool), Motivation: motivation.NewStore(pool), Predictions: predictions.NewStore(pool), Secrets: sec, LLMStore: llmStore, LLM: router.NewRouter(llmStore, sec, log, nil, 2), Prompts: prompts.NewStore(pool), Migrations: migrations.FS, Started: time.Now()}
	deps.Resolver = &predictions.Resolver{Store: deps.Predictions, Events: deps.Events, Beliefs: deps.Beliefs, Log: log}
	deps.Graph = graph.NewStore(pool)
	capReg := capabilities.NewRegistry()
	for _, c := range capabilities.Builtins() {
		capReg.Add(c)
	}
	deps.Capabilities = &capabilities.Service{Registry: capReg, DB: pool, Events: deps.Events, Log: log}
	deps.Integrations = &integrations.Manager{Store: integrations.NewStore(pool), Events: deps.Events, Graph: deps.Graph, Log: log, Pollers: map[integrations.Kind]integrations.Poller{}}
	deps.Dreaming = &dreaming.Service{DB: pool, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, Log: log}
	deps.Snapshots = &snapshots.Store{DB: pool, Events: deps.Events, Minds: deps.Minds}
	deps.Narrative = narrative.NewStore(pool)
	deps.Notifications = notifications.NewStore(pool)
	deps.Consolidation = &consolidation.Service{DB: pool, Memory: deps.Memory, Beliefs: deps.Beliefs, Events: deps.Events, Minds: deps.Minds, Log: log}
	deps.Reflection = &reflection.Service{DB: pool, Events: deps.Events, Beliefs: deps.Beliefs, Goals: deps.Goals, Memory: deps.Memory, Predictions: deps.Predictions, Log: log}
	if _, err := deps.Prompts.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	// 1–2. Start and "log in": an administrator with an API token.
	u, err := deps.Auth.CreateUser(ctx, "admin", "administrator-pass", auth.RoleAdministrator)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := deps.Auth.CreateAPIToken(ctx, u.ID, "m1", nil)
	perceive := &llm.Fake{ModelName: "fake-perceive", Script: []string{`{"intent":"statement","summary":"claims a disk failure","entities":[{"name":"atlas","kind":"system"}],"claims":[{"proposition":"atlas has a failing disk","certainty":"asserted"}],"questions":[],"urgency":0.6,"sentiment":-0.3}`}}
	respond := &llm.Fake{ModelName: "fake-respond", Script: []string{"Noted: I now hold that atlas has a failing disk, on your word alone.", "Atlas: I believe its disk is failing, tentatively.", "Still here after the restart; atlas' disk is still my concern."}}
	h := start(t, deps, perceive, respond)
	h.token = token

	// 3. Create a mind.
	var mind minds.Mind
	if code := h.do("POST", "/api/v1/minds", map[string]any{"slug": "m1", "display_name": "M1"}, &mind); code != 201 {
		t.Fatalf("create mind: %d", code)
	}
	// 4. Configure personality.
	p := mind.Personality
	p.Identity.Role = "acceptance tester"
	p.Traits["scepticism"] = 0.9
	var ver minds.Version
	if code := h.do("PUT", "/api/v1/minds/m1/personality", map[string]any{"personality": p, "change_note": "m1"}, &ver); code != 200 || ver.Version != 2 {
		t.Fatalf("personality: %d %+v", code, ver)
	}
	// 5. Configure an LLM: a provider and model exist in configuration; calls are served by the fake via role override.
	var prov router.Provider
	if code := h.do("POST", "/api/v1/llm/providers", map[string]any{"name": "fake", "kind": "ollama", "endpoint": "http://127.0.0.1:1", "allow_private": true}, &prov); code != 201 {
		t.Fatalf("provider: %d", code)
	}
	// 6. Chat: a statement, then a question.
	var ask struct {
		Reply *conversation.Message `json:"reply"`
		Note  string                `json:"note"`
	}
	if code := h.do("POST", "/api/v1/minds/m1/ask", map[string]any{"text": "atlas has a failing disk.", "wait_seconds": 20}, &ask); code != 200 || ask.Reply == nil {
		t.Fatalf("ask 1: %d %+v", code, ask)
	}
	if !strings.Contains(ask.Reply.Content, "Noted") || ask.Reply.LLMCallID.IsZero() || ask.Reply.EventID.IsZero() {
		t.Fatalf("reply 1 not from model with provenance: %+v", ask.Reply)
	}
	var intent cognition.Intent
	json.Unmarshal(ask.Reply.Intent, &intent) //nolint:errcheck
	if intent.Kind != "acknowledge" || len(intent.Beliefs) != 1 {
		t.Fatalf("intent 1 wrong: %+v", intent)
	}
	if code := h.do("POST", "/api/v1/minds/m1/ask", map[string]any{"text": "What do you believe about atlas?", "wait_seconds": 20}, &ask); code != 200 || ask.Reply == nil {
		t.Fatalf("ask 2: %d %+v", code, ask)
	}
	json.Unmarshal(ask.Reply.Intent, &intent) //nolint:errcheck
	if intent.Kind != "introspect" || len(intent.Beliefs) == 0 || !strings.Contains(intent.Beliefs[0].Proposition, "failing disk") {
		t.Fatalf("intent 2 not grounded in the recorded belief: %+v", intent)
	}
	// 7. Cognitive events appeared, with the pipeline shape.
	var evs struct{ Items []events.Event }
	h.do("GET", "/api/v1/minds/m1/events?limit=200", nil, &evs)
	types := map[events.Type]int{}
	for _, e := range evs.Items {
		types[e.Type]++
	}
	for _, want := range []events.Type{events.Observation, events.Perception, events.ThoughtCandidate, events.ThoughtSelected, events.WorkspaceBroadcast, events.MemoryCreated, events.BeliefCreated, events.LLMResponse, events.ResponseGenerated} {
		if types[want] == 0 {
			t.Fatalf("missing event type %s in %v", want, types)
		}
	}
	// 8–9. Working memory and workspace are inspectable.
	var ws api.WorkspaceView
	h.do("GET", "/api/v1/minds/m1/workspace", nil, &ws)
	if !ws.Running || ws.State.Cycle < 2 || len(ws.State.WorkingMemory) == 0 || len(ws.Candidates) == 0 {
		t.Fatalf("workspace: %+v", ws.State)
	}
	// 10. Episodic memories.
	var mems struct{ Items []memory.Memory }
	h.do("GET", "/api/v1/minds/m1/memories?kind=episodic", nil, &mems)
	if len(mems.Items) < 2 {
		t.Fatalf("expected episodic memories, got %d", len(mems.Items))
	}
	// 11–12. Beliefs with provenance back to the message.
	var bs struct{ Items []beliefs.Belief }
	h.do("GET", "/api/v1/minds/m1/beliefs", nil, &bs)
	if len(bs.Items) != 1 || bs.Items[0].Epistemic != "claim" {
		t.Fatalf("beliefs: %+v", bs.Items)
	}
	var detail api.BeliefDetail
	h.do("GET", "/api/v1/minds/m1/beliefs/"+string(bs.Items[0].ID), nil, &detail)
	if len(detail.Evidence) != 1 || detail.Evidence[0].Kind != "event" || detail.Breakdown.SourcesFor != 1 {
		t.Fatalf("provenance: %+v", detail)
	}
	var evCtx api.EventContext
	h.do("GET", "/api/v1/events/"+string(detail.Evidence[0].RefID)+"/context", nil, &evCtx)
	if evCtx.Event.Type != events.Observation || len(evCtx.Children) == 0 {
		t.Fatalf("event context: %+v", evCtx.Event)
	}
	var respCtx api.EventContext
	h.do("GET", "/api/v1/events/"+string(ask.Reply.EventID)+"/context", nil, &respCtx)
	var citesBelief bool
	for _, ref := range respCtx.Event.Evidence {
		if ref.Kind == "belief" {
			citesBelief = true
		}
	}
	if !citesBelief || respCtx.Cause == nil || respCtx.Cause.Type != events.Perception {
		t.Fatalf("reply event lacks provenance: %+v", respCtx.Event)
	}

	// Narrative and notifications are inspectable after cognition ran.
	var narr map[string]any
	h.do("GET", "/api/v1/minds/m1/narrative", nil, &narr)
	if txt, _ := narr["text"].(string); !strings.Contains(txt, "I am") {
		t.Fatalf("narrative missing: %v", narr)
	}
	// 13. Restart everything except the database.
	cycleBefore := ws.State.Cycle
	h.stop()
	h2 := start(t, deps, perceive, respond)
	defer h2.stop()
	h2.token = token
	var ws2 api.WorkspaceView
	h2.do("GET", "/api/v1/minds/m1/workspace", nil, &ws2)
	if !ws2.Running || ws2.State.Cycle != cycleBefore || len(ws2.State.WorkingMemory) != len(ws.State.WorkingMemory) {
		t.Fatalf("state not continued after restart: before %d/%d after %d/%d", cycleBefore, len(ws.State.WorkingMemory), ws2.State.Cycle, len(ws2.State.WorkingMemory))
	}
	// 14. Continue the same mind: it still holds the belief and answers.
	if code := h2.do("POST", "/api/v1/minds/m1/ask", map[string]any{"text": "What do you believe about atlas?", "wait_seconds": 20}, &ask); code != 200 || ask.Reply == nil {
		t.Fatalf("ask after restart: %d %+v", code, ask)
	}
	json.Unmarshal(ask.Reply.Intent, &intent) //nolint:errcheck
	if len(intent.Beliefs) == 0 {
		t.Fatalf("belief lost across restart: %+v", intent)
	}
	var bs2 struct{ Items []beliefs.Belief }
	h2.do("GET", "/api/v1/minds/m1/beliefs", nil, &bs2)
	if len(bs2.Items) != 1 || bs2.Items[0].ID != bs.Items[0].ID {
		t.Fatalf("belief identity changed: %+v", bs2.Items)
	}
	fmt.Println("M1 acceptance: events", len(evs.Items), "cycles", ws2.State.Cycle)
}
