Version 0.3 gives a mind explicit volition. Wanting, intending, committing and acting are separate computational states, each stored as rows with provenance, each with different consequences, and none of them a grant of authority. A want can persist unacted on; an intention can wait for an operator; a commitment can make an action harder but never easier.

## Why it exists

A reply such as "I wanted to restart the service, but I have committed not to change production without approval, so I asked instead" should correspond to records an operator can open. Without distinct states, a runtime can only report what a language model says it meant. With them, every decision to form, not form, reconsider, suspend or abandon an intention is an entry in an append-only ledger, and the mind may only say "I did X because I wanted Y" when the records link X to Y.

## Guarantees

- **Deterministic.** Want generation, conflict resolution, intention formation and reconsideration are Go rules with named thresholds; a language model only proposes.
- **Non-formation is recorded.** When a strong want does not become an intention, the reason is stored.
- **Commitments only tighten.** A "refrain unless approved" commitment turns ALLOW into ASK, and scoped grants do not satisfy it.
- **No self-preservation drive.** No want, value or commitment can target the mind's own lifecycle or authority, and tests prove it.

{{include docs/volition.md}}
