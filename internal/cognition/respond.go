package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/workspace"
	"github.com/andydixon/noema/pkg/concepts"
	"github.com/andydixon/noema/pkg/events"
	"github.com/andydixon/noema/pkg/ids"
)

// Intent is the structured decision of what to say, built from state before
// any language is generated. It is stored with the reply so the operator can
// check that the words match the intent and the intent matches the evidence.
type Intent struct {
	Kind        string      `json:"kind"` // answer | introspect | acknowledge | clarify | decline | greet | unknown
	Summary     string      `json:"summary"`
	Points      []string    `json:"points"`
	Beliefs     []BeliefRef `json:"beliefs"`
	Memories    []MemRef    `json:"memories"`
	Uncertainty string      `json:"uncertainty,omitempty"`
	Limitations []string    `json:"limitations,omitempty"`
	Analysis    any         `json:"analysis"`
}

// BeliefRef cites a belief.
type BeliefRef struct {
	ID          ids.ID  `json:"id"`
	Proposition string  `json:"proposition"`
	Confidence  float64 `json:"confidence"`
	Status      string  `json:"status"`
}

// MemRef cites a memory.
type MemRef struct {
	ID         ids.ID  `json:"id"`
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
}

// perceive is the handler for admitted "perceive" thoughts: it records the
// perception, encodes the episode, turns claims into beliefs, builds the
// response intent and generates the reply.
func (h Handlers) perceive(ctx context.Context, c *brain.Cycle, t workspace.Thought) error {
	raw, _ := json.Marshal(t.Content["analysis"])
	var a conversation.Analysis
	json.Unmarshal(raw, &a) //nolint:errcheck
	text, _ := t.Content["text"].(string)
	actor, _ := t.Content["actor"].(string)
	convID, _ := t.Content["conversation_id"].(string)
	msgID, _ := t.Content["message_id"].(string)
	srcID, _ := t.Content["source_event"].(string)
	trust := events.Trust(fmt.Sprint(t.Content["trust"]))
	if !trust.Valid() {
		trust = events.TrustUser
	}
	mind := c.State.Mind

	if call, ok := t.Content["llm_call"].(string); ok && call != "" {
		c.Emit(events.New{Type: events.LLMResponse, Source: "brain.perceive", Summary: "perception enriched by " + fmt.Sprint(t.Content["llm_model"]), Salience: 0.1, Confidence: 0.8,
			CausationID: t.EventID, Evidence: []events.EvidenceRef{{Kind: "llm_call", ID: call}}, Metadata: map[string]any{"role": "perceive", "call_id": call}})
	}
	perc, err := c.EmitNow(ctx, events.New{Type: events.Perception, Source: "brain.perceive", Summary: fmt.Sprintf("perceived %s from %s: %s", a.Intent, actor, truncate(text, 100)),
		Salience: t.Score, Confidence: 0.9, CausationID: t.EventID, ParentIDs: []ids.ID{t.EventID, ids.ID(srcID)}, Evidence: t.Evidence,
		Metadata: map[string]any{"intent": a.Intent, "introspection": a.Introspection, "conversation_id": convID, "message_id": msgID, "entities": a.Entities},
		Payload:  map[string]any{"analysis": a, "text": text, "actor": actor}})
	if err != nil {
		return err
	}
	// Episode of the exchange.
	var memRef ids.ID
	if h.Memory != nil {
		mem, err := h.Memory.Create(ctx, c.Tx, memory.New{MindID: mind.ID, Kind: concepts.Episodic, Epistemic: concepts.Observation, Content: actor + " said: " + text, Source: "conversation",
			Trust: trust, Confidence: 1, Importance: 0.4 + 0.3*a.Urgency, Salience: t.Score, OccurredAt: c.State.Now, OriginEvent: ids.ID(srcID),
			Detail: map[string]any{"participants": []string{actor}, "channel": "conversation", "conversation_id": convID, "intent": a.Intent, "entities": a.Entities}})
		if err == nil {
			memRef = mem.ID
			c.Emit(events.New{Type: events.MemoryCreated, Source: "brain.memory", Summary: "remembered: " + truncate(mem.Content, 100), Salience: 0.3, Confidence: 1, CausationID: perc.ID,
				Evidence: []events.EvidenceRef{{Kind: "memory", ID: string(mem.ID)}, {Kind: "event", ID: srcID, Note: "source"}}, Metadata: map[string]any{"memory_id": mem.ID, "kind": mem.Kind}})
		}
	}
	// Claims become beliefs; the message is their evidence.
	var claimed []BeliefRef
	if h.Beliefs != nil {
		for _, cl := range a.Claims {
			w := 0.6
			if cl.Certainty == "hedged" {
				w = 0.3
			}
			if a.Intent == "correction" {
				w += 0.2
			}
			res, err := h.Beliefs.Create(ctx, c.Tx, emitter(c), beliefs.New{MindID: mind.ID, Proposition: cl.Proposition, Epistemic: concepts.Claim, Source: "conversation", OriginEvent: perc.ID, Causation: perc.ID, Correlation: c.State.CycleID,
				Evidence: []beliefs.EvidenceInput{{Direction: "for", Kind: "event", RefID: srcID, Source: "said-by:" + actor, Weight: w, Credibility: trust.Credibility(), Note: actor + " " + cl.Certainty + " it"}}})
			if err != nil {
				c.Emit(events.New{Type: events.Metacognition, Source: "brain.perceive", Summary: "claim not recorded: " + err.Error(), Salience: 0.3, Confidence: 1, Metadata: map[string]any{"check": "claim_rejected"}})
				continue
			}
			claimed = append(claimed, BeliefRef{ID: res.Belief.ID, Proposition: res.Belief.Proposition, Confidence: res.Belief.Confidence, Status: string(res.Belief.Status)})
		}
	}
	intent := h.buildIntent(ctx, c, a, text, claimed)
	reply, callID, err := h.generate(ctx, c, text, intent)
	if err != nil {
		return err
	}
	evidence := []events.EvidenceRef{{Kind: "event", ID: srcID, Note: "question"}, {Kind: "event", ID: string(perc.ID), Note: "perception"}}
	for _, b := range intent.Beliefs {
		evidence = append(evidence, events.EvidenceRef{Kind: "belief", ID: string(b.ID), Note: fmt.Sprintf("cited %.2f", b.Confidence)})
	}
	for _, m := range intent.Memories {
		evidence = append(evidence, events.EvidenceRef{Kind: "memory", ID: string(m.ID), Note: "cited"})
	}
	if !memRef.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "memory", ID: string(memRef), Note: "episode"})
	}
	if !callID.IsZero() {
		evidence = append(evidence, events.EvidenceRef{Kind: "llm_call", ID: string(callID), Note: "wording"})
	}
	respEv, err := c.EmitNow(ctx, events.New{Type: events.ResponseGenerated, Source: "brain.respond", Summary: "replied (" + intent.Kind + "): " + truncate(reply, 120), Salience: 0.5, Confidence: intentConfidence(intent),
		CausationID: perc.ID, ParentIDs: []ids.ID{perc.ID}, Evidence: evidence,
		Metadata: map[string]any{"conversation_id": convID, "message_id": msgID, "intent": intent.Kind, "llm_call": callID},
		Payload:  map[string]any{"intent": intent, "reply": reply, "model_used": !callID.IsZero()}})
	if err != nil {
		return err
	}
	if h.Conversations != nil && convID != "" {
		intentJSON, _ := json.Marshal(intent)
		if _, err := h.Conversations.AddMessage(ctx, c.Tx, conversation.Message{ConversationID: ids.ID(convID), MindID: mind.ID, Role: "mind", Actor: mind.DisplayName, Content: reply,
			EventID: respEv.ID, ReplyTo: ids.ID(msgID), Intent: intentJSON, LLMCallID: callID}); err != nil {
			return fmt.Errorf("store reply: %w", err)
		}
	}
	c.Runtime.Hold(workspace.Item{Key: "conversation:" + convID, Content: actor + ": " + truncate(text, 80) + " → " + truncate(reply, 80), Kind: "thought", SourceEvent: ids.ID(srcID), Ref: convID})
	return nil
}

