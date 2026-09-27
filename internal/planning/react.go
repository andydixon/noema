package planning

import (
	"context"
	"errors"
	"strings"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/volition"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// React reconsiders open intentions and plans when something they depend on
// changes: a new commitment, a weakened or retracted assumption, a goal
// completed or abandoned. It is deterministic, runs outside cognitive
// cycles (from the event bus), and never acts; replanning may follow.
func (e *Executor) React(ctx context.Context, ev events.Event) error {
	switch ev.Type {
	case events.CommitmentCreated, events.BeliefUpdated, events.BeliefRetracted, events.BeliefChallenged, events.GoalCompleted, events.GoalAbandoned:
	default:
		return nil
	}
	m, err := e.Minds.Get(ctx, ev.MindID)
	if err != nil || m.Status != "active" {
		return err
	}
	open, err := e.Volition.Open(ctx, nil, ev.MindID)
	if err != nil {
		return err
	}
	meta := func(k string) string { s, _ := ev.Metadata[k].(string); return s }
	for _, it := range open {
		if !it.ParentID.IsZero() {
			continue // a subordinate intention shares its parent's plan; the parent is reconsidered
		}
		var t *volition.Trigger
		switch ev.Type {
		case events.CommitmentCreated:
			if m.Config.Ablated("commitments") {
				return nil
			}
			cm, err := e.Volition.GetCommitment(ctx, nil, ev.MindID, ids.ID(meta("commitment_id")))
			if err != nil || it.Proposal == nil || it.Proposal.Kind != "capability" {
				continue
			}
			a := e.actionInfo(ctx, it)
			t = &volition.Trigger{Kind: volition.TriggerCommitmentChanged, EventID: ev.ID, Commitment: &cm, Action: a, Detail: cm.Description}
		case events.GoalCompleted, events.GoalAbandoned:
			if string(it.GoalID) != meta("goal_id") || it.GoalID.IsZero() {
				continue
			}
			kind := volition.TriggerGoalCompleted
			if ev.Type == events.GoalAbandoned {
				kind = volition.TriggerGoalAbandoned
			}
			t = &volition.Trigger{Kind: kind, EventID: ev.ID, Detail: ev.Summary}
		default: // belief changes: only plans that assume the belief care
			bid := meta("belief_id")
			if bid == "" || it.PlanID.IsZero() || e.Resolver == nil || e.Resolver.Beliefs == nil {
				continue
			}
			p, err := e.Plans.Get(ctx, nil, it.PlanID)
			if err != nil || !PlanOpen(p.Status) {
				continue
			}
			for _, a := range p.Assumptions {
				if string(a.BeliefID) != bid {
					continue
				}
				b, err := e.Resolver.Beliefs.Get(ctx, nil, ev.MindID, a.BeliefID)
				if err != nil {
					continue
				}
				if b.Confidence < a.MinConfidence || b.Status == "rejected" || b.Status == "superseded" {
					t = &volition.Trigger{Kind: volition.TriggerBeliefWeakened, EventID: ev.ID, Detail: "an assumption no longer holds: \"" + b.Proposition + "\" is " + string(b.Status)}
				}
			}
		}
		if t == nil {
			continue
		}
		if err := e.apply(ctx, it, *t); err != nil && !errors.Is(err, volition.ErrClosed) {
			return err
		}
	}
	return nil
}

func (e *Executor) apply(ctx context.Context, it volition.Intention, t volition.Trigger) error {
	if !it.PlanID.IsZero() {
		if p, err := e.Plans.Get(ctx, nil, it.PlanID); err == nil && PlanOpen(p.Status) && p.IntentionID == it.ID {
			return e.reconsider(ctx, p, it, t)
		}
	}
	rc := volition.Reconsider(it, t, volition.ReconsiderContext{Now: e.now()})
	if rc.Outcome == volition.Keep {
		return nil
	}
	_, err := e.Volition.Apply(ctx, e.rec(), it.MindID, it.ID, rc, t)
	return err
}

func (e *Executor) actionInfo(ctx context.Context, it volition.Intention) *volition.ActionInfo {
	p := it.Proposal
	a := &volition.ActionInfo{Capability: p.Capability, Target: p.Target, Effect: "unknown"}
	c, ok := e.Caps.Registry.Get(p.Capability)
	if !ok {
		return a
	}
	a.Access, a.Risk, a.Idempotent, a.SideEffects = string(c.Access), string(c.Risk), c.Idempotent, c.SideEffects
	if t, err := c.ResolveTarget("", p.Params); err == nil && t != "" {
		a.Target = t
	}
	if c.Locked {
		a.Effect = "locked"
		return a
	}
	d, err := e.Caps.Preview(ctx, capabilities.Invocation{MindID: it.MindID, Capability: p.Capability, Params: p.Params})
	if err != nil {
		a.Effect = "deny"
		return a
	}
	a.Effect = strings.ToLower(string(d.Effect))
	return a
}
