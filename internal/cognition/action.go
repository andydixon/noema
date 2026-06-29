package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/andydixon/noema/internal/attention"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/capabilities"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Act proposes capability invocations from requests the mind receives. In
// this phase the only deterministic mapping is "fetch <url>" → http.fetch;
// integrations register more. Proposing is free; executing goes through the
// policy layer, so a request never bypasses ALLOW / ASK / DENY.
type Act struct{}

// Name implements brain.Process.
func (Act) Name() string { return "act" }

var urlRe = regexp.MustCompile(`https?://[^\s<>"']+`)

// Observe implements brain.Process.
func (Act) Observe(_ context.Context, s *brain.State) ([]brain.Candidate, error) {
	if s.Services.Capabilities == nil {
		return nil, nil
	}
	var out []brain.Candidate
	for _, ev := range s.NewEvents {
		if ev.Type != events.Observation || ev.Metadata["conversation_id"] == nil {
			continue
		}
		var p events.ObservationPayload
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		a := conversation.Analyse(p.Text)
		if a.Intent != "request" {
			continue
		}
		lower := strings.ToLower(p.Text)
		if u := urlRe.FindString(p.Text); u != "" && (strings.Contains(lower, "fetch") || strings.Contains(lower, "read") || strings.Contains(lower, "look at") || strings.Contains(lower, "check")) {
			u = strings.TrimRight(u, ".,)")
			out = append(out, brain.Candidate{Kind: "action", Subject: "http.fetch:" + u, Summary: "proposed action: fetch " + u,
				Content:    map[string]any{"capability": "http.fetch", "target": u, "params": map[string]any{"url": u}, "reason": "asked by " + p.Actor + ": " + truncate(p.Text, 120), "source_event": string(ev.ID), "conversation_id": ev.Metadata["conversation_id"]},
				Features:   attention.Features{Salience: 0.7, Urgency: 0.6, Consequence: 0.7, Confidence: 0.9, OperatorPriority: prioFor(p.Trust), ContextRelevance: 0.5},
				Confidence: 0.9, SourceEvents: []ids.ID{ev.ID}, Evidence: []events.EvidenceRef{{Kind: "event", ID: string(ev.ID), Note: "request"}}})
		}
	}
	return out, nil
}

func prioFor(t events.Trust) float64 {
	if t == events.TrustOperator {
		return 1
	}
	return 0.7
}

// action invokes the capability layer for an admitted action thought and
// records the outcome in working memory. The layer decides; this handler
// never executes anything itself.
func (h Handlers) action(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	svc := c.State.Services.Capabilities
	if svc == nil {
		return nil
	}
	capName, _ := t.Content["capability"].(string)
	target, _ := t.Content["target"].(string)
	reason, _ := t.Content["reason"].(string)
	params, _ := t.Content["params"].(map[string]any)
	var ev []capabilities.EvidenceRef
	for _, e := range t.Evidence {
		ev = append(ev, capabilities.EvidenceRef{Kind: e.Kind, ID: e.ID, Note: e.Note})
	}
	if c.State.Mode == brain.ModeDreaming {
		ctx = capabilities.WithForbidden(ctx, "dreaming")
	}
	out, err := svc.Invoke(ctx, capabilities.Invocation{MindID: c.State.Mind.ID, Capability: capName, Target: target, Params: params, Reason: reason, Evidence: ev, Correlation: c.State.CycleID})
	if err != nil {
		c.Emit(events.New{Type: events.Metacognition, Source: "brain.act", Summary: "action refused before policy: " + err.Error(), Salience: 0.4, Confidence: 1, CausationID: t.EventID, Metadata: map[string]any{"check": "action_invalid", "capability": capName}})
		return nil
	}
	content := fmt.Sprintf("%s on %s: %s", capName, target, out.Status)
	if out.Status == "pending" {
		content += " (awaiting operator approval)"
	}
	if out.Status == "denied" {
		content += " (" + out.Decision.Reason + ")"
	}
	c.Runtime.Hold(workspace.Item{Key: "action:" + capName + ":" + target, Content: content, Kind: "note", SourceEvent: out.EventID, Ref: string(out.ApprovalID)})
	return nil
}