func intentConfidence(in Intent) float64 {
	var max float64
	for _, b := range in.Beliefs {
		if b.Confidence > max {
			max = b.Confidence
		}
	}
	switch in.Kind {
	case "unknown", "decline":
		return 0.9 // confident that it does not know
	case "introspect", "acknowledge", "greet":
		return 0.95
	}
	if max == 0 {
		return 0.5
	}
	return max
}

// buildIntent decides what to convey from state alone.
func (h Handlers) buildIntent(ctx context.Context, c *brain.Cycle, a conversation.Analysis, text string, claimed []BeliefRef) Intent {
	mind := c.State.Mind
	in := Intent{Analysis: a, Limitations: c.State.SelfModel.Limitations}
	if len(in.Limitations) > 3 {
		in.Limitations = in.Limitations[:3]
	}
	topic := a.Topic
	if topic == "" {
		topic = text
	}
	switch a.Intent {
	case "greeting":
		in.Kind, in.Summary = "greet", "Greet "+firstNonEmpty(a.Topic, "the user")+" briefly and offer to help."
	case "statement", "correction":
		in.Kind = "acknowledge"
		if len(claimed) == 0 {
			in.Summary = "Acknowledge the message; no checkable claim was found in it."
			break
		}
		in.Beliefs = claimed
		in.Summary = fmt.Sprintf("Acknowledge that %d statement(s) were recorded as claims with the shown confidence, noting they rest on this message alone.", len(claimed))
		for _, b := range claimed {
			in.Points = append(in.Points, fmt.Sprintf("Recorded \"%s\" as a %s at confidence %.2f.", b.Proposition, b.Status, b.Confidence))
			if b.Status == string(beliefs.StatusContested) {
				in.Points = append(in.Points, "That conflicts with something I already held; both are now contested.")
			}
		}
	case "request":
		in.Kind = "decline"
		caps := c.State.SelfModel.Capabilities
		if len(caps) == 0 {
			in.Summary = "Explain honestly that no external capabilities are granted, so the request cannot be carried out; offer what is known instead."
			in.Points = append(in.Points, "I have no capabilities granted to act on external systems.")
		} else {
			in.Summary = "Explain which capabilities exist and that actions require policy approval."
		}
		h.addKnowledge(ctx, c, &in, topic)
	case "introspection":
		in.Kind = "introspect"
		h.introspect(ctx, c, &in, a, topic)
	default: // question, other
		in.Kind = "answer"
		h.addKnowledge(ctx, c, &in, topic)
		if len(in.Beliefs) == 0 && len(in.Memories) == 0 {
			in.Kind = "unknown"
			in.Summary = "Say plainly that nothing is known about this; do not guess."
			in.Uncertainty = "No beliefs or memories match the question."
		} else {
			in.Summary = "Answer from the cited beliefs and memories, stating confidence; say what is uncertain."
		}
	}
	if in.Points == nil {
		in.Points = []string{}
	}
	if in.Beliefs == nil {
		in.Beliefs = []BeliefRef{}
	}
	if in.Memories == nil {
		in.Memories = []MemRef{}
	}
	_ = mind
	return in
}

