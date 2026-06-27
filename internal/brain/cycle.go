package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/andydixon/noema/internal/attention"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/internal/motivation"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// cursorConsumer names the brain's durable read position.
const cursorConsumer = "brain"

// settle is how old an event must be before the brain reads it, so that a
// concurrently committing transaction with a lower sequence number is not
// skipped forever (see ADR-0002 and PLAN risk R3).
const settle = 250 * time.Millisecond

// processTimeout bounds one process's Observe call.
const processTimeout = 15 * time.Second

// Cycle is the working context of one cognitive cycle, handed to handlers.
type Cycle struct {
	Runtime *Runtime
	State   *State
	Tx      pgx.Tx
	pending []events.New
	emitted []events.Event
}

// Emit queues an event for the cycle's transaction. Correlation is the cycle
// id unless the caller set one.
func (c *Cycle) Emit(n events.New) {
	n.MindID = c.State.Mind.ID
	if n.CorrelationID.IsZero() {
		n.CorrelationID = c.State.CycleID
	}
	if n.OccurredAt.IsZero() {
		n.OccurredAt = c.State.Now
	}
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	n.Metadata["cycle"] = c.State.Cycle
	c.pending = append(c.pending, n)
}

// EmitNow appends one event immediately inside the cycle transaction and
// returns it, for handlers that need the event id for provenance. It is still
// published only after commit.
func (c *Cycle) EmitNow(ctx context.Context, n events.New) (events.Event, error) {
	if err := c.flush(ctx); err != nil {
		return events.Event{}, err
	}
	n.MindID = c.State.Mind.ID
	if n.CorrelationID.IsZero() {
		n.CorrelationID = c.State.CycleID
	}
	if n.OccurredAt.IsZero() {
		n.OccurredAt = c.State.Now
	}
	if n.Metadata == nil {
		n.Metadata = map[string]any{}
	}
	n.Metadata["cycle"] = c.State.Cycle
	evs, err := c.Runtime.events.AppendIn(ctx, c.Tx, n)
	if err != nil {
		return events.Event{}, err
	}
	c.emitted = append(c.emitted, evs...)
	return evs[0], nil
}

// flush writes queued events inside the transaction and returns them.
func (c *Cycle) flush(ctx context.Context) error {
	if len(c.pending) == 0 {
		return nil
	}
	evs, err := c.Runtime.events.AppendIn(ctx, c.Tx, c.pending...)
	if err != nil {
		return err
	}
	c.emitted = append(c.emitted, evs...)
	c.pending = nil
	return nil
}

// Runtime is the cognitive loop of one mind.
type Runtime struct {
	mind     minds.Mind
	db       *pgxpool.Pool
	events   *evstore.Store
	minds    *minds.Store
	registry *Registry
	pool     *scheduler.Pool
	log      *slog.Logger
	metrics  *observability.Metrics
	services Services

	wake     chan struct{}
	commands chan command
	snaps    chan chan workspace.State
	state    workspace.State
	loops    *loopDetector
	budget   *cycleBudget
	now      func() time.Time
}

type command struct {
	kind  string // cycle | pause | resume | reset
	args  map[string]bool
	reply chan error
}

// Config groups runtime dependencies.
type Config struct {
	DB       *pgxpool.Pool
	Events   *evstore.Store
	Minds    *minds.Store
	Registry *Registry
	Pool     *scheduler.Pool
	Log      *slog.Logger
	Metrics  *observability.Metrics
	Services Services
}

