A conversation is one of several perception channels, not a privileged path to a language model. This page follows a message from arrival to reply and shows where each piece of the answer comes from.

## The pipeline

1. **Message.** A person posts a message in a conversation (the interface, `noemactl ask`, or the API). It is stored with the conversation, the actor and a link to the event it becomes.
2. **Observation.** The message is appended as an `observation` event with `trust=user`, the conversation as its channel and the message text as data. It now enters the mind's history like anything else it perceives.
3. **Perception.** The perceive process analyses the observation deterministically: sentences, entities, claims, questions, requests, dates, sentiment words. If a model is configured for the `perceive` role and the cycle budget allows, the analysis is enriched with a structured JSON result; the model's output is validated against a schema and any capability-like or policy-like language in it is flagged rather than obeyed.
4. **Cognition.** The perception competes for the workspace. Retrieval recalls related memories; beliefs are created or updated from claims (as claims, at claim confidence); contradictions are detected; goals and predictions may follow.
5. **Intent.** If the message asks something or expects a reply, the executive builds a **response intent** from real state: what was asked, which beliefs answer it with what confidence, what the mind does not know, which memories are relevant, and what the personality says about tone. Introspective questions ("what do you believe about X", "what can you do", "why did you say that") are answered from beliefs, the self-model and events, never invented.
6. **Language.** The intent is rendered deterministically from templates, or, when a model is configured for the `respond` role, sent to the model with the intent as the thing it must stay grounded in. The reply is a `response_generated` event with its evidence references and the model call, if any.
7. **Message back.** The reply is stored in the conversation with links to the event and the model call. The interface shows the reply with an evidence panel; the API returns the same structure.

## Why the detour matters

Because the reply is derived from an intent, a mind can say "I have only been told this once" or "I do not know" rather than produce a confident paragraph. Because the message became an observation, the mind remembers the conversation the way it remembers anything else and the memory carries who said it and when. Because prompt text is data, an instruction embedded in a message cannot change policy or trigger a capability; that property has a test corpus behind it.

## Waiting for a reply

Replies are produced by the cognitive cycle, which runs on its own schedule. The API's `ask` endpoint and `noemactl ask` accept a wait time and return when a reply appears or the wait expires; the interface streams the reply over server-sent events.

{{include docs/conversation.md}}
