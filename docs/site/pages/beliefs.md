Beliefs are the mind's propositions about the world, each with a score that is *derived* from evidence rather than asserted, and each honest about what that score is. This page gives the arithmetic, what every number means, the lifecycle including the unknown and superseded states, how sources earn reliability, and the operator controls.

## What the numbers mean

Every number on a belief page has one defined meaning, and none of them is a probability unless the page says so. The **epistemic score** is the deterministic output of the arithmetic below over the recorded evidence: a ranking of how well supported a proposition is given what this mind has seen and how it weighs it. It is comparable between beliefs of one mind; it is not an 83 per cent chance of being true. The **calibrated estimate** is the observed hit rate of the mind's resolved predictions whose stated confidence fell in the same range, shown only once at least twenty predictions in that range have been decided; predictions are the one place a score meets reality, so they are the only empirical basis offered, and until it exists the page says why not. The **primary uncertainty** is the deterministic reason the score should be doubted most: a single source family, second-hand evidence only, conflicting evidence, little evidence, or an unclear reading of what was said.

## Confidence

A belief starts from a prior set by its epistemic kind (a hypothesis at 0.25, a claim at 0.50, a fact at 0.80; the full table is under [concepts](concepts)). Evidence is then grouped by **family**, the independent source a row ultimately rests on: a person, an integration, an operator, a prediction. Within a family the strongest primary item counts in full, each further primary item adds a quarter, and the total is capped at one and a half times the strongest; derived items (a consolidated memory, an inference, a reflection, a dream, a recalled memory) count only when the family has no primary item at all, because they re-express information rather than corroborate it. The confidence is

```
confidence = sigmoid( logit(prior) + 1.5 × (for × diversity − against) )
```

where `for` and `against` are the sums of family contributions and `diversity` rises from 0.6 for a single family to 1.0 for three or more. The result is bounded to [0.02, 0.98]: a mind is never certain and never certain of the negation. The number is an **uncalibrated epistemic score**, not a probability of truth; the belief page also shows a calibrated estimate drawn from the mind's resolved predictions when at least twenty were decided in the same range, and names the primary uncertainty (a single source family, second-hand only, conflicting evidence, little evidence, an unclear reading). Every evidence row carries its derivation, family and root ids; the breakdown, family by family, is stored with every version and shown on the belief page. Rows written before provenance was recorded are marked as legacy and grouped by their source string; no ancestry is invented for them.

## Statuses

`unknown` (recorded, but no live evidence for or against), `hypothesis` (a proposal awaiting evidence), `tentative`, `likely`, `accepted`, `contested` (real evidence on both sides), `rejected` (evidence or an operator pushed the score low) and `superseded` (replaced by a corrected proposition or a later state). Status follows the score and the shape of the evidence automatically; an operator can pin `rejected`. Absence of evidence never collapses towards false: a belief whose evidence has all been retracted becomes `unknown`, its score is not quoted, and an introspective question is answered with "I do not know".

## Time

Every belief is classified deterministically as **persistent** (a name, a relationship, a fact about the past), **transient** (a status, a level, a location, a mood) or of unknown scope, and carries the time it was observed. When a newer transient belief conflicts with an older one observed at least a minute earlier, the older one is closed with a validity interval and marked superseded rather than contested; the mind holds the current state and can list the earlier ones. Persistent beliefs that conflict are contradictions in the ordinary sense.

## Source reliability

Credibility starts from the channel's trust level. It is then adjusted per **source and subject**: a source whose supported beliefs were borne out by predictions or upheld by operators becomes more credible on that subject, one whose beliefs were refuted or retracted becomes less so, and nothing carries across subjects. Reliability is applied only once three outcomes exist and is never global, so a monitoring integration that is excellent on disk usage and noisy on latency is treated as exactly that.

## Contradictions

A deterministic detector compares new propositions with existing ones on the same subject using negation patterns and lexical overlap. A detected contradiction creates a `contradiction_detected` event, links the two beliefs, marks both `contested` when the evidence is comparable, and gives curiosity something to ask about. Contradictions are never resolved by picking the newer statement; they are resolved by evidence, or by an operator.

## Alternatives and links

A belief may carry alternative propositions (hypothesis generation always includes "these are unrelated"). Beliefs link to each other with `supports`, `contradicts` and `depends_on`, which the interface renders and which re-evaluation follows.

## Evidence kinds

Evidence points at memories, events, operator statements, tool results, predictions and other beliefs, with a direction, a weight, a note and the source's credibility. Retracting a memory retracts the evidence rows that cite it and re-evaluates the affected beliefs. Prediction outcomes add evidence automatically: a refuted prediction weakens the beliefs it was derived from.

## Operator corrections

From the belief page or `noemactl belief wrong`, an operator can mark a belief wrong (operator counter-evidence at high credibility), retract it outright, or attach a "do not infer" correction that prevents the same proposition being re-derived by consolidation or association. Every correction is an event, an audit entry and, for the mind, an autobiographical memory.

{{include docs/beliefs.md}}
