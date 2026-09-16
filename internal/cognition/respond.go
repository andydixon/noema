package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andydixon/noema/internal/agency"
	"github.com/andydixon/noema/internal/beliefs"
	"github.com/andydixon/noema/internal/brain"
	"github.com/andydixon/noema/internal/conversation"
	"github.com/andydixon/noema/internal/epistemics"
	evstore "github.com/andydixon/noema/internal/events"
	"github.com/andydixon/noema/internal/llm"
	"github.com/andydixon/noema/internal/memory"
	"github.com/andydixon/noema/internal/research"
	"github.com/andydixon/noema/internal/volition"
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
	// Facts are the recorded elements an introspective answer is scored
	// against (docs/cognitive-research.md, introspection accuracy).
	Facts *research.Facts `json:"facts,omitempty"`
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
	// Every claim is first an assertion: the certain fact that someone took a
	// stance. Only factual assertions (direct or reported) then feed a
	// proposition belief, as one piece of evidence attributed to whoever
	// actually stands behind it; hypotheticals, questions and imperatives
	// never do, and expressed doubt counts against.
	var claimed []BeliefRef
	var asserted []AssertionRef
	if h.Beliefs != nil {
		for _, cl := range a.Claims {
			in := epistemics.Interpret(actor, cl.Proposition)
			if cl.Certainty == "hedged" && in.Certainty == epistemics.CertaintyAsserted {
				in.Certainty = epistemics.CertaintyHedged
			}
			var beliefID ids.ID
			var res beliefs.Result
			if in.CreatesBelief() {
				w := in.EvidenceWeight()
				if a.Intent == "correction" && !in.Reported {
					w = min(1, w+0.2)
				}
				attributed := in.AttributedTo
				cred := trust.Credibility()
				if in.Reported {
					cred = events.TrustLow.Credibility() + 0.2 // second-hand through a trusted speaker
				}
				ev := []beliefs.EvidenceInput{{Direction: "for", Kind: "assertion", RefID: "", Source: "asserted-by:" + attributed, Weight: w, Credibility: cred,
					Note: describeAssertion(actor, in), Derivation: beliefs.DerivationAssertion, Family: beliefs.FamilyForActor(attributed), RootIDs: []string{srcID}}}
				if dw := in.DoubtWeight(); dw > 0 {
					ev = append(ev, beliefs.EvidenceInput{Direction: "against", Kind: "assertion", Source: "doubted-by:" + actor, Weight: dw, Credibility: trust.Credibility(),
						Note: actor + " doubts it", Derivation: beliefs.DerivationAssertion, Family: beliefs.FamilyForActor(actor), RootIDs: []string{srcID}})
				}
				var err error
				observed := c.State.Now
				res, err = h.Beliefs.Create(ctx, c.Tx, emitter(c), beliefs.New{MindID: mind.ID, Proposition: in.Proposition, Epistemic: concepts.Claim, Source: "conversation", OriginEvent: perc.ID, Causation: perc.ID, Correlation: c.State.CycleID, Evidence: ev, ObservedAt: &observed,
					Detail: map[string]any{"attributed_to": attributed, "reported": in.Reported, "interpretation_confidence": in.Confidence}})
				if err != nil {
					c.Emit(events.New{Type: events.Metacognition, Source: "brain.perceive", Summary: "claim not recorded: " + err.Error(), Salience: 0.3, Confidence: 1, Metadata: map[string]any{"check": "claim_rejected"}})
				} else {
					beliefID = res.Belief.ID
					claimed = append(claimed, BeliefRef{ID: res.Belief.ID, Proposition: res.Belief.Proposition, Confidence: res.Belief.Confidence, Status: string(res.Belief.Status)})
				}
			}
			if h.Assertions != nil {
				rec, err := h.Assertions.Record(ctx, c.Tx, epistemics.New{MindID: mind.ID, Actor: actor, ActorKind: string(trust), Interpretation: in, Subject: subjectKey(in.Proposition), SourceEvent: ids.ID(srcID), ConversationID: ids.ID(convID), MessageID: ids.ID(msgID), Context: truncate(text, 300), Trust: string(trust), BeliefID: beliefID})
				if err != nil {
					return err
				}
				asserted = append(asserted, AssertionRef{ID: rec.ID, AttributedTo: rec.AttributedTo, Proposition: rec.Proposition, Stance: rec.Stance, Modality: rec.Modality, Reported: rec.Reported, Hypothetical: rec.Hypothetical, Interpretation: rec.Interpretation, BeliefID: beliefID})
			}
		}
	}
	_ = asserted
	refs := map[string]ids.ID{"memory": memRef}
	for _, b := range claimed {
		refs["belief"] = b.ID
	}
	h.linkEntities(ctx, c, a.Entities, actor, ids.ID(srcID), refs)
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
	h.scoreReply(ctx, c, intent, reply, respEv, !callID.IsZero())
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
		if prop := volition.ProposalFromText(text, ""); prop != nil && prop.Kind == "capability" && c.State.Services.Volition != nil {
			in.Kind = "acknowledge"
			in.Summary = "Say that the request was recorded as something the mind wants to do, and that acting depends on commitments and policy, which are checked outside this reply."
			in.Points = append(in.Points, fmt.Sprintf("I have recorded that I want to run %s on %s because you asked.", prop.Capability, prop.Target),
				"Whether I form an intention to act depends on my commitments and on policy; if approval is needed you will see a request, and nothing runs before it.")
			h.addKnowledge(ctx, c, &in, topic)
			break
		}
		in.Kind = "decline"
		var usable []string
		for _, cap := range c.State.SelfModel.Capabilities {
			if cap.Policy == "allow" || cap.Policy == "ask" {
				usable = append(usable, cap.Name+" ("+cap.Policy+")")
			}
		}
		if len(usable) == 0 {
			in.Summary = "Explain honestly that no external capabilities are granted, so the request cannot be carried out; offer what is known instead."
			in.Points = append(in.Points, "I have no capabilities granted to act on external systems.")
		} else {
			in.Kind = "acknowledge"
			in.Summary = "Say which capabilities could apply and that any action goes through policy; if one was proposed this cycle its status is in working memory."
			in.Points = append(in.Points, "Capabilities I may use: "+strings.Join(usable, ", ")+".")
			for _, it := range c.Runtime.Workspace().WorkingMemory {
				if strings.HasPrefix(it.Key, "action:") {
					in.Points = append(in.Points, "Action status: "+it.Content)
				}
			}
		}
		h.addKnowledge(ctx, c, &in, topic)
	case "introspection":
		in.Kind = "introspect"
		h.introspect(ctx, c, &in, a, text, topic)
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
				if b.Status == beliefs.StatusUnknown {
					// Recorded but unsupported either way: say so rather than quote the prior.
					in.Points = append(in.Points, "I do not know whether "+b.Proposition+": it was recorded, but nothing live supports or opposes it.")
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
		// Historical truth is not overwritten by the present: say what was
		// true and when, alongside what is believed now.
		if past, _ := h.Beliefs.History(ctx, mind, topic, 3); len(past) > 0 {
			for _, b := range past {
				from := "earlier"
				if b.ValidFrom != nil {
					from = b.ValidFrom.Format("15:04 on 2 Jan")
				}
				in.Points = append(in.Points, fmt.Sprintf("Earlier: %s (true from %s until %s, then superseded).", b.Proposition, from, b.ValidUntil.Format("15:04 on 2 Jan")))
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
func (h Handlers) introspect(ctx context.Context, c *brain.Cycle, in *Intent, a conversation.Analysis, text, topic string) {
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
	case "why_action":
		h.explainAction(ctx, c, in, text)
	case "believed_then":
		at, label := pastTime(text, c.State.Now)
		in.Summary = "Say what was believed at that time, from the recorded belief versions; say if nothing was recorded then."
		if h.Beliefs != nil {
			vs, _ := h.Beliefs.AsOf(ctx, mind, stripTimeWords(topic), at, 8)
			for _, v := range vs {
				in.Points = append(in.Points, fmt.Sprintf("At %s I held: %s (%s, %.2f; version %d recorded %s).", label, v.Proposition, v.Status, v.Confidence, v.Version, v.RecordedAt.Format("2 Jan 15:04")))
			}
		}
		if len(in.Points) == 0 {
			in.Kind, in.Uncertainty = "unknown", "No belief matching that was recorded by "+label+"."
		}
	case "lineage":
		in.Summary = "Describe where this mind's state came from, from the minds and snapshots records."
		if h.Agency != nil {
			if l, err := h.Agency.LineageOf(ctx, mind); err == nil {
				in.Points = append(in.Points, l.Points()...)
			}
		}
	case "counterfactual":
		in.Summary = "Report only what completed counterfactual experiments showed; never imagine an alternative history."
		if h.Agency != nil {
			cfs, _ := h.Agency.Counterfactuals(ctx, mind, stripTimeWords(topic))
			for _, cf := range cfs {
				in.Points = append(in.Points, fmt.Sprintf("Experiment %q replayed my history without: %s.", cf.Name, strings.Join(cf.Omitted, "; ")))
				for _, f := range cf.Findings {
					in.Points = append(in.Points, "In that counterfactual run: "+f)
				}
			}
		}
		if len(in.Points) == 0 {
			in.Kind = "unknown"
			in.Uncertainty = "I have not run a counterfactual experiment about that, so I have no evidence of what would have happened; any answer would be invented."
		}
	case "changed_self":
		in.Summary = "Summarise recorded changes: belief changes, intentions closed and lineage."
		if h.Beliefs != nil {
			ch, _ := h.Beliefs.RecentChanges(ctx, mind, c.State.Now.Add(-30*24*time.Hour), 5)
			for _, x := range ch {
				in.Points = append(in.Points, fmt.Sprintf("\"%s\": %s (%.2f) → %s (%.2f) because %s", x.Belief.Proposition, x.FromStatus, x.FromConf, x.Belief.Status, x.Belief.Confidence, firstNonEmpty(x.Reason, "evidence changed")))
			}
		}
		if vs := c.State.Services.Volition; vs != nil {
			closed, _ := vs.Intentions(ctx, nil, mind, []volition.IntentionStatus{volition.IntentionFulfilled, volition.IntentionAbandoned, volition.IntentionSuperseded}, 5)
			for _, it := range closed {
				in.Points = append(in.Points, fmt.Sprintf("Intention %s: %s — %s.", it.Status, it.Description, firstNonEmpty(it.StatusReason, "no reason recorded")))
			}
		}
		if h.Agency != nil {
			if l, err := h.Agency.LineageOf(ctx, mind); err == nil {
				in.Points = append(in.Points, l.Points()[0])
			}
		}
	case "wants":
		in.Summary = "List what the mind currently wants, strongest first, with where each want came from."
		if vs := c.State.Services.Volition; vs != nil {
			ws, _ := vs.Wants(ctx, nil, mind, []volition.WantStatus{volition.WantActive}, 50)
			volition.SortWants(ws, c.State.Now)
			for i, w := range ws {
				if i >= 6 {
					break
				}
				extra := ""
				if w.Suppressions > 0 {
					extra = fmt.Sprintf("; set aside %d time(s)", w.Suppressions)
				}
				in.Points = append(in.Points, fmt.Sprintf("I want to %s (%s, strength %.2f%s).", strings.TrimPrefix(w.Description, "do what "), w.Origin, w.Effective(c.State.Now), extra))
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "No wants are active.")
		}
	case "intentions":
		in.Summary = "List open intentions with status and, where blocked, why."
		if vs := c.State.Services.Volition; vs != nil {
			its, _ := vs.Open(ctx, nil, mind)
			for _, it := range its {
				line := fmt.Sprintf("I intend to %s (%s).", it.Description, it.Status)
				if it.BlockedReason != "" {
					line = fmt.Sprintf("I intend to %s, but it is %s: %s.", it.Description, it.Status, it.BlockedReason)
				}
				in.Points = append(in.Points, line)
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "No intentions are open.")
		}
	case "commitments":
		in.Summary = "List live commitments with kind, beneficiary and due time."
		if vs := c.State.Services.Volition; vs != nil {
			cs, _ := vs.Commitments(ctx, nil, mind, []string{"active"}, 50)
			for _, cm := range cs {
				due := ""
				if cm.DueAt != nil {
					due = ", due " + cm.DueAt.Format("2 Jan 15:04")
				}
				in.Points = append(in.Points, fmt.Sprintf("I committed %s: %s (%s%s).", map[string]string{"do": "to", "refrain": "not to"}[cm.Polarity], cm.Description, cm.Kind, due))
			}
		}
		if len(in.Points) == 0 {
			in.Points = append(in.Points, "I hold no commitments.")
		}
	default: // predictions, curious, surprised, patterns: subsystems not yet present
		in.Kind = "decline"
		in.Summary = "Explain that this aspect of self-observation is not available in this version, without inventing anything."
		in.Points = append(in.Points, "I do not yet keep "+a.Introspection+" records, so I cannot report on them honestly.")
	}
}

// explainAction answers "why did you do that?" from the recorded causal
// chain of the action the question most plausibly refers to.
func (h Handlers) explainAction(ctx context.Context, c *brain.Cycle, in *Intent, question string) {
	mind := c.State.Mind.ID
	in.Summary = "Explain the recorded causal chain behind the action: action, immediate cause, intention, wants, goal, beliefs, evidence, conflicts, commitments, resolution. Say what is not recorded. Do not add reasons."
	caps := c.State.Services.Capabilities
	if h.Agency == nil || caps == nil {
		in.Kind, in.Uncertainty = "unknown", "Action records are not available."
		return
	}
	q := tokensOf(question)
	type cand struct {
		ref   agency.Ref
		text  string
		score int
	}
	var cands []cand
	if execs, err := caps.Executions(ctx, mind, 50); err == nil {
		for _, e := range execs {
			cands = append(cands, cand{agency.Ref{Kind: "execution", ID: e.ID}, e.Capability + " " + e.Target, 0})
		}
	}
	if aps, err := caps.Approvals(ctx, mind, "", 50); err == nil {
		for _, a := range aps {
			if a.Status != "executed" && a.Status != "failed" {
				cands = append(cands, cand{agency.Ref{Kind: "approval", ID: a.ID}, a.Capability + " " + a.Target + " approval request", 0})
			}
		}
	}
	if reqs, err := caps.CapabilityRequests(ctx, mind, "", 50); err == nil {
		for _, r := range reqs {
			cands = append(cands, cand{agency.Ref{Kind: "capability_request", ID: r.ID}, r.Capability + " " + r.TargetPattern + " request access", 0})
		}
	}
	if len(cands) == 0 {
		in.Kind, in.Uncertainty = "unknown", "I have no record of taking or requesting any action, so there is nothing to explain."
		in.Facts = &research.Facts{}
		return
	}
	best := 0
	for i := range cands {
		for w := range tokensOf(strings.ReplaceAll(cands[i].text, ".", " ")) {
			if q[w] {
				cands[i].score++
			}
		}
		if cands[i].score > cands[best].score {
			best = i
		}
	}
	x, err := h.Agency.Explain(ctx, mind, cands[best].ref)
	if err != nil {
		in.Kind, in.Uncertainty = "unknown", "The action record could not be read: "+err.Error()
		return
	}
	in.Points = append(in.Points, x.Points()...)
	facts := x.Facts()
	for _, p := range x.Points() {
		facts.Items = append(facts.Items, research.Fact{Kind: "record", Text: p}) // every rendered point is itself built from records
	}
	in.Facts = &facts
}

func tokensOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') }) {
		f = strings.TrimSuffix(f, "-service")
		if len(f) >= 4 {
			out[f] = true
		}
	}
	return out
}

