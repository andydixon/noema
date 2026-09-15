package planning

import (
	"context"
	"errors"

	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/llm/prompts"
	"github.com/andydixon/noema/internal/llm/router"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// PolicyEffect previews the policy effect of a request through the
// capability layer, for the planner's choice of route. It confers nothing.
func PolicyEffect(caps *capabilities.Service) func(ctx context.Context, mind ids.ID, capability, target string, params map[string]any) string {
	return func(ctx context.Context, mind ids.ID, capability, target string, params map[string]any) string {
		d, err := caps.Preview(ctx, capabilities.Invocation{MindID: mind, Capability: capability, Target: target, Params: params})
		if err != nil {
			return "deny"
		}
		if d.Locked {
			return "locked"
		}
		return string(d.Effect)
	}
}

// ModelPlanner asks the model assigned to the plan role for a decomposition.
// The response is parsed and validated by the planner like any draft.
func ModelPlanner(r *router.Router, ps *prompts.Store) func(ctx context.Context, mind ids.ID, objective string, vocabulary []capabilities.Capability) (string, ids.ID, error) {
	return func(ctx context.Context, mind ids.ID, objective string, vocabulary []capabilities.Capability) (string, ids.ID, error) {
		if r == nil || ps == nil || !r.Available(ctx, llm.RolePlan) {
			return "", ids.Nil, errors.New("no model is assigned to the plan role")
		}
		pr, err := ps.Get(ctx, "plan")
		if err != nil {
			return "", ids.Nil, err
		}
		var usable []capabilities.Capability
		for _, c := range vocabulary {
			if !c.Locked {
				usable = append(usable, c)
			}
		}
		req, err := pr.Render(map[string]any{"Objective": objective, "Capabilities": usable})
		if err != nil {
			return "", ids.Nil, err
		}
		req.MindID, req.Purpose = mind, "plan"
		resp, err := r.Complete(ctx, req)
		if err != nil {
			return "", ids.Nil, err
		}
		raw := string(resp.JSON)
		if raw == "" {
			raw = resp.Text
		}
		return raw, resp.CallID, nil
	}
}

// WakeOn returns an after-commit function that wakes the executor when a
// cycle formed, resumed or planned an intention.
func (e *Executor) WakeOn() func(mind ids.ID, emitted []events.Event) {
	return func(_ ids.ID, emitted []events.Event) {
		for _, ev := range emitted {
			switch ev.Type {
			case events.PlanCreated, events.IntentionFormed, events.IntentionResumed, events.PlanReplanned:
				e.Wake()
				return
			}
		}
	}
}

// Listen reacts to events from a bus subscription until ctx ends, and wakes
// the executor for decisions made outside cycles (approvals, grants).
func (e *Executor) Listen(ctx context.Context, ch <-chan events.Event) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			switch ev.Type {
			case events.ToolAuthorised, events.ToolDenied, events.ToolExecuted, events.ToolFailed:
				e.Wake()
			}
			if err := e.React(ctx, ev); err != nil && e.Log != nil {
				e.Log.Warn("plan reaction failed", "event", ev.ID, "err", err)
			}
		}
	}
}
