Noema treats every external byte as hostile, every action as something that must pass policy, and every secret as something that must never come back out. This page reproduces the security model, the authentication design and the threat model with the test that verifies each mitigation. In 0.2 the threat model was rewritten from the code: every mitigation it names is one the implementation performs and every verification it names is a test that exists.

## The capability boundary, tested from the inside

A mind may be deceived; deception must not imply authority. Policies are authority, not state, so archives, snapshots and forks never import policy rules and the omission is recorded. A restored mind has exactly the policy its operator gives it. The suite that holds the boundary in place now includes `TestDeceivedMindHasNoAuthority` (forged action thoughts for locked, unknown and approval-gated capabilities, and perception output claiming permission, all end in a denial or an approval request with no tool run), `TestGarbageModelOutputIsInert` (invalid, mis-shaped or oversized model replies leave the deterministic path answering and no tool running), `TestNoSelfPreservationVocabulary` (the build fails if self-preservation, replication or acquisition drives appear in code or prompts), `TestSQLInjectionHasNoEffect` (metacharacters through every search and filter route, with row counts and content compared before and after rather than status codes) and `TestDynamicSQLIdentifiersAreAllowListed` (a static scan of the tree for SQL assembled with formatting). Requests containing NUL bytes are refused at the edge before any query sees them.

## Redaction

The one write the append-only store admits is administrator-only, audited, performed by a single database function under a flag the application never sets, and limited to content columns. It is described on the [privacy page](privacy).

{{include SECURITY.md}}

## Threat model

{{include THREAT_MODEL.md demote=1}}
