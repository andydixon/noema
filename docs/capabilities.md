# Capabilities and policy

Nothing a mind reasons about can touch the world except through `capabilities.Service.Invoke` (ADR-0005). There is no other execution path, and there is no capability that edits policies, so a mind cannot escalate.

## Capabilities

A capability is registered in code with a name, description, risk class (low, medium, high, critical), target kind, a parameter schema (unknown parameters are rejected; URLs must be http(s); paths may not traverse), a default effect and an executor. Built-ins: `noema.note` (allow), `http.fetch` (ask; read-only through the SSRF-safe client), `internet_search` and `send_email` (ask; need integrations), and the locked `sudo_shell` and `delete_database`, which exist only so they can be named in policy and listed as unavailable in the self-model. Locked capabilities always deny and cannot be relaxed by any rule.

## Policy

Rules live in `policies` with scope (global or one mind), capability (or `*`), a target glob, an action kind and an optional daily hour window. Evaluation is deterministic: the most specific matching rule wins (mind 8, capability 4, target 2, action 1, window 1), and on ties the stricter effect wins (DENY > ASK > ALLOW). With no matching rule the capability's default applies. Every invocation writes a `policy_decision` event naming the rule and its specificity.

The policy page shows the effective matrix (mind × capability) for a generic target right now, the rules, and active session grants.

## Invocation

`Invoke` refuses in forbidden contexts (dreaming, replay), validates parameters, evaluates policy and session grants, hashes the exact request, and then:

- **ALLOW** executes with a timeout and panic isolation, records a `tool_executions` row, emits `tool_executed` or `tool_failed`, and turns any textual output into an **observation with tool trust** so it re-enters cognition as data, never as instructions.
- **ASK** creates an approval request (capability, target, parameters, reason, risk, evidence, expiry), emits `tool_requested` and an alert notification.
- **DENY** emits `tool_denied` and counts the denial.

## Approvals

Operators approve once, approve for the session (a time-boxed grant matching the capability and target), deny with a note, or modify parameters before approving (re-validated and re-hashed; the original hash is kept in the append-only decision record). Approvals execute immediately through the same path and the outcome is stored on the request. Requests expire; a job marks overdue ones.

## Self-model

After start-up and after any policy change, each mind's self-model capability list is recomputed from the effective policy and a `self_model_updated` event is emitted when it changed. The reply generator uses it, so a mind says which capabilities it may use and reports action status (pending, denied, executed) instead of claiming to have acted.

## Cognition

The `act` process proposes actions from requests it can map deterministically (currently "fetch <url>" → `http.fetch`); integrations add more. Proposals compete for attention like any thought; the admitted `action` handler calls `Invoke` and holds the outcome in working memory.