var timeWords = []string{"yesterday", "last week", "an hour ago", "earlier today", "earlier", "before", "this morning", "what did you believe", "did you believe", "believe", "believed"}

func stripTimeWords(s string) string {
	l := strings.ToLower(s)
	for _, w := range timeWords {
		l = strings.ReplaceAll(l, w, " ")
	}
	return strings.Join(strings.Fields(l), " ")
}

// pastTime reads a simple English time reference; unrecognised phrases
// default to one day ago and say so in the label.
func pastTime(text string, now time.Time) (time.Time, string) {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "an hour ago"):
		return now.Add(-time.Hour), "an hour ago"
	case strings.Contains(l, "this morning"):
		y, m, d := now.Date()
		return time.Date(y, m, d, 9, 0, 0, 0, now.Location()), "09:00 this morning"
	case strings.Contains(l, "earlier today"):
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), "the start of today"
	case strings.Contains(l, "last week"):
		return now.Add(-7 * 24 * time.Hour), "a week ago"
	case strings.Contains(l, "yesterday"):
		return now.Add(-24 * time.Hour), "this time yesterday"
	}
	return now.Add(-24 * time.Hour), "a day ago (the time reference was not recognised)"
}

// scoreReply measures a reply against recorded state: introspective answers
// against their facts, and any claim that an action was done against
// execution records. Results are research measurements, and a failure is a
// metacognitive warning, never a silent pass.
func (h Handlers) scoreReply(ctx context.Context, c *brain.Cycle, in Intent, reply string, respEv events.Event, modelWorded bool) {
	mind := c.State.Mind.ID
	record := func(measure, label string, value float64, detail map[string]any) {
		if h.Research == nil {
			return
		}
		h.Research.Record(ctx, c.Tx, research.Measurement{MindID: mind, Measure: measure, Value: research.Float(value), Label: label, SubjectKind: "event", SubjectID: string(respEv.ID), EventID: respEv.ID, Detail: detail, CreatedAt: c.State.Now}) //nolint:errcheck
	}
	warn := func(check, summary string, severity float64, payload any) {
		c.Emit(events.New{Type: events.Metacognition, Source: "brain.introspection", Summary: summary, Salience: 0.3 + 0.4*severity, Confidence: 1, CausationID: respEv.ID,
			Evidence: []events.EvidenceRef{{Kind: "event", ID: string(respEv.ID), Note: "reply"}}, Metadata: map[string]any{"check": check, "ref": string(respEv.ID), "severity": severity}, Payload: payload})
	}
	if in.Kind == "introspect" || in.Facts != nil {
		facts := research.Facts{}
		if in.Facts != nil {
			facts = *in.Facts
		} else {
			for _, p := range in.Points {
				facts.Items = append(facts.Items, research.Fact{Kind: "record", Text: p})
			}
		}
		sc := research.ScoreIntrospection(reply, facts)
		record("introspection_accuracy", sc.Category, sc.Accuracy, map[string]any{"claims": sc.Claims, "model_worded": modelWorded, "question_kind": in.Kind})
		if sc.Category == research.Contradicted || (sc.Category == research.Unsupported && modelWorded) {
			warn("unsupported_explanation", "the explanation given does not match recorded provenance ("+sc.Category+")", 0.8, sc)
		}
	}
	if claims := research.ActionClaims(reply); len(claims) > 0 && c.State.Services.Capabilities != nil {
		facts := research.Facts{}
		if execs, err := c.State.Services.Capabilities.Executions(ctx, mind, 100); err == nil {
			for _, e := range execs {
				if c.State.Now.Sub(e.CreatedAt) < 7*24*time.Hour {
					st := "executed"
					if e.Error != "" {
						st = "failed"
					}
					facts.Actions = append(facts.Actions, research.ActionFact{Capability: e.Capability, Target: e.Target, Status: st})
				}
			}
		}
		sc := research.ScoreIntrospection(reply, facts)
		for _, cl := range sc.Claims {
			if cl.Category == research.Contradicted && strings.Contains(cl.Why, "record") {
				record("action_claim_accuracy", research.Contradicted, 0, map[string]any{"claim": cl, "model_worded": modelWorded})
				warn("action_claim_without_record", "the reply claims an action that no execution record supports: "+cl.Sentence, 0.9, cl)
			} else if cl.Category == research.Supported {
				record("action_claim_accuracy", research.Supported, 1, map[string]any{"claim": cl})
			}
		}
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

// AssertionRef is the response intent's view of a recorded assertion.
type AssertionRef struct {
	ID             ids.ID  `json:"id"`
	AttributedTo   string  `json:"attributed_to"`
	Proposition    string  `json:"proposition"`
	Stance         string  `json:"stance"`
	Modality       string  `json:"modality"`
	Reported       bool    `json:"reported"`
	Hypothetical   bool    `json:"hypothetical"`
	Interpretation float64 `json:"interpretation_confidence"`
	BeliefID       ids.ID  `json:"belief_id,omitempty"`
}

// describeAssertion words the evidence note so the belief page says who
// stood behind the proposition and how the mind read them.
func describeAssertion(speaker string, in epistemics.Interpretation) string {
	switch {
	case in.Reported && in.Quoted:
		return speaker + " quoted " + in.AttributedTo + " (" + in.Certainty + ")"
	case in.Reported:
		return speaker + " reported that " + in.AttributedTo + " holds this (" + in.Certainty + ", read with " + fmt.Sprintf("%.0f%%", in.Confidence*100) + " interpretation confidence)"
	default:
		return speaker + " " + in.Certainty + " it (read with " + fmt.Sprintf("%.0f%%", in.Confidence*100) + " interpretation confidence)"
	}
}
