# ADR-0002: Append-only cognitive events with database-enforced immutability

Date: 2026-09-14  
Status: Accepted

## Context
Provenance and replay require that history never changes. Application discipline alone is fragile.

## Decision
`cognitive_events` (and `belief_versions`, `personality_versions`, `audit_log`, `approval_decisions`) get triggers that raise on UPDATE and DELETE. Belief changes, corrections, consolidations and retractions are new events referencing old ones. Retention purges, if ever configured, run through a dedicated, audited maintenance path that disables the trigger inside one transaction and records what was purged.

Events use UUIDv7 ids (time-ordered) plus a `bigserial seq` that gives a strictly increasing, monotonic ordering key. Durable consumers read by `seq`; the in-memory bus is only a wake-up.

The sequence is **not** gap-free and nothing may assume it is: PostgreSQL sequences are non-transactional, so a value consumed by a statement that later rolls back is never reused, and values may also be skipped across crashes or cache pre-allocation. Consumers therefore treat `seq` as an ordering cursor only: "everything after N" is `seq > N`, never `seq = N + 1`. A missing value is not evidence of a missing event; corruption is detected by the append-only triggers and integrity checks, not by counting. `internal/events.TestCursorsToleratePlaceholderGaps` guards this.

## Consequences
- History is trustworthy by construction; provenance links never dangle.
- Storage grows monotonically; retention policy is an explicit operator decision.
- Cursor-based consumption makes restarts and replay simple, and tolerates gaps in `seq` by construction.

## Alternatives considered
- Event sourcing with projections rebuilt from events only: elegant but forces all reads through replay; we keep mutable projection tables (beliefs, memories) with immutable version tables beside them.
- Kafka or similar: unnecessary operational weight for a single-process daemon.