// addKnowledge cites the beliefs and memories relevant to a topic.
func (h Handlers) addKnowledge(ctx context.Context, c *brain.Cycle, in *Intent, topic string) {
	mind := c.State.Mind.ID
	if h.Beliefs != nil {
		if bs, _ := h.Beliefs.Search(ctx, mind, topic, 5); len(bs) > 0 {
			var maxConf float64
			for _, b := range bs {
				if b.Status == beliefs.StatusRejected {
					continue
				}
				in.Beliefs = append(in.Beliefs, BeliefRef{ID: b.ID, Proposition: b.Proposition, Confidence: b.Confidence, Status: string(b.Status)})
				in.Points = append(in.Points, fmt.Sprintf("%s (%s, %.2f)", b.Proposition, b.Status, b.Confidence))
				if b.Confidence > maxConf {
					maxConf = b.Confidence
				}
			}
			if maxConf > 0 && maxConf < 0.6 {
				in.Uncertainty = fmt.Sprintf("The strongest relevant belief is only at %.2f.", maxConf)
			}
		}
	}
	if h.Memory != nil {
		if hits, _ := h.Memory.Retrieve(ctx, mind, memory.Query{Text: topic, Limit: 4, Now: c.State.Now, Semantic: true}); len(hits) > 0 {
			for _, r := range hits {
				if r.Score < 0.3 {
					continue
				}
				in.Memories = append(in.Memories, MemRef{ID: r.Memory.ID, Kind: string(r.Memory.Kind), Content: truncate(r.Memory.Content, 200), Confidence: r.Memory.Confidence})
				in.Points = append(in.Points, fmt.Sprintf("I remember: %s (%s, %.2f)", truncate(r.Memory.Content, 160), r.Memory.Kind, r.Memory.Confidence))
			}
		}
	}
}

