# ADR-0001: Global-workspace cognitive cycle with deterministic attention

Date: 2026-09-14  
Status: Accepted

## Context
Noema needs many concurrent cognitive processes to compete for limited "attention" in a way an operator can inspect and reproduce. Letting an LLM decide what to think about would be opaque, expensive and non-deterministic.

## Decision
Each mind runs a cycle loop. Registered `Process` implementations observe a `BrainState` and return `ThoughtCandidate`s. A pure-Go attention scorer computes a weighted sum over named features with per-mind weights, records every candidate and its component scores as events, and admits the top-K into a capacity-limited workspace. Admitted thoughts are broadcast (as state and as an event) and dispatched to executive handlers. Cycles are triggered by new events or mode timers, never by a fixed hot loop.

## Consequences
- Attention is explainable ("why did A win?") and unit-testable for determinism.
- LLM cost is bounded per cycle because processes, not the cycle, decide when to call models within budgets.
- Adding a cognitive faculty means adding a `Process`; the core does not change.
- Workspace capacity and weights are tunables exposed in the UI with audit trails.

## Alternatives considered
- Single LLM agent loop with tools: opaque, vendor-bound, cannot show why.
- Actor-per-process with message passing: more machinery for no inspectability gain; concurrency is still bounded by a pool in the chosen design.
