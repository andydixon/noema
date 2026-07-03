// Package replay re-runs recorded cognition (spec §41) and drives sandbox
// minds for experiments (spec §57). A Driver owns a brain runtime that is
// never started as a loop: the driver is the clock, feeding one stimulus at
// a time and stepping cycles until the mind settles. Every step runs under a
// forbidden capability context, so replays and experiments cannot act.
package replay

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/minds"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Driver steps a sandbox mind synchronously.
type Driver struct {
	rt   *brain.Runtime
	cfg  brain.Config
	mind minds.Mind
	why  string
}

// NewDriver builds a runtime for the sandbox mind. cfg.Services.LLM should
// be a replaying router when recorded responses are wanted.
func NewDriver(ctx context.Context, cfg brain.Config, m minds.Mind, why string) (*Driver, error) {
	rt, err := brain.NewRuntime(ctx, cfg, m)
	if err != nil {
		return nil, err
	}
	return &Driver{rt: rt, cfg: cfg, mind: m, why: why}, nil
}

// MaxStepsPerStimulus bounds how long the driver waits for a mind to settle.
const MaxStepsPerStimulus = 8

// bookkeeping event types are produced by every cycle and carry no cognitive content for comparison.
var bookkeeping = map[events.Type]bool{events.CycleCompleted: true, events.WorkspaceBroadcast: true, events.AttentionShift: true, events.ModeChanged: true,
	events.LLMRequest: true, events.LLMResponse: true, events.PolicyDecision: true, events.ThoughtCandidate: true}

// Settle runs cycles until one produces nothing but bookkeeping, or the cap
// is reached, and returns every event committed.
func (d *Driver) Settle(ctx context.Context) ([]events.Event, int, error) {
	ctx = capabilities.WithForbidden(ctx, d.why)
	var all []events.Event
	steps := 0
	for steps < MaxStepsPerStimulus {
		// Let the just-committed events pass the runtime's settle window.
		select {
		case <-time.After(brain.SettleWindow):
		case <-ctx.Done():
			return all, steps, ctx.Err()
		}
		out, err := d.rt.Step(ctx, d.why)
		if err != nil {
			return all, steps, err
		}
		steps++
		all = append(all, out...)
		if !hasContent(out) {
			break
		}
	}
	return all, steps, nil
}

func hasContent(evs []events.Event) bool {
	for _, e := range evs {
		if !bookkeeping[e.Type] {
			return true
		}
	}
	return false
}

// Inject appends a stimulus copy to the sandbox mind and returns it.
func (d *Driver) Inject(ctx context.Context, src events.Event, tag map[string]any) (events.Event, error) {
	meta := map[string]any{}
	for k, v := range src.Metadata {
		meta[k] = v
	}
	for k, v := range tag {
		meta[k] = v
	}
	var payload any = src.Payload
	out, err := d.cfg.Events.Append(ctx, events.New{MindID: d.mind.ID, Type: src.Type, Source: src.Source, OccurredAt: time.Now(), Salience: src.Salience, Confidence: src.Confidence,
		Valence: src.Valence, Arousal: src.Arousal, Evidence: src.Evidence, Summary: src.Summary, Metadata: meta, Payload: payload})
	if err != nil {
		return events.Event{}, err
	}
	return out[0], nil
}

// Observe appends a fresh observation stimulus (experiments).
func (d *Driver) Observe(ctx context.Context, source, text string, salience float64, meta map[string]any) (events.Event, error) {
	out, err := d.cfg.Events.Append(ctx, events.New{MindID: d.mind.ID, Type: events.Observation, Source: source, OccurredAt: time.Now(), Salience: salience, Confidence: 0.9, Summary: text, Metadata: meta,
		Payload: events.ObservationPayload{Text: text, ContentType: "text/plain", Channel: source, Trust: events.TrustOperator}})
	if err != nil {
		return events.Event{}, err
	}
	return out[0], nil
}

// Mind returns the sandbox mind.
func (d *Driver) Mind() minds.Mind { return d.mind }

// Digest is the comparable content of a list of events: type + normalised
// summary with ids and numbers removed, sorted.
func Digest(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		if bookkeeping[e.Type] {
			continue
		}
		out = append(out, string(e.Type)+": "+Normalise(e.Summary))
	}
	sortStrings(out)
	return out
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	numRe  = regexp.MustCompile(`\d+(\.\d+)?`)
)

// Normalise strips ids, numbers and case so summaries from different minds compare.
func Normalise(s string) string {
	s = uuidRe.ReplaceAllString(strings.ToLower(s), "<id>")
	s = numRe.ReplaceAllString(s, "<n>")
	return strings.Join(strings.Fields(s), " ")
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
}

// Same reports whether two digests match as multisets.
func Same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ = ids.Nil
