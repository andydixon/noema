Version 0.2 is the result of a review that asked one question of every part of Noema that makes a knowledge claim: is what the system says about itself true of the code? Where the answer was no, the code, the documentation or both were changed, and a test was left behind. This page is the summary; the sections below reproduce the full deliverable.

## What the review changed, in one paragraph

Statements people make are now recorded as **assertions** before anything becomes a belief, so a mind can be sure who said what while remaining unsure whether it is so. Every piece of evidence carries its **provenance**: how it was derived, which independent source it ultimately rests on, and where it came from, so a source repeating itself or a consolidation re-expressing it can no longer masquerade as corroboration. Every score is labelled as what it is, an **uncalibrated epistemic score**, with a separate calibrated estimate offered only when the mind's own resolved predictions justify one. **Unknown** is a status, not a low number. Beliefs about changing states **supersede** rather than contradict. Sources earn **reliability per subject** from outcomes. Association and causation are different relations. Replays record the **runtime identity** they ran under and say whether they could have been exact. History gained an audited **redaction** path that removes words and keeps shape. The threat model was rewritten so that every mitigation it names is one the code performs, with the test that proves it.

## Why it matters

A system that tracks evidence is only as honest as its accounting. The 0.1 arithmetic was sound but its inputs were not always what they claimed to be: a claim from one person looked like an observation, a consolidated memory looked like a second witness, a number that was a ranking looked like a probability. The review did not add cleverness; it removed ways of being wrong about oneself, and it did so without discarding a row of existing data. Old evidence is labelled as legacy rather than back-filled with invented ancestry.

{{include docs/epistemics.md}}