// introspect answers questions about the mind's own state from that state.
func (h Handlers) introspect(ctx context.Context, c *brain.Cycle, in *Intent, a conversation.Analysis, topic string) {
	mind := c.State.Mind.ID
	ws := c.Runtime.Workspace()
	switch a.Introspection {
	case "thinking":
		in.Summary = "Describe what is currently in the workspace and working memory, most salient first."
		for i, t := range ws.Workspace {
			if i >= 5 {
				break
			}
			in.Points = append(in.Points, fmt.Sprintf("%s (score %.2f)", t.Summary, t.Score))
		}
		for i, it := range ws.WorkingMemory {
			if i >= 4 {
				break
			}
			in.Points = append(in.Points, "holding: "+truncate(it.Content, 100))
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "The workspace is empty; nothing is being attended to right now.")
		}
	case "beliefs":
		in.Summary = "List what is believed about the topic with confidence and status; say if nothing is known."
		h.addKnowledge(ctx, c, in, topic)
		if len(in.Beliefs) == 0 && len(in.Memories) == 0 {
			in.Kind, in.Uncertainty = "unknown", "No beliefs or memories concern "+topic+"."
		}
	case "why":
		in.Summary = "Explain the evidence for and against the belief the user asked about, and what would change it."
		if h.Beliefs != nil {
			if bs, _ := h.Beliefs.Search(ctx, mind, topic, 1); len(bs) > 0 {
				b := bs[0]
				in.Beliefs = append(in.Beliefs, BeliefRef{ID: b.ID, Proposition: b.Proposition, Confidence: b.Confidence, Status: string(b.Status)})
				rows, _ := h.Beliefs.EvidenceFor(ctx, c.Tx, mind, b.ID)
				for _, e := range rows {
					if e.Retracted {
						continue
					}
					in.Points = append(in.Points, fmt.Sprintf("%s: %s (weight %.2f × credibility %.2f)", e.Direction, firstNonEmpty(e.Note, e.Kind), e.Weight, e.Credibility))
				}
				in.Points = append(in.Points, "Independent evidence from another source would move it most; a contradicting observation would lower it.")
			} else {
				in.Kind, in.Summary = "unknown", "Say that no belief matches what was asked."
			}
		}
	case "uncertain":
		in.Summary = "List the beliefs that are least settled, with confidence."
		if h.Beliefs != nil {
			bs, _ := h.Beliefs.Uncertain(ctx, mind, 6)
			for _, b := range bs {
				in.Beliefs = append(in.Beliefs, BeliefRef{ID: b.ID, Proposition: b.Proposition, Confidence: b.Confidence, Status: string(b.Status)})
				in.Points = append(in.Points, fmt.Sprintf("%s (%s, %.2f)", b.Proposition, b.Status, b.Confidence))
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "No beliefs sit in the uncertain band right now.")
		}
	case "contradictions":
		in.Summary = "List contested beliefs and what they conflict with."
		if h.Beliefs != nil {
			bs, _ := h.Beliefs.Contested(ctx, mind, 6)
			for _, b := range bs {
				in.Beliefs = append(in.Beliefs, BeliefRef{ID: b.ID, Proposition: b.Proposition, Confidence: b.Confidence, Status: string(b.Status)})
				links, _ := h.Beliefs.Links(ctx, mind, b.ID)
				for _, l := range links {
					if l.Relation == "contradicts" && l.Other != nil {
						in.Points = append(in.Points, fmt.Sprintf("\"%s\" (%.2f) contradicts \"%s\" (%.2f)", b.Proposition, b.Confidence, l.Other.Proposition, l.Other.Confidence))
					}
				}
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "No contradictions are currently open.")
		}
	case "learned":
		in.Summary = "Summarise what was recorded in the last seven days: new memories and beliefs."
		evs, _ := c.Runtime.Events().Query(ctx, evstore.Filter{MindID: mind, Types: []events.Type{events.MemoryCreated, events.BeliefCreated, events.HypothesisCreated, events.MemoryConsolidated}, Since: c.State.Now.Add(-7 * 24 * time.Hour), Limit: 8})
		for _, e := range evs {
			in.Points = append(in.Points, e.Summary)
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "Nothing new was recorded this week.")
		}
	case "changed":
		in.Summary = "List beliefs whose status changed recently and why."
		if h.Beliefs != nil {
			ch, _ := h.Beliefs.RecentChanges(ctx, mind, c.State.Now.Add(-30*24*time.Hour), 6)
			for _, x := range ch {
				in.Beliefs = append(in.Beliefs, BeliefRef{ID: x.Belief.ID, Proposition: x.Belief.Proposition, Confidence: x.Belief.Confidence, Status: string(x.Belief.Status)})
				in.Points = append(in.Points, fmt.Sprintf("\"%s\": %s (%.2f) → %s (%.2f) because %s", x.Belief.Proposition, x.FromStatus, x.FromConf, x.Belief.Status, x.Belief.Confidence, firstNonEmpty(x.Reason, "evidence changed")))
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "No belief has changed status in the last month.")
		}
	default: // predictions, curious, surprised, patterns: subsystems not yet present
		in.Kind = "decline"
		in.Summary = "Explain that this aspect of self-observation is not available in this version, without inventing anything."
		in.Points = append(in.Points, "I do not yet keep "+a.Introspection+" records, so I cannot report on them honestly.")
	}
}

