# Planning and execution

Plans turn intentions into explicit, revisable work. A language model may propose a decomposition. Go code owns validation, dependency integrity, the capability vocabulary, policy (through the capability layer), resource accounting, execution state, retries, deadlines and approvals. A plan is neither a promise nor a grant of authority.

## Plans

A plan belongs to one intention and records:

- **objective**, **origin** (template, model, operator, dream, replan) and **strategy**;
- **steps**, ordered and linked by dependencies into a directed acyclic graph;
- **assumptions**: beliefs the plan relies on, with a minimum confidence;
- **completion criteria** and **abort conditions**;
- **risk**, an **estimate** (capability invocations, operator approvals, wall time, network operations, money, tokens), a **budget** and the resources **actually** used;
- the **alternatives** considered, each with an estimate, expected confidence, score and why it was or was not selected;
- the **validation** result with errors and warnings;
- **revision**, and links to the revision it superseded and the one that superseded it.

Plan statuses: `draft`, `validated`, `waiting`, `executing`, `blocked`, `awaiting_approval`, `replanning`, `completed`, `failed`, `abandoned`, `superseded`.

### Step kinds

| Kind | What it does |
|---|---|
| `capability` | Invokes one registered capability with validated parameters. May carry an **expectation** (what should follow, with a confidence and a time window) and a **precondition** (a read-only check that must hold first). |
| `request_capability` | Raises a structured capability request (capability, target pattern, parameter limits, duration, operation cap, reason, risk, mitigation, fallback, alternatives) and waits for an operator. |
| `verify` | Resolves another step's expectation by observing the world through a read-only capability, repeatedly within the window. |
| `monitor` | Repeats a read-only check until it has held continuously for a stable period, or fails at a deadline ("monitor atlas until stable for 15 minutes"). |
| `wait` | Pauses a branch for a period. |
| `internal` | A deterministic internal action from a closed vocabulary. Today that is `summarise_findings`: the results of its dependencies become one observation that re-enters cognition. |

Step statuses: `pending`, `ready`, `executing`, `awaiting_approval`, `awaiting_grant`, `waiting`, `completed`, `failed`, `skipped`, `blocked`, `cancelled`.

## Validation

`planning.Validate` trusts nothing in a draft:

- one to thirty steps, unique short keys, titles present;
- dependencies refer to steps of the plan, no step depends on itself, and the graph is acyclic (Kahn's algorithm; a cycle names the steps involved);
- every capability is **registered and not locked**; parameters pass the capability's schema (unknown parameters are rejected); targets agree with parameters;
- checks (verify, monitor, precondition) use **read-only** capabilities and a known comparison;
- a verify step must depend on the step whose expectation it verifies, and that step must have one;
- kinds and internal actions come from closed vocabularies;
- writing, non-idempotent steps are limited to one attempt;
- a writing step without an expectation produces a warning.

A draft that fails validation is stored as a `draft` with its errors, and the executor never runs drafts.

Model proposals are parsed into the same structure. Fields that could carry authority or claim progress (`authorised`, `approved`, `status`, `completed`, `policy`, `require_approval: false`) are not part of it. They are listed as ignored in the plan's validation warnings, and approval requirements come only from commitments and policy.

## The planner

Deterministic strategies come first, in preference order. A strategy that failed in an earlier revision of the same intention is not used again.

| Strategy | For | Steps |
|---|---|---|
| `direct` | an intention with a capability as means | P1 the capability (requiring approval when the intention does); for `service.restart`, an expectation that the unit is running normally within 60 s and V1 a verify step using `service.status` |
| `diagnose` | an investigation of a service-like subject | status, host memory, disk, and logs, each only if policy allows or asks; if log access needs approval and no grant is live, R1 requests 30 minutes and 20 operations of `logs.query` on the unit, with the risk (logs may contain personal identifiers), mitigation (masking on by default) and fallback (ask the operator); then S1 summarises the findings |
| `diagnose_then_escalate` | after a direct service action failed | the diagnosis above, then N1 a note to the operator saying what was tried and seen, without repeating the action |
| `model` | when nothing else applies and a model is assigned to the `plan` role | whatever the model proposes, validated |

When no strategy applies, planning fails with "no possible plan". A freshly formed intention is then abandoned (its want suppressed), and a replan ends the intention.

Alternatives are scored `expected confidence − 0.05 × approvals − 0.001 × wall seconds − 0.01 × money` and recorded with the plan. The operator therefore sees what was considered, what it would have cost and why the chosen one was chosen.

## The executor

The executor holds no plan state in memory. It is started once by the scheduler, is woken after cycles that formed or resumed intentions and by approvals, grants and executions on the event bus, and otherwise ticks every fifteen seconds. Every tick reads the database.

For each open plan of an **active** mind (disabled sandbox minds used by replay and experiments are never advanced), it moves steps one at a time:

1. **pending → ready** when all dependencies completed or were skipped; **cancelled** when one failed. This transition, like every other, is a conditional update, so a stale copy can never move a step backwards.
2. **ready → executing** by a conditional update that only one executor can win, with a lease of the capability's timeout plus thirty seconds.
3. The step acts only through `capabilities.Service.Invoke` (or `RequestCapability`). The invocation names the step as its origin, correlates with the intention, cites the intention, plan and step as evidence, and carries an explanation for any approver: what, why, the expected outcome, side effects and the alternatives considered. `RequireApproval` is set when the step or a commitment-bound intention requires it for a writing capability.
4. The outcome is adopted:
   - **executed**: the step completes; its expectation becomes a prediction with `verification` criteria and a window, linked to the execution event and to the supporting belief;
   - **pending**: the step awaits approval, the intention is blocked and a subordinate intention formed;
   - **denied**: the step fails and the intention is reconsidered as a capability change;
   - **failed**: an idempotent step retries with backoff up to its attempts; otherwise it fails.
5. Approval and request steps are polled. **Approved and executed** resumes the parent, fulfils the subordinate and completes the step. **Denied** fails it with the note. **Expired or withdrawn** fails it as an expired approval.

### Observe → act → verify

An HTTP 200 or a command returning is never taken as the outcome. `service.restart` reports "restart requested (health not yet verified)". The verify step then observes. A check never raises an approval request: if policy would not simply allow the observation, the check reports "not observable".

- The check holds: the prediction is **confirmed** (`prediction_resolved`) and the step completes.
- The check does not hold yet: the step waits and checks again until the window closes.
- The window closes without the check holding: the prediction is **refuted**. The existing resolver writes `prediction_error`, adds evidence against the supporting belief and updates calibration. The step fails as a failed expectation, and the plan is reconsidered.
- The observation cannot be made at all: the prediction **expires** (unknown is not refuted), and the step fails as an unverified outcome.

The mapping to the event vocabulary reuses existing types instead of adding redundant ones:

| Stage | Event |
|---|---|
| action requested | `plan_step_started`, `policy_decision` |
| authorised | `tool_authorised` (or `tool_requested` for approval) |
| completed | `tool_executed` / `tool_failed`, `plan_step_completed` / `plan_step_failed` |
| observation | the verification check's own `tool_executed` and any output observation |
| expectation resolved | `prediction_resolved` / `prediction_error` |

### Replanning

When the volition rule says replan, the executor marks the plan `replanning`, records the reconsideration, cancels the plan's open steps (withdrawing their pending approval requests and capability requests), and asks the planner for a new revision. The old plan is marked `superseded` with a link, and `plan_replanned` names both. The failed plan is never edited to look as if the new one was always intended.

The limit is three replans per intention. After that, or when no strategy remains, the intention is abandoned with the reason. In the restart case this means a failed restart is followed by diagnosis and escalation, not a second restart.

A new commitment that only adds an approval requirement replans under the new constraint and may reuse the same strategy.

### Resources, operator attention and long-running work

- Every plan has a budget. Invocations default to at most 20, and wall time, money and approvals may be capped. Before each capability step the executor compares actual use with the budget. When exhausted, the step is blocked, the intention suspended and the plan blocked.
- Operator attention is a resource. Capability requests are capped per mind (five open by default), and requests from one intention share a batch key, so the approval centre groups them. When the cap is reached, a request step waits ten minutes and asks again. Approval history is shown to the operator but never used to decide.
- Wait and monitor steps persist their next run time. No goroutine sleeps on a plan, so long-running plans ("wait for the deployment, then verify", "monitor until stable for fifteen minutes") survive restarts.

### Restarts, duplicates and interruptions

- **Duplicate executors** (two schedulers, two daemons) cannot run a step twice, because claims are conditional updates.
- **A step found executing with an expired lease** means the process stopped mid-action. The executor looks up what happened by the step's origin:
  - a recorded execution is adopted (completed or failed);
  - a recorded approval request resumes the wait;
  - with no record, an idempotent or read-only capability is retried;
  - with no record and a non-idempotent writing capability, the step is **blocked** with "outcome unknown; not repeating a non-idempotent action without verification", and the intention is blocked as `outcome_unverified`. An operator checks and releases the step from the plan page or `POST /api/v1/minds/{mind}/plans/{id}/steps/{step}/retry`.
- **Preconditions** skip an action when the world changed: if the unit is already healthy, the restart is skipped and, when the precondition says so, the intention is fulfilled without acting.
- **Forks and imports** quiesce inherited work: open steps are blocked with their approval and request ids cleared, open plans blocked and live intentions suspended. Approvals and grants belong to the source mind.
- **Dreams** may leave wants and `dream`-origin drafts; the executor never runs a dream plan.

### Procedural memory

When a plan completes, the steps that completed are stored as a procedural memory ("To restart atlas-worker.service: run service.restart on atlas-worker; then verify: atlas-worker is active. The outcome was confirmed by observation."), with a higher confidence when a verify or monitor step confirmed it. The revision number is noted when earlier plans failed.

## Typed capabilities

Plans can only use registered capabilities; see [capabilities.md](capabilities.md). The host set is bounded: `service.status`, `service.restart`, `logs.query`, `host.disk.usage`, `host.memory.status` and `host.process.list`. The browser set is read-only: `browser.navigate`, `browser.read` and `browser.links`. There is no general command capability, and none can be registered at runtime.

## Where to inspect

- Mind → Volition → Plans; a plan page shows the dependency graph, each step's expectation, observation and linked approval, request, execution and subordinate intention, estimates against budget, assumptions, alternatives, revisions and validation.
- API: `/api/v1/minds/{mind}/plans`, `/plans/{id}`, `/plans/{id}/cancel`, `/plans/{id}/steps/{step}/retry`.

## Limits

- The deterministic strategies cover single capability actions, service diagnosis and escalation. Other objectives need a model-proposed plan; there is no route for operators to author plans directly in this release.
- Verification knows the expected observation for `service.restart` only; other writing capabilities complete without a built-in verification unless a plan states one.
- Money estimates are zero for all built-in capabilities; token estimates come from model calls, not from plans.
