# ADR-0012: Wants, intentions and commitments as separate recorded states

Date: 2026-09-15  
Status: Accepted

## Context
Until 0.2 a request mapped straight to a capability invocation, and goals were the only motivational record. Nothing recorded that the mind wanted something, whether it decided to pursue it, what normative constraints applied, or why a strong want led to no action. Explanations of behaviour would have had to be reconstructed after the fact, which is exactly the fluent, unsupported self-description Noema is meant to avoid.

## Decision
- Keep four states distinct, in their own tables with their own events: **wants** (preference with origin, decaying strength, urgency, optional means), **intentions** (decision with sources, expected result, priority, status and blocked reason), **commitments** (do/refrain over matched actions, optional "unless approved", weight from kind, due, expiry, revocability) and **actions** (what the capability layer recorded).
- Derive wants, resolve conflicts, form intentions, check commitments and reconsider intentions with pure Go functions over plain inputs (`internal/volition`), and store every choice, including non-formation, with its terms in an append-only decision ledger.
- Let attention gate formation: an `intend` candidate competes for the workspace, so what the mind decides to pursue depends on the same competition as everything else, and ablations of attention or the workspace are measurable.
- No fixed hierarchy between wants and commitments. Conflicts are resolved by recorded scores in which commitments contribute normative weight, goals priority and urgency scales strength. Value-derived wants (caution, privacy) oppose actions explicitly and can constrain an approach to the approved route.
- Commitments restrict only. A refrain-unless-approved commitment sets `RequireApproval`, turning ALLOW into ASK; nothing in volition can loosen a decision.
- Represent the intention–action gap: a blocked intention names what it waits for, and a subordinate intention records the waiting (obtain approval, obtain access).

## Consequences
- "What did the mind want, which wants conflicted, which won and why" is a query, and so is "why did it not act".
- Formation weights are hand-set; they are recorded, not claimed to be right, and can be studied with experiments.
- More events and rows per interaction; thresholds, reinforcement, decay and a cap on active wants bound accumulation.
- Explicit request recognition is literal English; other requests produce wants without means.

## Alternatives considered
- Asking a model whether to act: rejected; decisions about pursuit and constraints must be deterministic and replayable.
- Commitments as policy rows: rejected; policy is authority outside cognition, commitments are cognitive state and must never grant.
- A single "intent" record with flags: rejected; the distinctions (wanting without intending, intending without authority) are the point.
