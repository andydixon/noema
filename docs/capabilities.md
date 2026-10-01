# Capabilities and policy

Nothing a mind reasons about can touch the world except through `capabilities.Service.Invoke` (ADR-0005). There is no other execution path, and there is no capability that edits policies, so a mind cannot escalate.

## Capabilities

A capability is registered in code with a name, description, risk class (low, medium, high, critical), target kind, a parameter schema (unknown parameters are rejected; URLs must be http(s); paths may not traverse), a default effect and an executor, and with the metadata plans rely on (ADR-0011):

- **access**: `read` (observes, changes nothing outside Noema) or `write`; unknown access is treated as write;
- **side effects** in plain words, **idempotency**, a **timeout** (default 60 s) and a **resource estimate**;
- **target derivation**: the policy target computed from the parameters (the unit a restart acts on, the host of a URL), so a rule on a target always matches what will be touched; a caller-supplied target that disagrees is refused;
- **output trust**: the trust of the observation made from the output (web content is low trust).

The registry is **frozen** before any mind runs; registering afterwards panics, so a mind can never add a tool.

| Capability | Access | Default | Notes |
|---|---|---|---|
| `noema.note` | read | allow | a note in the mind's own history |
| `http.fetch` | read | ask | one GET through the SSRF-safe client; output low trust |
| `browser.navigate`, `browser.read`, `browser.links` | read | ask | no scripts, cross-host redirects refused, host as policy target, output low trust |
| `host.disk.usage`, `host.memory.status` | read | allow | kernel figures |
| `host.process.list` | read | ask | names, state and memory; command lines are never read |
| `service.status` | read | allow | `systemctl show` with a validated unit name |
| `service.restart` | write, not idempotent | ask | `systemctl restart`; "requested, health not verified" |
| `logs.query` | read | ask | `journalctl` for a unit, bounded; e-mail and IP addresses and credential-like values masked unless `redact_pii` is false |
| `read_metrics`, `repository.read` | read | allow / ask | through connected integrations |
| `internet_search`, `send_email` | read / write | ask | need integrations |
| `sudo_shell`, `delete_database` | write | locked | exist only so they can be named in policy and listed as unavailable |

Host operations run a fixed argument vector from an allow-list of two programs (`systemctl`, `journalctl`), with no shell and a minimal environment. Unit names are validated: no leading dash, no path characters. Writing browser operations (click, fill, submit, download) are designed in ADR-0011 but not registered. Locked capabilities always deny and cannot be relaxed by any rule.

## Policy

Rules live in `policies` with scope (global or one mind), capability (or `*`), a target glob, an action kind and an optional daily hour window. Evaluation is deterministic: the most specific matching rule wins (mind 8, capability 4, target 2, action 1, window 1), and on ties the stricter effect wins (DENY > ASK > ALLOW). With no matching rule the capability's default applies. Every invocation writes a `policy_decision` event naming the rule and its specificity.

The policy page shows the effective matrix (mind × capability) for a generic target right now, the rules, and active session grants.

## Invocation

`Invoke` refuses in forbidden contexts (dreaming, replay), validates parameters, derives the target, evaluates policy, then session grants and scoped grants (only where policy says ASK), applies `RequireApproval` (set by plans when a commitment or value constraint demands approval; it turns ALLOW into ASK and grants do not satisfy it), records the requesting origin (for example a plan step), hashes the exact request, and then:

- **ALLOW** executes with a timeout and panic isolation, records a `tool_executions` row, emits `tool_executed` or `tool_failed`, and turns any textual output into an **observation with tool trust** so it re-enters cognition as data, never as instructions.
- **ASK** creates an approval request (capability, target, parameters, reason, risk, evidence, expiry), emits `tool_requested` and an alert notification.
- **DENY** emits `tool_denied` and counts the denial.

## Approvals

Operators approve once, approve for the session (a time-boxed grant matching the capability and target), deny with a note, or modify parameters before approving (re-validated and re-hashed; the original hash is kept in the append-only decision record; parameters that would change the target are refused). Approvals execute immediately through the same path and the outcome is stored on the request. Requests expire; a job marks overdue ones. The requesting mind may withdraw a pending approval its plan no longer needs. Decisions must be made by a named person: component names are refused as deciders.

## Scoped grants and capability requests

When a plan needs authority the mind lacks, it raises a **capability request**: capability, target pattern, parameter limits, access, duration (at most seven days), operation cap, reason, evidence, risk, mitigation, fallback and alternatives, with the mind's earlier explicit decisions on that capability shown for information. Open requests are capped per mind (five by default), and requests from one intention are batched in the approval centre.

An operator grants (possibly narrowed) or denies it. A grant is a **scoped grant**: capability, target glob, parameter globs, access, expiry and optional operation count, consumed atomically. Operators can also grant or revoke directly. Grants apply only where policy says ASK, and never over DENY or a locked capability. A sweep marks grants that expired, were used up or were revoked, and refreshes the self-model, which lists live grants. API: `/api/v1/capability-requests`, `/api/v1/scoped-grants`, `/api/v1/minds/{mind}/scoped-grants`.

## Self-model

After start-up and after any policy change, each mind's self-model capability list is recomputed from the effective policy and a `self_model_updated` event is emitted when it changed. The reply generator uses it, so a mind says which capabilities it may use and reports action status (pending, denied, executed) instead of claiming to have acted.

## Cognition

Cognition never calls `Invoke` directly. The `act` process turns explicit requests ("fetch <url>", "restart <unit>", "logs for <unit>", "status of <unit>") into **wants** with the capability as proposed means. Formation decides whether an intention forms and under which commitments ([volition.md](volition.md)), and the plan executor invokes ([planning.md](planning.md)).
