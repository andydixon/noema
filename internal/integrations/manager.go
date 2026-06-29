package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/graph"
	"github.com/andydixon/noema/internal/scheduler"
	"github.com/andydixon/noema/internal/security/secrets"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Poller ingests from one kind of source.
type Poller interface {
	// Poll fetches new material and emits observations via the sink. It must
	// respect ctx and never execute ingested content.
	Poll(ctx context.Context, in Integration, sink *Sink) error
}

// Sink is what pollers use to emit observations and documents.
type Sink struct {
	Events *evstore.Store
	Store  *Store
	Graph  *graph.Store
	Log    *slog.Logger
	Mind   ids.ID
	Integ  Integration
	run    Run
	notes  []string
	mu     sync.Mutex
}

// Note records a human-readable line in the run.
func (s *Sink) Note(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notes) < 50 {
		s.notes = append(s.notes, fmt.Sprintf(format, args...))
	}
}

// Item counts something examined.
func (s *Sink) Item() { s.mu.Lock(); s.run.Items++; s.mu.Unlock() }

// Fail counts an error.
func (s *Sink) Fail(err error) {
	s.mu.Lock()
	s.run.Errors++
	s.mu.Unlock()
	s.Note("error: %v", err)
}

// Observation describes something perceived from the source.
type Observation struct {
	Summary     string
	Text        string
	Reference   string
	ContentType string
	Salience    float64
	Metadata    map[string]any
	Entities    []string // names to upsert as graph entities (document titles, repos, hosts)
	EntityKind  graph.Kind
}

// Observe emits an observation event with full provenance and tool-grade
// trust. Text is capped; the caller is responsible for safe extraction.
func (s *Sink) Observe(ctx context.Context, o Observation) (events.Event, error) {
	if len(o.Text) > 20000 {
		o.Text = o.Text[:20000]
	}
	if o.Salience <= 0 {
		o.Salience = 0.5
	}
	md := map[string]any{"integration_id": string(s.Integ.ID), "integration": s.Integ.Name, "kind": s.Integ.Kind, "trust": s.Integ.Trust}
	for k, v := range o.Metadata {
		md[k] = v
	}
	summary := o.Summary
	if len(summary) > 180 {
		summary = summary[:177] + "..."
	}
	evs, err := s.Events.Append(ctx, events.New{MindID: s.Mind, Type: events.Observation, Source: string(s.Integ.Kind) + ":" + s.Integ.Name, Summary: summary, Salience: o.Salience, Confidence: 0.9, Metadata: md,
		Payload: events.ObservationPayload{Text: o.Text, ContentType: firstNonEmpty(o.ContentType, "text/plain"), Source: string(s.Integ.Kind) + ":" + s.Integ.Name, Trust: s.Integ.Trust, Method: "poll", Reference: o.Reference, Channel: string(s.Integ.ID)}})
	if err != nil {
		return events.Event{}, err
	}
	s.mu.Lock()
	s.run.Observations++
	s.mu.Unlock()
	if s.Graph != nil {
		for _, name := range o.Entities {
			if e, err := s.Graph.Upsert(ctx, s.Store.db, s.Mind, o.EntityKind, name, time.Now()); err == nil {
				s.Graph.LinkRef(ctx, s.Store.db, s.Mind, e.ID, "event", evs[0].ID) //nolint:errcheck
			}
		}
	}
	return evs[0], nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Manager schedules pollers with bounded concurrency and records runs.
type Manager struct {
	Store   *Store
	Events  *evstore.Store
	Graph   *graph.Store
	Secrets *secrets.Resolver
	Log     *slog.Logger
	Pollers map[Kind]Poller
	Pool    *scheduler.Pool
	running sync.Map // integration id → struct{} while a poll is in flight
}

// Due returns enabled integrations whose interval has elapsed.
func (m *Manager) Due(ctx context.Context) ([]Integration, error) {
	list, err := m.Store.List(ctx, ids.Nil)
	if err != nil {
		return nil, err
	}
	var out []Integration
	for _, i := range list {
		if !i.Enabled || i.Kind == Webhook {
			continue
		}
		if i.LastRunAt == nil || time.Since(*i.LastRunAt) >= i.Interval() {
			out = append(out, i)
		}
	}
	return out, nil
}

// Tick runs every due integration through the pool. Called by the scheduler.
func (m *Manager) Tick(ctx context.Context) error {
	due, err := m.Due(ctx)
	if err != nil {
		return err
	}
	var tasks []func(context.Context) error
	for _, in := range due {
		in := in
		tasks = append(tasks, func(ctx context.Context) error { _, err := m.RunOne(ctx, in.ID); return err })
	}
	scheduler.Parallel(ctx, m.Pool, tasks) //nolint:errcheck // per-run errors are recorded on the integration
	return m.Store.MarkStale(ctx)
}

// RunOne polls a single integration now and records the run.
func (m *Manager) RunOne(ctx context.Context, id ids.ID) (Run, error) {
	if _, busy := m.running.LoadOrStore(id, struct{}{}); busy {
		return Run{}, fmt.Errorf("%w: integration is already running", ErrInvalid)
	}
	defer m.running.Delete(id)
	in, err := m.Store.Get(ctx, id)
	if err != nil {
		return Run{}, err
	}
	p, ok := m.Pollers[in.Kind]
	if !ok {
		return Run{}, fmt.Errorf("%w: no poller for %s", ErrInvalid, in.Kind)
	}
	sink := &Sink{Events: m.Events, Store: m.Store, Graph: m.Graph, Log: m.Log, Mind: in.MindID, Integ: in, run: Run{IntegrationID: id, StartedAt: time.Now()}}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("poller panicked: %v", r)
			}
		}()
		err = p.Poll(pctx, in, sink)
	}()
	run := sink.run
	if err != nil {
		run.Error = scrub(err.Error())
	}
	notes, _ := json.Marshal(sink.notes)
	run.Notes = notes
	if rerr := m.Store.RecordRun(ctx, id, run); rerr != nil {
		m.Log.Error("record integration run failed", "err", rerr)
	}
	return run, err
}

// scrub removes credential-looking fragments from error text.
func scrub(s string) string {
	for _, marker := range []string{"Bearer ", "://"} {
		if i := strings.Index(s, marker); i >= 0 && marker == "://" {
			// hide userinfo in URLs
			if at := strings.Index(s[i:], "@"); at > 0 {
				s = s[:i+3] + "[redacted]" + s[i+at:]
			}
		}
	}
	return s
}
