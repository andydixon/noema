Dreaming is an optional idle mode. It is off by default, confined to an hours window, budgeted, and structurally unable to act. When it runs, the mind replays a deterministic sample of its memories, notices pairs that share vocabulary but were never connected, and proposes speculative hypotheses for an operator to review.

## Why it exists

A mind that only ever reasons about the thing in front of it never notices that two things it learned weeks apart are related. Dreaming is the cheapest safe way to find such links: it uses no external input, spends a bounded number of model calls (often none), produces nothing above hypothesis confidence, and leaves every proposal in a review queue.

## Guarantees

- **Cannot act.** The dream marks its context forbidden for the capability layer before doing anything; any invocation reached from a dream is refused, and a test proves it.
- **Low confidence by construction.** Every dream hypothesis is a belief of epistemic kind `hypothesis` with two weak memory citations, which lands near 0.30 and is asserted in tests to stay at or below 0.35. Only ordinary evidence can raise it.
- **Nothing rewritten.** Sampled memories are untouched; new links are additional edges; rejecting a hypothesis retracts its belief and leaves everything else.
- **Budgeted and scheduled.** Per-mind model-call, memory and hypothesis caps; a global pause switch; scheduled dreams require enabled, inside the hours window, interval elapsed and not paused. An operator's "dream now" ignores the hours window but never the pause.

{{include docs/dreaming.md}}
