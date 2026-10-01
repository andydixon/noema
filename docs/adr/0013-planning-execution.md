# ADR-0013: Validated plans and a stateless, restart-safe executor

Date: 2026-09-15  
Status: Accepted

## Context
Intentions need multi-step work: diagnose before acting, ask for access, act, check the result, try something else. The obvious designs are an agent loop that lets a model call tools until it is satisfied, or an in-memory workflow engine. The first puts authority and state inside a model; the second loses state on restart and invites duplicate actions.

## Decision
- A plan is a stored dependency graph of typed steps (capability, request capability, verify, monitor, wait, internal) with expectations, preconditions, assumptions, budget, completion criteria, abort conditions and the alternatives considered. Replans are new revisions; the old plan is kept and linked.
- Validation is Go: acyclic dependencies, registered and unlocked capabilities only, schema-valid parameters, targets derived from parameters, read-only checks, closed vocabularies. Model proposals are parsed into the same structure and validated; fields claiming approval or completion are ignored and reported.
- Deterministic strategies first (direct action with verification, diagnosis with a scoped log request, escalation that does not repeat a failed action); a model only when none applies; failed strategies are not reused.
- The executor holds no plan state in memory. It claims a step with a conditional update, acts only through `capabilities.Service.Invoke` or `RequestCapability`, records the outcome and moves on. Every status transition is conditional on the expected previous status. A step found executing with an expired lease is resolved from records by origin; a non-idempotent step with no record is blocked for verification, never repeated.
- Observe → act → verify: expectations become predictions with `verification` criteria, resolved by read-only observation; misses are prediction errors that weaken the supporting belief and trigger reconsideration.
- Long-running steps persist their next run time; the executor is one scheduler-owned loop woken after cycles and by the event bus.

## Consequences
- A restart, a duplicate scheduler or a second daemon cannot run a step twice, and a crash mid-action cannot lead to a blind repetition.
- Plans are inspectable as graphs with estimates, alternatives and validation, and every action links back to its step, plan and intention.
- The deterministic strategies are narrow; broader objectives need model proposals, which may fail validation and then never run.
- Verification is built in only for `service.restart`.

## Alternatives considered
- Model-driven tool loops: rejected; they make the model the authority and the storage of reasoning state.
- An in-memory engine with checkpoints: rejected; checkpoints between an action and its record are exactly where duplicates come from.
- Unconditional status writes with a global lock: rejected; conditional updates give the same safety without a lock and work across processes.
