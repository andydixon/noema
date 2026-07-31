Beliefs are the mind's propositions about the world, each with a confidence that is *derived* from evidence rather than asserted. This page gives the arithmetic, the lifecycle and the operator controls.

## Confidence

A belief starts from a prior set by its epistemic kind (a hypothesis at 0.25, a claim at 0.50, a fact at 0.80; the full table is under [concepts](concepts)). Evidence is then grouped by **family**, the independent source a row ultimately rests on: a person, an integration, an operator, a prediction. Within a family the strongest primary item counts in full, each further primary item adds a quarter, and the total is capped at one and a half times the strongest; derived items (a consolidated memory, an inference, a reflection, a dream, a recalled memory) count only when the family has no primary item at all, because they re-express information rather than corroborate it. The confidence is

```
confidence = sigmoid( logit(prior) + 1.5 × (for × diversity − against) )
```

where `for` and `against` are the sums of family contributions and `diversity` rises from 0.6 for a single family to 1.0 for three or more. The result is bounded to [0.02, 0.98]: a mind is never certain and never certain of the negation. Every evidence row carries its derivation, family and root ids; the breakdown, family by family, is stored with every version and shown on the belief page. Rows written before provenance was recorded are marked as legacy and grouped by their source string; no ancestry is invented for them.

## Statuses

`hypothesis` (below 0.35), `tentative`, `likely`, `accepted` (above 0.85), `contested` (significant evidence on both sides), `rejected` (retracted by evidence or an operator) and `superseded` (replaced by a corrected proposition). Status follows confidence and evidence shape automatically; an operator can pin `rejected`.

## Contradictions

A deterministic detector compares new propositions with existing ones on the same subject using negation patterns and lexical overlap. A detected contradiction creates a `contradiction_detected` event, links the two beliefs, marks both `contested` when the evidence is comparable, and gives curiosity something to ask about. Contradictions are never resolved by picking the newer statement; they are resolved by evidence, or by an operator.

## Alternatives and links

A belief may carry alternative propositions (hypothesis generation always includes "these are unrelated"). Beliefs link to each other with `supports`, `contradicts` and `depends_on`, which the interface renders and which re-evaluation follows.

## Evidence kinds

Evidence points at memories, events, operator statements, tool results, predictions and other beliefs, with a direction, a weight, a note and the source's credibility. Retracting a memory retracts the evidence rows that cite it and re-evaluates the affected beliefs. Prediction outcomes add evidence automatically: a refuted prediction weakens the beliefs it was derived from.

## Operator corrections

From the belief page or `noemactl belief wrong`, an operator can mark a belief wrong (operator counter-evidence at high credibility), retract it outright, or attach a "do not infer" correction that prevents the same proposition being re-derived by consolidation or association. Every correction is an event, an audit entry and, for the mind, an autobiographical memory.

{{include docs/beliefs.md}}