// NewRuntime creates a runtime for a mind and loads its persisted state.
func NewRuntime(ctx context.Context, cfg Config, m minds.Mind) (*Runtime, error) {
	r := &Runtime{mind: m, db: cfg.DB, events: cfg.Events, minds: cfg.Minds, registry: cfg.Registry, pool: cfg.Pool,
		log: cfg.Log.With("mind", m.Slug), metrics: cfg.Metrics, services: cfg.Services,
		wake: make(chan struct{}, 1), commands: make(chan command), snaps: make(chan chan workspace.State), now: time.Now}
	r.loops = newLoopDetector(m.Config.LoopDetectWindow, m.Config.LoopDetectThreshold)
	r.budget = newCycleBudget(m.Config.MaxCyclesPerMinute)
	if err := r.loadState(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// persisted wraps the workspace state with runtime bookkeeping.
type persisted struct {
	workspace.State
	LoopRing []string `json:"loop_ring,omitempty"`
}

func (r *Runtime) loadState(ctx context.Context) error {
	var raw []byte
	err := r.db.QueryRow(ctx, `SELECT state FROM workspace_states WHERE mind_id=$1`, r.mind.ID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		r.state = workspace.State{Mode: string(ModeAwake)}
		return nil
	}
	if err != nil {
		return err
	}
	var p persisted
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("corrupt workspace state: %w", err)
	}
	r.state = p.State
	r.loops.restore(p.LoopRing)
	if r.state.Mode == "" {
		r.state.Mode = string(ModeAwake)
	}
	return nil
}

func (r *Runtime) saveState(ctx context.Context, q storage.Querier) error {
	raw, _ := json.Marshal(persisted{State: r.state, LoopRing: r.loops.snapshot()})
	_, err := q.Exec(ctx, `INSERT INTO workspace_states (mind_id, state) VALUES ($1,$2) ON CONFLICT (mind_id) DO UPDATE SET state=excluded.state, updated_at=now()`, r.mind.ID, raw)
	return err
}

// Wake asks the runtime to run a cycle soon (debounced by CycleMinInterval).
func (r *Runtime) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Command sends an operator command and waits for the result.
func (r *Runtime) Command(ctx context.Context, kind string, args map[string]bool) error {
	reply := make(chan error, 1)
	select {
	case r.commands <- command{kind: kind, args: args, reply: reply}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot returns a copy of the current workspace state, taken on the loop
// goroutine so no lock is needed.
func (r *Runtime) Snapshot(ctx context.Context) (workspace.State, error) {
	reply := make(chan workspace.State, 1)
	select {
	case r.snaps <- reply:
	case <-ctx.Done():
		return workspace.State{}, ctx.Err()
	}
	select {
	case st := <-reply:
		return st, nil
	case <-ctx.Done():
		return workspace.State{}, ctx.Err()
	}
}

// Run is the loop. It returns when ctx ends.
func (r *Runtime) Run(ctx context.Context) error {
	r.log.Info("mind runtime started", "mode", r.state.Mode, "cycle", r.state.Cycle)
	idle := time.NewTimer(r.mind.Config.IdleCycleInterval.D())
	defer idle.Stop()
	var debounce *time.Timer
	var debounceC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			if debounce == nil {
				debounce = time.NewTimer(r.mind.Config.CycleMinInterval.D() + settle)
				debounceC = debounce.C
			}
		case <-debounceC:
			debounce, debounceC = nil, nil
			r.runCycle(ctx, "stimulus")
			idle.Reset(r.mind.Config.IdleCycleInterval.D())
		case <-idle.C:
			r.runCycle(ctx, "idle")
			idle.Reset(r.mind.Config.IdleCycleInterval.D())
		case cmd := <-r.commands:
			cmd.reply <- r.handleCommand(ctx, cmd)
		case reply := <-r.snaps:
			reply <- r.copyState()
		}
	}
}

func (r *Runtime) handleCommand(ctx context.Context, cmd command) error {
	switch cmd.kind {
	case "cycle":
		if Mode(r.state.Mode) == ModePaused {
			return errors.New("mind is paused")
		}
		r.runCycle(ctx, "operator")
		return nil
	case "pause":
		return r.setMode(ctx, ModePaused, "operator paused cognition")
	case "resume":
		if Mode(r.state.Mode) != ModePaused {
			return nil
		}
		return r.setMode(ctx, ModeAwake, "operator resumed cognition")
	case "reset":
		return r.reset(ctx, cmd.args)
	case "reload":
		m, err := r.minds.Get(ctx, r.mind.ID)
		if err != nil {
			return err
		}
		r.mind = m
		r.loops = newLoopDetector(m.Config.LoopDetectWindow, m.Config.LoopDetectThreshold)
		r.budget = newCycleBudget(m.Config.MaxCyclesPerMinute)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd.kind)
}

func (r *Runtime) setMode(ctx context.Context, to Mode, why string) error {
	from := r.state.Mode
	if from == string(to) {
		return nil
	}
	r.state.Mode = string(to)
	return storage.Tx(ctx, r.db, func(tx pgx.Tx) error {
		if _, err := r.events.AppendIn(ctx, tx, events.New{MindID: r.mind.ID, Type: events.ModeChanged, Source: "brain", Summary: fmt.Sprintf("mode %s → %s: %s", from, to, why),
			Salience: 0.3, Confidence: 1, Metadata: map[string]any{"from": from, "to": to}, Payload: map[string]any{"from": from, "to": to, "reason": why}}); err != nil {
			return err
		}
		return r.saveState(ctx, tx)
	})
}

// reset clears transient state on operator request (spec §3). It never touches
// events, memories or beliefs.
func (r *Runtime) reset(ctx context.Context, what map[string]bool) error {
	var parts []string
	if what["workspace"] {
		r.state.Workspace = nil
		parts = append(parts, "workspace")
	}
	if what["working_memory"] {
		r.state.WorkingMemory = nil
		parts = append(parts, "working memory")
	}
	if what["loops"] {
		r.loops = newLoopDetector(r.mind.Config.LoopDetectWindow, r.mind.Config.LoopDetectThreshold)
		parts = append(parts, "loop detector")
	}
	if len(parts) == 0 {
		return nil
	}
	return storage.Tx(ctx, r.db, func(tx pgx.Tx) error {
		if _, err := r.events.AppendIn(ctx, tx, events.New{MindID: r.mind.ID, Type: events.OperatorAction, Source: "operator", Summary: "Operator reset transient state: " + strings.Join(parts, ", "),
			Salience: 0.5, Confidence: 1, Metadata: map[string]any{"action": "reset"}, Payload: map[string]any{"reset": parts}}); err != nil {
			return err
		}
		return r.saveState(ctx, tx)
	})
}

// runCycle executes one cycle. Errors are logged, never fatal: a broken
// process or malformed event must not stop the mind (spec §46).
func (r *Runtime) runCycle(ctx context.Context, trigger string) {
	if Mode(r.state.Mode) == ModePaused {
		return
	}
	if !r.budget.allow(r.now()) {
		r.log.Warn("cycle rate limit reached; skipping", "trigger", trigger)
		return
	}
	start := r.now()
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var emitted []events.Event
	before := r.copyState()
	err := storage.Tx(cctx, r.db, func(tx pgx.Tx) error {
		var err error
		emitted, err = r.cycle(cctx, tx, trigger, start)
		return err
	})
	if err != nil {
		r.state = before // the transaction rolled back; forget in-memory changes too
		r.log.Error("cycle failed", "trigger", trigger, "err", err)
		return
	}
	r.events.Publish(emitted...) // only after commit, so subscribers never see phantom events
}

func (r *Runtime) cycle(ctx context.Context, tx pgx.Tx, trigger string, start time.Time) ([]events.Event, error) {
	cursor, err := r.events.Cursor(ctx, tx, r.mind.ID, cursorConsumer)
	if err != nil {
		return nil, err
	}
	// Read settled events after the cursor. The query is done on the pool (not
	// the tx) so a long transaction does not hold a snapshot; ordering by seq.
	newEvents, err := r.events.Query(ctx, evstore.Filter{MindID: r.mind.ID, AfterSeq: cursor, Ascending: true, Limit: 200})
	if err != nil {
		return nil, err
	}
	// Recorded-at settle: Query filters by occurred_at, so also drop events
	// recorded too recently (occurred_at may be backdated by integrations).
	filtered := newEvents[:0]
	for _, ev := range newEvents {
		if start.Sub(ev.RecordedAt) >= settle {
			filtered = append(filtered, ev)
		}
	}
	newEvents = filtered
	stimulus := false
	for _, ev := range newEvents {
		if !strings.HasPrefix(ev.Source, "brain") {
			stimulus = true
			break
		}
	}
	if trigger == "stimulus" && !stimulus {
		// Woken by our own output or nothing has settled yet: consume and wait.
		if len(newEvents) > 0 {
			if err := r.events.SetCursor(ctx, tx, r.mind.ID, cursorConsumer, newEvents[len(newEvents)-1].Seq); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	if trigger == "idle" && !stimulus && len(r.state.Workspace) == 0 {
		// Nothing to think about: just move to idle mode.
		if Mode(r.state.Mode) != ModeIdle && r.state.LastStimulus.Add(5*time.Minute).Before(start) {
			r.state.Mode = string(ModeIdle)
		}
		return nil, r.saveState(ctx, tx)
	}

	cycleID := ids.New()
	if stimulus {
		r.state.LastStimulus = start
		if Mode(r.state.Mode) == ModeIdle {
			r.state.Mode = string(ModeAwake)
		}
	}
	sm, _ := r.minds.GetSelfModel(ctx, r.mind.ID)
	st := &State{Mind: r.mind, SelfModel: sm, Mode: Mode(r.state.Mode), Cycle: r.state.Cycle + 1, CycleID: cycleID, Now: start, NewEvents: newEvents, Workspace: r.state, Services: r.services}
	if r.services.Goals != nil {
		st.Goals, _ = r.services.Goals.Active(ctx, r.mind.ID)
	}
	st.Weights = r.mind.Config.AttentionWeights
	if r.services.Motivation != nil {
		if ms, err := r.services.Motivation.Load(ctx, tx, r.mind); err == nil {
			motivation.Decay(&ms, motivation.Baselines(r.mind.Personality), start.Sub(ms.Affect.UpdatedAt))
			st.Motiv = ms
			st.Weights = motivation.Modulate(r.mind.Config.AttentionWeights, ms)
		}
	}
	c := &Cycle{Runtime: r, State: st, Tx: tx}

	// 1. Ask every process, concurrently and bounded.
	cands := r.observe(ctx, st)
	if len(cands) == 0 && len(r.state.Workspace) == 0 {
		// Nothing proposed and nothing held: consume the events without
		// counting a cycle, so self-generated events cannot keep the loop busy.
		if len(newEvents) > 0 {
			if err := r.events.SetCursor(ctx, tx, r.mind.ID, cursorConsumer, newEvents[len(newEvents)-1].Seq); err != nil {
				return nil, err
			}
		}
		return nil, r.saveState(ctx, tx)
	}
	r.state.Cycle++

	// 2. Score deterministically with the cycle's effective weights; suppress loops.
	weights := st.Weights
	var scored []attention.Scored
	byID := map[ids.ID]*Candidate{}
	suppressed := map[ids.ID]string{}
	for i := range cands {
		cand := &cands[i]
		cand.ID = ids.New()
		byID[cand.ID] = cand
		b := attention.Score(cand.Features, weights)
		fp := fingerprint(*cand)
		if r.loops.looping(fp) {
			suppressed[cand.ID] = fp
			b.Total = 0
			if r.loops.shouldWarn(fp) {
				c.Emit(events.New{Type: events.Metacognition, Source: "brain.loop-detector", Summary: "suppressed recurring thought without progress: " + cand.Summary,
					Salience: 0.6, Confidence: 0.9, Metadata: map[string]any{"fingerprint": fp, "check": "loop"}, Payload: map[string]any{"fingerprint": fp, "kind": cand.Kind, "subject": cand.Subject}})
			}
		}
		scored = append(scored, attention.Scored{ID: string(cand.ID), Breakdown: b})
	}
	attention.Rank(scored)
	if r.metrics != nil {
		r.metrics.Candidates.Add(float64(len(scored)))
	}

	// 3. Decay, then admit.
	prevTop, _ := r.state.Top()
	r.state.Decay(r.mind.Config.WorkingMemoryDecay)
	var thoughts []workspace.Thought
	for _, sc := range scored {
		cand := byID[ids.ID(sc.ID)]
		if _, isSuppressed := suppressed[cand.ID]; isSuppressed {
			continue
		}
		thoughts = append(thoughts, workspace.Thought{ID: cand.ID, Process: cand.Process, Kind: cand.Kind, Subject: cand.Subject, Summary: cand.Summary, Content: cand.Content,
			Score: sc.Breakdown.Total, Breakdown: sc.Breakdown, Evidence: cand.Evidence, Confidence: cand.Confidence})
	}
	outcomes, displaced := r.state.Admit(thoughts, r.mind.Config.WorkspaceCapacity, r.state.Cycle, start)
	outcomeByID := map[ids.ID]workspace.Outcome{}
	for _, o := range outcomes {
		outcomeByID[o.ID] = o
	}

	// 4. Record every candidate with its arithmetic and fate.
	admittedThoughts := map[ids.ID]workspace.Thought{}
	for _, t := range r.state.Workspace {
		if t.AdmittedCycle == r.state.Cycle {
			admittedThoughts[t.ID] = t
		}
	}
	for _, sc := range scored {
		cand := byID[ids.ID(sc.ID)]
		o, has := outcomeByID[cand.ID]
		fate := "rejected"
		reason := o.Reason
		if fp, s := suppressed[cand.ID]; s {
			fate, reason = "suppressed", "loop detector: "+fp
		} else if has && o.Admitted {
			fate = "admitted"
		}
		var causation ids.ID
		if len(cand.SourceEvents) > 0 {
			causation = cand.SourceEvents[0]
		}
		c.Emit(events.New{Type: events.ThoughtCandidate, Source: "brain." + cand.Process, Summary: fmt.Sprintf("[%s %.2f] %s", fate, sc.Breakdown.Total, cand.Summary),
			Salience: sc.Breakdown.Total, Confidence: cand.Confidence, CausationID: causation, ParentIDs: cand.SourceEvents, Evidence: cand.Evidence,
			Metadata: map[string]any{"candidate_id": string(cand.ID), "process": cand.Process, "kind": cand.Kind, "subject": cand.Subject, "fate": fate, "rank": o.Rank},
			Payload:  map[string]any{"candidate_id": cand.ID, "kind": cand.Kind, "subject": cand.Subject, "features": cand.Features, "breakdown": sc.Breakdown, "fate": fate, "reason": reason, "rank": o.Rank, "content": cand.Content}})
	}
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	candidateEvent := map[ids.ID]ids.ID{}
	for _, ev := range c.emitted {
		if ev.Type == events.ThoughtCandidate {
			if s, ok := ev.Metadata["candidate_id"].(string); ok {
				candidateEvent[ids.ID(s)] = ev.ID
			}
		}
	}

	// 5. thought_selected for admissions, then broadcast.
	for i := range r.state.Workspace {
		t := &r.state.Workspace[i]
		if t.AdmittedCycle != r.state.Cycle {
			continue
		}
		cand := byID[t.ID]
		var parents []ids.ID
		if pe, ok := candidateEvent[t.ID]; ok {
			parents = append(parents, pe)
		}
		if cand != nil {
			parents = append(parents, cand.SourceEvents...)
		}
		c.Emit(events.New{Type: events.ThoughtSelected, Source: "brain.workspace", Summary: t.Summary, Salience: t.Score, Confidence: t.Confidence,
			CausationID: firstOr(parents, ids.Nil), ParentIDs: parents, Evidence: t.Evidence,
			Metadata: map[string]any{"candidate_id": string(t.ID), "process": t.Process, "kind": t.Kind, "subject": t.Subject, "rank": outcomeByID[t.ID].Rank},
			Payload:  map[string]any{"thought": t, "why": fmt.Sprintf("rank %d of %d with score %.2f; %s", outcomeByID[t.ID].Rank, len(scored), t.Score, outcomeByID[t.ID].Reason)}})
		if r.metrics != nil {
			r.metrics.Admissions.Inc()
		}
	}
	if err := c.flush(ctx); err != nil {
		return nil, err
	}
	for _, ev := range c.emitted {
		if ev.Type == events.ThoughtSelected {
			if s, ok := ev.Metadata["candidate_id"].(string); ok {
				for i := range r.state.Workspace {
					if string(r.state.Workspace[i].ID) == s {
						r.state.Workspace[i].EventID = ev.ID
					}
				}
			}
		}
	}

	// 6. Executive: dispatch admitted thoughts to handlers (sequential, ordered by rank).
	for _, t := range r.state.Workspace {
		if t.AdmittedCycle != r.state.Cycle {
			continue
		}
		h, ok := r.registry.handlers[t.Kind]
		if !ok {
			h = defaultHandler
		}
		if err := h(ctx, c, t); err != nil {
			r.log.Warn("handler failed", "kind", t.Kind, "err", err)
			c.Emit(events.New{Type: events.Metacognition, Source: "brain.executive", Summary: "handler for " + t.Kind + " failed: " + err.Error(), Salience: 0.4, Confidence: 1,
				Metadata: map[string]any{"check": "handler_error", "kind": t.Kind}})
		}
		r.loops.record(fingerprint(Candidate{Process: t.Process, Kind: t.Kind, Subject: t.Subject}))
	}
	for _, h := range r.registry.hooks {
		if err := h.AfterCycle(ctx, c, c.emitted); err != nil {
			r.log.Warn("hook failed", "hook", h.Name(), "err", err)
		}
	}
	pruned := r.state.Prune(0.05)

	// 7. Broadcast the resulting workspace and note attention shifts.
	if newTop, ok := r.state.Top(); ok && (prevTop.ID == ids.Nil || prevTop.Subject != newTop.Subject || prevTop.Kind != newTop.Kind) {
		c.Emit(events.New{Type: events.AttentionShift, Source: "brain.workspace", Summary: "attention now on: " + newTop.Summary, Salience: newTop.Score, Confidence: newTop.Confidence,
			CausationID: newTop.EventID, Metadata: map[string]any{"from": prevTop.Subject, "to": newTop.Subject}, Payload: map[string]any{"from": prevTop.Summary, "to": newTop.Summary}})
	}
	if len(scored) > 0 || len(displaced) > 0 || pruned > 0 {
		c.Emit(events.New{Type: events.WorkspaceBroadcast, Source: "brain.workspace", Summary: fmt.Sprintf("workspace: %d thoughts, %d in working memory", len(r.state.Workspace), len(r.state.WorkingMemory)),
			Salience: 0.2, Confidence: 1, Metadata: map[string]any{"thoughts": len(r.state.Workspace), "working_memory": len(r.state.WorkingMemory)},
			Payload: map[string]any{"workspace": r.state.Workspace, "working_memory": r.state.WorkingMemory, "displaced": displaced, "pruned": pruned}})
		c.Emit(events.New{Type: events.CycleCompleted, Source: "brain", Summary: fmt.Sprintf("cycle %d (%s): %d events, %d candidates, %d admitted, %d suppressed in %dms", r.state.Cycle, trigger, len(newEvents), len(scored), len(admittedThoughts), len(suppressed), time.Since(start).Milliseconds()),
			Salience: 0.1, Confidence: 1, Metadata: map[string]any{"trigger": trigger, "candidates": len(scored), "admitted": len(admittedThoughts), "suppressed": len(suppressed), "duration_ms": time.Since(start).Milliseconds()},
			Payload: map[string]any{"effective_weights": st.Weights}})
	}
	if err := c.flush(ctx); err != nil {
		return nil, err
	}

	// 8. Persist and advance the cursor to the last *read* event.
	r.state.LastCycleID, r.state.LastCycleAt = cycleID, start
	r.state.EffectiveWeights = st.Weights
	if len(newEvents) > 0 {
		if err := r.events.SetCursor(ctx, tx, r.mind.ID, cursorConsumer, newEvents[len(newEvents)-1].Seq); err != nil {
			return nil, err
		}
	}
	if err := r.saveState(ctx, tx); err != nil {
		return nil, err
	}
	return c.emitted, nil
}

// copyState deep-copies slices so callers cannot alias loop-owned state.
func (r *Runtime) copyState() workspace.State {
	st := r.state
	st.Workspace = append([]workspace.Thought{}, r.state.Workspace...)
	st.WorkingMemory = append([]workspace.Item{}, r.state.WorkingMemory...)
	return st
}

func firstOr(xs []ids.ID, d ids.ID) ids.ID {
	if len(xs) > 0 {
		return xs[0]
	}
	return d
}

// observe runs all processes through the bounded pool with per-process
// timeouts and isolation. A failing process yields no candidates.
func (r *Runtime) observe(ctx context.Context, st *State) []Candidate {
	procs := r.registry.Processes()
	results := make([][]Candidate, len(procs))
	tasks := make([]func(context.Context) error, len(procs))
	for i, p := range procs {
		tasks[i] = func(ctx context.Context) error {
			pctx, cancel := context.WithTimeout(ctx, processTimeout)
			defer cancel()
			out, err := p.Observe(pctx, st)
			if err != nil {
				r.log.Warn("process failed", "process", p.Name(), "err", err)
				return nil
			}
			for j := range out {
				out[j].Process = p.Name()
				out[j].Features = out[j].Features.Clamp()
				if out[j].Kind == "" {
					out[j].Kind = "note"
				}
			}
			results[i] = out
			return nil
		}
	}
	scheduler.Parallel(ctx, r.pool, tasks) //nolint:errcheck // per-task errors already logged
	var all []Candidate
	for _, rs := range results {
		all = append(all, rs...)
	}
	return all
}

// Workspace returns a copy of the live workspace state for handlers.
func (r *Runtime) Workspace() workspace.State { return r.copyState() }

// Events exposes the event store to handlers.
func (r *Runtime) Events() *evstore.Store { return r.events }

// Hold places an item in working memory using the mind's configured capacity.
// It must only be called from handlers (on the loop goroutine).
func (r *Runtime) Hold(it workspace.Item) {
	r.state.Hold(it, r.mind.Config.WorkingMemoryCapacity, r.state.Cycle, r.now())
}

// defaultHandler places an admitted thought into working memory.
func defaultHandler(_ context.Context, c *Cycle, t workspace.Thought) error {
	key := t.Kind + ":" + t.Subject
	if t.Subject == "" {
		key = t.Kind + ":" + string(t.ID)
	}
	ref, _ := t.Content["ref"].(string)
	var src ids.ID
	if s, ok := t.Content["source_event"].(string); ok {
		src = ids.ID(s)
	}
	c.Runtime.state.Hold(workspace.Item{Key: key, Content: t.Summary, Kind: t.Kind, SourceEvent: src, Ref: ref}, c.Runtime.mind.Config.WorkingMemoryCapacity, c.State.Cycle, c.State.Now)
	return nil
}

// cycleBudget is a per-minute token bucket for cycles.
type cycleBudget struct {
	perMinute int
	tokens    float64
	last      time.Time
}

func newCycleBudget(perMinute int) *cycleBudget {
	return &cycleBudget{perMinute: perMinute, tokens: float64(perMinute)}
}

func (b *cycleBudget) allow(now time.Time) bool {
	if !b.last.IsZero() {
		b.tokens += now.Sub(b.last).Minutes() * float64(b.perMinute)
	}
	b.last = now
	if b.tokens > float64(b.perMinute) {
		b.tokens = float64(b.perMinute)
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
