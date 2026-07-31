package predictions

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/andydixon/noema/internal/beliefs"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/observability"
	"github.com/andydixon/noema/internal/storage"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Resolver judges open predictions against recorded events, records
// outcomes, emits prediction_resolved / prediction_error events and feeds
// the result back into the linked belief as evidence (spec §11).
type Resolver struct {
	Store   *Store
	Events  *evstore.Store
	Beliefs *beliefs.Store
	DB      interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
	Log     *slog.Logger
	Metrics *observability.Metrics
	Now     func() time.Time
}

// Run resolves every judgeable open prediction. Safe to call periodically.
func (r *Resolver) Run(ctx context.Context) error {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	open, err := r.Store.Open(ctx, ids.Nil)
	if err != nil {
		return err
	}
	for _, p := range open {
		if err := r.resolveOne(ctx, p, now); err != nil {
			r.Log.Warn("prediction resolution failed", "prediction", p.ID, "err", err)
		}
	}
	return nil
}

func (r *Resolver) resolveOne(ctx context.Context, p Prediction, now time.Time) error {
	var window []events.Event
	if p.Criteria.Kind == "event_match" {
		var err error
		window, err = r.Events.Query(ctx, evstore.Filter{MindID: p.MindID, Since: p.WindowStart, Until: minTime(now, p.WindowEnd), Ascending: true, Limit: 1000})
		if err != nil {
			return err
		}
		// Events that cite this prediction (its own creation) are not evidence about the world.
		filtered := window[:0]
		for _, ev := range window {
			if ev.ID != p.OriginEvent && ev.Category != events.CatPrediction {
				filtered = append(filtered, ev)
			}
		}
		window = filtered
	}
	var beliefConf *float64
	if p.Criteria.Kind == "belief" && r.Beliefs != nil {
		if b, err := r.Beliefs.Get(ctx, r.Store.db, p.MindID, p.Criteria.BeliefID); err == nil {
			beliefConf = &b.Confidence
		}
	}
	o, ok := Judge(p, now, window, beliefConf)
	if !ok {
		return nil
	}
	return storage.Tx(ctx, r.Store.db, func(tx pgx.Tx) error {
		res, emitted, err := r.ResolveIn(ctx, tx, p, o, "predictions.resolver")
		if err != nil {
			return err
		}
		_ = res
		r.Events.Publish(emitted...)
		return nil
	})
}

// ResolveIn records an outcome inside q and emits the events; shared by the
// scheduler and operator resolution. Returned events must be published by
// the caller after commit.
func (r *Resolver) ResolveIn(ctx context.Context, q storage.Querier, p Prediction, o Outcome, source string) (Result, []events.Event, error) {
	typ := events.PredictionResolved
	salience := 0.4
	if o.Status == StatusRefuted {
		typ = events.PredictionError
		salience = 0.5 + 0.4*p.Confidence // a confident miss deserves attention
	}
	refs := append([]events.EvidenceRef{{Kind: "prediction", ID: string(p.ID)}}, o.Evidence...)
	evs, err := r.Events.AppendIn(ctx, q, events.New{MindID: p.MindID, Type: typ, Source: source, Summary: fmt.Sprintf("prediction %s (was %.2f): %s — %s", o.Status, p.Confidence, p.Statement, o.Reason),
		Salience: salience, Confidence: 1, CausationID: p.OriginEvent, Evidence: refs,
		Metadata: map[string]any{"prediction_id": string(p.ID), "outcome": string(o.Status), "confidence": p.Confidence, "belief_id": string(p.BeliefID)},
		Payload:  map[string]any{"prediction_id": p.ID, "statement": p.Statement, "outcome": o.Status, "confidence": p.Confidence, "reason": o.Reason, "evidence": o.Evidence}})
	if err != nil {
		return Result{}, nil, err
	}
	res, err := r.Store.Resolve(ctx, q, p, o, evs[0].ID)
	if err != nil {
		return Result{}, nil, err
	}
	if r.Metrics != nil {
		if typ == events.PredictionError {
			r.Metrics.PredictionErr.Inc()
		}
	}
	// Feed back into the belief the prediction was derived from.
	if !p.BeliefID.IsZero() && r.Beliefs != nil && (o.Status == StatusConfirmed || o.Status == StatusRefuted) {
		dir, weight := "for", 0.3
		if o.Status == StatusRefuted {
			dir, weight = "against", 0.5
		}
		emit := func(ctx context.Context, q storage.Querier, n events.New) (events.Event, error) {
			out, err := r.Events.AppendIn(ctx, q, n)
			if err != nil {
				return events.Event{}, err
			}
			evs = append(evs, out[0])
			return out[0], nil
		}
		if _, err := r.Beliefs.AddEvidence(ctx, q, emit, p.MindID, p.BeliefID, beliefs.EvidenceInput{Direction: dir, Kind: "prediction", RefID: string(p.ID), Source: "prediction:" + string(p.ID), Weight: weight, Credibility: 0.9, Note: "prediction " + string(o.Status) + ": " + p.Statement,
			Derivation: beliefs.DerivationPrediction, Family: "prediction:" + string(p.ID), RootIDs: refIDs(o.Evidence)}, "prediction "+string(o.Status), evs[0].ID, ids.Nil); err != nil {
			r.Log.Warn("belief feedback failed", "belief", p.BeliefID, "err", err)
		}
	}
	return res, evs, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// refIDs lists the event ids an outcome was judged against.
func refIDs(refs []events.EvidenceRef) []string {
	var out []string
	for _, r := range refs {
		if r.Kind == "event" && r.ID != "" {
			out = append(out, r.ID)
		}
	}
	return out
}