// generate phrases the intent. With a model: the respond prompt, constrained
// to cited material. Without: a deterministic rendering of the points.
func (h Handlers) generate(ctx context.Context, c *brain.Cycle, text string, in Intent) (string, ids.ID, error) {
	s := c.State
	if s.Services.LLM != nil && s.Services.Prompts != nil && s.Services.LLM.Available(ctx, llm.RoleRespond) && s.AllowLLM() {
		if pr, err := s.Services.Prompts.Get(ctx, "respond"); err == nil {
			data := map[string]any{"Brief": s.Mind.Personality.Brief(), "Intent": in.Summary + "\n" + strings.Join(in.Points, "\n"), "Beliefs": in.Beliefs, "Memories": in.Memories, "Uncertainty": in.Uncertainty, "Limitations": in.Limitations, "Message": text}
			if req, err := pr.Render(data); err == nil {
				req.MindID, req.CorrelationID, req.Purpose = s.Mind.ID, s.CycleID, "respond"
				c.Emit(events.New{Type: events.LLMRequest, Source: "brain.respond", Summary: "asked model to phrase reply (" + in.Kind + ")", Salience: 0.1, Confidence: 1, Metadata: map[string]any{"role": "respond", "prompt": pr.Name, "version": pr.CurrentVersion}})
				resp, err := s.Services.LLM.Complete(ctx, req)
				if err == nil && strings.TrimSpace(resp.Text) != "" {
					c.Emit(events.New{Type: events.LLMResponse, Source: "brain.respond", Summary: "model phrased reply via " + resp.Provider + "/" + resp.Model, Salience: 0.1, Confidence: 1,
						Evidence: []events.EvidenceRef{{Kind: "llm_call", ID: string(resp.CallID)}}, Metadata: map[string]any{"role": "respond", "call_id": string(resp.CallID), "tokens": resp.Usage.TotalTokens}})
					return strings.TrimSpace(resp.Text), resp.CallID, nil
				}
				c.Emit(events.New{Type: events.Metacognition, Source: "brain.respond", Summary: "model reply failed; using deterministic wording", Salience: 0.3, Confidence: 1, Metadata: map[string]any{"check": "llm_failure", "error": errString(err)}})
			}
		}
	}
	return deterministicReply(in), ids.Nil, nil
}

func errString(err error) string {
	if err == nil {
		return "empty response"
	}
	return err.Error()
}

// deterministicReply renders the intent without a model: honest, plain, with
// confidences visible.
func deterministicReply(in Intent) string {
	var b strings.Builder
	switch in.Kind {
	case "greet":
		b.WriteString("Hello. I can tell you what I currently hold and how sure I am of it.")
	case "unknown":
		b.WriteString("I don't know. Nothing I hold bears on that.")
	case "decline":
		b.WriteString("I can't do that.")
	case "acknowledge":
		b.WriteString("Noted.")
	case "introspect":
		b.WriteString("From my current state:")
	default:
		b.WriteString("Here is what I hold on that:")
	}
	for _, p := range in.Points {
		b.WriteString("\n- " + p)
	}
	if in.Uncertainty != "" {
		b.WriteString("\n" + in.Uncertainty)
	}
	b.WriteString("\n(No language model is configured, so this is a direct rendering of my state.)")
	return b.String()
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
