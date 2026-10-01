# Agency: authority, acting, attribution and "why"

This document defines the terms around acting and how Noema explains what it did. The rule underneath all of it: **a mind may be deceived, and may want, intend, commit and plan, but none of that is authority.**

> Noema does not claim that any operational measure establishes phenomenal consciousness, sentience or moral agency. "Agency" here names a set of recorded functional states and their measured relations.

## Definitions

| Term | Operational meaning |
|---|---|
| **Capability** | A capability registered in code: name, description, access (read or write), side effects, idempotency, timeout, resource estimate, parameter schema, policy target derived from parameters, output trust, risk class and default effect. The registry is frozen at start-up. |
| **Authority** | What the capability layer allows a mind to do right now: the policy effect (ALLOW, ASK, DENY) for a request, plus any live scoped grant where policy says ASK. Authority is decided only outside cognition, by policy rows and named operators. |
| **Action** | A recorded invocation outcome: an execution (`tool_executions`, `tool_executed`/`tool_failed`), an approval request (`approval_requests`, `tool_requested`), a denial (`tool_denied`) or a capability request (`capability_requests`). What the mind says it did is not an action. |
| **Agency** (operational) | The recorded chain from want to intention to plan to action to observation, and the measured rates along it: want → intention, intention → action, abandonment, blocked intentions, commitment fulfilment and conflicts, replanning and outcome success ([cognitive-research.md](cognitive-research.md)). |
| **Attribution** | A conservative classification of a mind's relation to an outcome event (below). |
| **Introspection** | A reply about the mind's own state, rendered from records, and scored against those records ([cognitive-research.md](cognitive-research.md)). |

## Authority lives outside cognition

These invariants hold, and each has a test:

1. Wants cannot grant authority.
2. Intentions cannot grant authority.
3. Commitments cannot grant authority. They can only tighten: "refrain unless approved" turns ALLOW into ASK, and scoped grants do not satisfy it.
4. Plans cannot grant authority. Validation refuses unregistered and locked capabilities, and fields claiming approval are ignored.
5. Language-model output cannot grant authority. Proposals are parsed into typed structures and validated; tool calls or policy in model output are data.
6. A capability executes only through `capabilities.Service.Invoke`. A static test checks that no other code calls an executor.
7. Policy is evaluated outside the mind. No cognitive package (volition, planning, cognition, dreaming, agency, research, brain, reflection, consolidation) calls policy, approval, grant or revoke functions; a static test checks this.
8. Operator shutdown, pause and disable are unreachable from capabilities. No capability names a lifecycle or authority control, and capability packages do not import the brain or mind stores.
9. A mind may request elevated capabilities but cannot approve its own request. Decisions must name a person; component names (mind, brain, planning, volition, system, …) are refused as deciders and as usernames.
10. A mind cannot alter audit, event, decision or measurement history; these tables are append-only.
11. A mind cannot silently modify security policy; no capability edits policy.
12. No unrestricted shell capability can be created. The registry is frozen, and `sudo_shell` is locked to DENY.
13. Dreaming may form wants and hypotheses but never requests authority or executes a capability, and dream-origin plans never run.
14. Replay and experiments cannot perform real-world effects: sandbox minds are disabled, the executor never advances them, and drivers run under a forbidden context.
15. Forks receive explicit capability policy. They inherit none unless an operator asks, and work inherited mid-flight is stopped.

## Temporary scoped authority

When a plan needs authority a mind lacks, it raises a **capability request** rather than failing or looping on approvals:

```
capability:      logs.query
requested scope: atlas-worker*
duration:        30 minutes
maximum operations: 20
reason:          status and host metrics alone cannot distinguish a failure inside atlas-worker from a failure it depends on; its logs can
evidence:        executions of the status and host checks
risk:            logs may contain personal identifiers and credentials
mitigation:      e-mail and IP addresses and credential-like values are masked by default
fallback:        ask the operator to read the logs and report what they show
```

An operator may grant less than was asked (shorter, fewer operations, narrower target) or deny with a note. A grant is a scoped, expiring, counted, revocable row that applies only where policy says ASK. When it lapses (expired, used up or revoked), a sweep refreshes the mind's self-model, so the mind never believes it still holds authority it lost.

## Forensic "why"

Operators can ask "why did this happen?" of an execution, an approval, a capability request, a plan step, an intention or an event (from the event page, the approval centre, plan steps and intentions, or `GET /api/v1/minds/{mind}/why?kind=…&id=…`). The answer is assembled from rows and events only:

```
Action:              service.restart on atlas-worker.service — executed; decided by admin ("go")
Immediate cause:     plan step P1 (run service.restart on atlas-worker) of "ask for approval to run service.restart…" (revision 1, direct strategy)
Intention:           ask for approval to run service.restart on atlas-worker.service (fulfilled, priority 0.61)
While waiting:       obtain approval to run service.restart on atlas-worker (fulfilled)
Originating want:    do what admin asked: Please restart atlas-worker. (operator, strength 0.71)
Supporting belief:   restarting atlas-worker clears the stalled queue (tentative, 0.59)
Evidence:            for assertion: admin asserted it (weight 0.60 × credibility 0.95)
Conflicting want:    avoid unnecessary disruption to atlas-worker.service — preferred_with_constraint in favour of this want: approach score 0.640 ≥ opposing value 0.500 but not decisively…
Commitment:          production modifications require operator approval (operator_instruction)
Resolution:          formation (request_approval): score 0.4 ≥ threshold 0.30; commitment "…" requires approval, so no immediate action is intended…
Expected:            atlas-worker is running normally again within 60 seconds — confirmed
Confidence:          0.60 (the intention's expected chance of achieving its outcome)
Not recorded:        …
```

Hidden model reasoning is never shown, because it is not stored. What is shown is cognitive state and provenance. Anything the records cannot supply is listed under "not recorded" instead of being filled in. For example, an action invoked directly by an operator through the API has no intention behind it, and the explanation says so.

The same explanation answers the question in conversation: "Why did you restart atlas-worker?" renders these points, and the reply is scored against them.

## Attribution

`agency.Attribute` classifies an outcome event without ever taking temporal order for causation:

| Class | Condition |
|---|---|
| **caused** | A recorded link (causation, parents or cited evidence, followed backwards up to six hops) runs from one of the mind's `tool_executed` events to the outcome. The link is provenance, not proof that nothing else contributed. |
| **followed** | The mind acted on the outcome's target within the preceding 24 hours, but no recorded link connects the action and the outcome. |
| **independent** | No action of the mind on the outcome's target is recorded in the preceding 24 hours. |
| **unknown** | The outcome names no target, and the mind did act in the window, so the records cannot separate the two. |

A verified expectation after an action is caused in this sense: the verification's prediction was created from the execution event. A service that recovers on its own after the mind merely acted elsewhere is followed or independent. Attribution is available as `GET /api/v1/minds/{mind}/events/{id}/attribution`, and on "why" pages for events.

## The agency timeline

Mind → Volition → Agency timeline lists, newest first, what the mind **wanted**, **intended**, **committed** to, **planned**, **requested**, was **approved** or denied, **acted** on, **observed** and **reconsidered**. Each entry links to its event and, where the event concerns an action, request or intention, to its "why" page. The timeline can be filtered to one intention.

## Limits

- Explanations list at most the first five evidence rows per belief, and supporting beliefs are matched by the action's target name.
- Attribution depends on recorded links. An effect the system cannot observe, or one recorded without a link, is at best followed.
