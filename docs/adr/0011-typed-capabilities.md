# ADR-0011: Typed, bounded capabilities and temporary scoped authority

Date: 2026-09-15  
Status: Accepted

## Context
Autonomous plans need more actions than `http.fetch`, and the obvious shortcut, a shell, would hand cognition an unbounded vocabulary whose consequences policy cannot describe. Plans also need to know, before acting, whether an operation changes anything, whether it is safe to repeat and roughly what it costs. When a mind lacks authority it could only fail or wait for a per-request approval; there was no way to ask for narrowly scoped, temporary permission with a justification, and "approve for session" could not limit parameters, access or operation count. Separately, policy matched on a caller-supplied target string that nothing tied to the parameters, so a rule for one unit could in principle authorise a request whose parameters named another.

## Decision
- Every capability declares access (`read` or `write`; unknown is treated as write), side effects, idempotency, a timeout and a resource estimate. The registry is frozen after start-up; adding a capability afterwards panics.
- A capability may derive its policy target from its parameters (`TargetFrom`): the unit a restart acts on, the host of a URL. A different caller-supplied target is refused, and an approval whose modified parameters would change the target is refused.
- New typed operations run fixed argument vectors from an allow-list of two programs (`systemctl`, `journalctl`) with no shell, a minimal environment, validated unit names (no leading dash, no path characters), bounded time and output: `service.status`, `service.restart`, `logs.query` (masks e-mail and IP addresses and credential-like values by default), `host.disk.usage`, `host.memory.status`, `host.process.list` (names only; command lines can contain secrets and are never read).
- Browser access is read-only: `browser.navigate`, `browser.read`, `browser.links` fetch through the SSRF-safe client, never run scripts, refuse cross-host redirects, use the host as the policy target and deliver content as low-trust observations. `http.fetch` output is low trust too.
- `browser.click`, `browser.fill`, `browser.submit` and `browser.download` are designed but not registered. They require a sandboxed rendering engine with no access to Noema's network or credentials, per-domain write policy, a submission preview in the approval, and downloads written only into a quarantined research-folder integration so they go through the existing hostile-input ingestion. None of that ships in this release, and an unregistered name cannot be invoked or requested.
- Temporary authority is a scoped grant: capability, target pattern, parameter globs, access, expiry (at most seven days) and an optional operation count consumed atomically. Grants apply only where policy says ASK, never over DENY or a locked capability, and never satisfy an invocation that a commitment marked as requiring per-request approval. Minds ask for grants with structured capability requests (scope, duration, operation cap, reason, evidence, risk, mitigation, fallback, alternatives) that only a named person can decide; component names are refused as deciders and as usernames. Open requests per mind are capped, and requests from one intention share a batch. Lapsed grants refresh the self-model.

## Consequences
- Plans can reason about side effects and repetition from metadata instead of guessing from names.
- Host capabilities depend on systemd and journald; on other systems they fail with an error, never silently succeed.
- PII masking is a mitigation, not a guarantee: names and free-text personal data pass through.
- Operators gain a request queue; the cap and batching keep it short at the cost of a mind sometimes waiting before it may ask again.

## Alternatives considered
- A general command capability behind ASK: rejected; approvals of arbitrary strings are not meaningful review, and it contradicts the locked `sudo_shell` stance.
- A headless browser now: rejected for this release; the isolation it needs is larger than the rest of the change.
- Extending session grants in place: rejected; they are referenced by the existing approval flow and API, so scoped grants are a separate table and the old path keeps working.
