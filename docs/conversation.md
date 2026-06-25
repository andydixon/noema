# Conversation

A message to a mind is never forwarded to a model. It goes through the cognitive cycle (spec §38):

```
message ──▶ observation event (trust: user/operator, channel, actor, reference)
        ──▶ perceive process: deterministic analysis (intent, entities, claims, urgency),
            optionally enriched by the perceive prompt → "perceive" candidate
        ──▶ attention competition (with recall and association candidates)
        ──▶ perceive handler: perception event, episodic memory, claims → beliefs,
            response intent built from beliefs and memories, reply wording,
            response_generated event citing everything used
        ──▶ message row (role: mind) linked to the event, the intent and the model call
```

## Intents

The deterministic analyser classifies each message as question, statement, request, correction, greeting or **introspection**. Introspective questions are answered from state, not invented:

| Question | Source of the answer |
|---|---|
| What are you thinking about? | workspace and working memory |
| What do you believe about X? | belief search plus memory retrieval for X |
| Why do you believe that? | the evidence rows of the best-matching belief |
| What are you uncertain about? | beliefs in the 0.3–0.7 band or contested |
| Which beliefs contradict each other? | contested beliefs and their contradiction links |
| What did you learn this week? | memory and belief creation events in the last seven days |
| What have you changed your mind about? | beliefs whose status changed, with the recorded reason |
| predictions / curiosity / surprise / patterns | declined honestly until those subsystems exist |

Statements and corrections become **claims**: beliefs with the message as evidence (weight by certainty, credibility by trust). Contradictions with existing beliefs are detected and reported in the reply. Requests are declined while the mind has no capabilities, citing the self-model.

## Wording

If a model is assigned to the `respond` role and the per-cycle budget allows, the `respond` prompt receives the personality brief, the intent, the cited beliefs and memories, the uncertainty note and the self-model limits, with the user message in a data block. Otherwise the reply is a direct, plain rendering of the intent with confidences shown and a note that no model was used. Either way the stored intent lets an operator check that the words match the evidence.

## Provenance

The `response_generated` event cites the observation, the perception, every belief and memory used, the episode written, and the model call. The chat page shows "why this reply" under each answer and links each turn to its event.
