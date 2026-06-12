# ADR-0005: Capability and policy layer as the only path to action

Date: 2026-09-14  
Status: Accepted

## Context
A reasoning system must not gain infrastructure access because it can describe an action. Operators need ALLOW/ASK/DENY control at several scopes with auditability.

## Decision
Tools register a `Capability` (name, description, parameter schema, risk class, target kind). The only execution entry point is `capabilities.Invoke`, which: validates parameters, evaluates policies (scopes: global, mind, capability, target pattern, action type, time window; most specific wins; DENY > ASK > ALLOW on ties), records a `policy_decision` event, and either executes, creates an approval request, or denies. Approvals are bound to a hash of the exact request; "approve for session" grants a time-boxed, hash-pattern-bound exception. Contexts flagged as dreaming or replay are refused unconditionally. `DENY`-locked capabilities (sudo shell, database deletion) cannot be relaxed via UI or API.

## Consequences
- Every external effect has a policy decision and an event behind it.
- New tools are small: implement the capability, register it, choose a default policy.
- Minds have no capability that edits policies, so escalation is structurally impossible.

## Alternatives considered
- Per-tool ad-hoc checks: inevitably bypassed.
- OS-level sandboxing only: valuable later as defence in depth, not a replacement for policy.
