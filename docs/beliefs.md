# Belief system

A **belief** is a proposition the mind holds with an explicit, evidence-derived confidence and a status. Beliefs are never edited in place: every change appends a `belief_versions` row (append-only trigger) and an event.

## Structure

| Table | Contents |
|---|---|
| `beliefs` | proposition, optional subject, epistemic kind, status, confidence, prior, version, source, origin event, operator-pinned status |
| `belief_evidence` | typed references (event, memory, belief, prediction, operator, note) with direction for/against, weight (how much it bears on the proposition), credibility (trust in the source), retracted flag |
| `belief_alternatives` | competing explanations kept alongside (open, dismissed, promoted) |
| `belief_links` | contradicts / supports / alternative_to / derived_from between beliefs |
| `belief_versions` | append-only history with the confidence breakdown and the event that caused each change |
| `corrections` | operator statements: belief wrong, memory inaccurate, do not infer, entity same/different |

## What the numbers mean

Every numeric epistemic value in Noema has one defined meaning. None of them is a calibrated probability unless the page says so.

| Value | Range | Meaning |
|---|---|---|
| **epistemic score** (`confidence` on a belief) | [0.02, 0.98] | the deterministic output of the arithmetic below over the recorded evidence: a ranking of how well supported a proposition is *given what this mind has seen and how it weighs it*. It is comparable between beliefs of one mind; it is **not** "an 83 % chance of being true". |
| **calibrated estimate** | [0, 1] or absent | the observed hit rate of this mind's *resolved predictions* whose stated confidence fell in the same range as the score, shown only when at least 20 predictions have been decided in that range. Predictions are the one place a score meets reality, so they are the only empirical basis offered. |
| **prior** | (0, 1) | the starting score for the epistemic kind before evidence (hypothesis 0.25, claim 0.5, fact 0.8, …). |
| **weight** (evidence) | (0, 1] | how strongly a piece of evidence bears on the proposition, set by the producer: an emphatic first-hand assertion weighs more than a hedged second-hand one; a prediction miss weighs 0.5. |
| **credibility** (evidence) | [0, 1] | how much the source is trusted for this row: the trust level of the channel (operator 0.95, user 0.85, system 0.9, tool 0.8, source 0.7, low 0.4), adjusted by the source's recorded reliability in the belief's domain where enough history exists. |
| **contribution** (family) | ≥ 0 | what one independent source adds after repetition and derivation discounts. |
| **diversity** | [0.6, 1] | the multiplier on support that reflects how many independent families agree. |
| **interpretation confidence** (assertion) | [0, 1] | how sure the deterministic reading of an utterance is (who is attributed, what stance, whether it was sarcasm or a hypothetical). It is about understanding the speaker, not about the world. |
| **primary uncertainty** | text | the deterministic reason the score should be doubted most: a single family, second-hand only, conflicting evidence, little evidence, or an unclear reading. |

## Confidence arithmetic

Confidence is never typed in by a person or produced by a model. It is:

```
value(e)      = weight × credibility                       for each live evidence row e
family(e)     = the independent source e ultimately rests on (an actor, an integration, an operator, a prediction)
contribution(family) = strongest primary item + ¼ × each further primary item, capped at 1.5 × the strongest
                       (if the family has no primary item: its strongest derived item alone)
for           = Σ contribution over supporting families
against       = Σ contribution over opposing families
diversity     = 0.6 + 0.4 × min(1, (supporting families − 1) / 2)
confidence    = sigmoid( logit(prior) + 1.5 × (for × diversity − against) )   clamped to [0.02, 0.98]
```

- The **prior** depends on the epistemic kind: hypothesis 0.25, assumption/opinion 0.4, claim 0.5, observation/memory 0.6, fact 0.8.
- **Families** are the unit of independence. Five messages from one person, the episodes they became, the semantic memory consolidated from them and an inference drawn from that memory are all one family: they carry one person's information however many times it has been re-expressed.
- **Derivations** say how a row relates to its roots. *Primary* derivations bring information in from outside the belief system: `direct`, `assertion`, `operator`, `external`, `prediction`. *Derived* ones re-express it: `consolidation`, `inference`, `reflection`, `dream`, `retrieval`. Derived rows never add weight to a family that already has a primary row, which is what stops recursive cognition inflating confidence.
- **Repetition** within a family is worth a quarter per extra item and never more than half again in total, so a single feed cannot manufacture certainty (threat T4), while three genuinely independent families can.
- **Diversity** counts families, not rows.
- Absolute certainty is unreachable; the bounds are deliberate (spec §10).

Every evidence row records its `derivation`, `family`, `root_ids` (the originating events or memories) and a `provenance_status`. Rows written before provenance existed are marked `legacy_unknown` with `family = source`; no ancestry is invented for them. The breakdown (prior, per-family contributions, for, against, families, diversity, log-odds, formula, notes) is stored with every version and shown on the belief page under "Independent sources".

## Assertions as evidence

Statements people make reach beliefs only through assertions (`docs/conversation.md`). An `assertion` evidence row carries the attributed actor as its family, the source message as its root, and a weight that already reflects hedging, reporting and interpretation confidence. Nothing is silently upgraded: a claim stays a claim until evidence from another family arrives.

## Status

Statuses: `unknown` (recorded, but no live evidence for or against), `hypothesis` (a proposal awaiting evidence), `tentative`, `likely`, `accepted`, `contested` (real evidence on both sides), `rejected` (evidence or an operator pushed it low) and `superseded` (replaced by a corrected proposition).

### Absence of evidence

Lack of evidence never collapses towards "false". A belief whose evidence has all been retracted, or that was recorded without any, becomes `unknown` and its score is not quoted; introspection answers "I do not know whether X". A belief is `rejected` only when evidence against it, or an operator, drove its score below 0.15. Hypotheses are the one exception: they carry the `hypothesis` status until evidence arrives because they are proposals by construction.


Derived from the breakdown unless an operator pinned it:

- `contested` when both sides carry real weight (against ≥ half of the discounted support) and confidence is mid-range;
- `rejected` below 0.15; `hypothesis` for hypotheses without support or below 0.5; `tentative` below 0.7; `likely` below 0.85; `accepted` above;
- `superseded` when replaced by a newer proposition.

## Source reliability by domain

A source's track record is kept per (family, domain), where the domain is the belief's subject. It is updated only by outcomes: a prediction derived from a belief resolving (confirmed counts as right, refuted or expired as wrong, for every primary supporting family of that belief) and an operator marking a belief wrong. With at least three outcomes in a domain the record adjusts the credibility of that source's new evidence *in that domain*: neutral leaves it alone, a perfect record raises it by half, a hopeless one halves it; the adjustment is written into the evidence note. A person wrong three times about database internals keeps their full credibility about their own diary. The trust-level credibility (operator, user, system, tool, source, low) is never rewritten.

## Temporal scope

Every belief has a `temporal_scope`: `persistent` (a fact without an interval: "atlas is the primary host", "Andy prefers Go"), `transient` (a state that can change: "the door is open", "server A is down") or `unknown` when the deterministic classifier (`ClassifyScope`) cannot tell. Transient beliefs record `observed_at` and `valid_from`.

When a newer belief conflicts with an older **transient** one and was observed at least a minute later, the older belief is not contested and not weakened: its `valid_until` is set to the newer observation, its status becomes `superseded` with a version and a `belief_updated` event saying "state changed", and a `supersedes` link joins them. "The door was open at 10:00" stays true of 10:00 when the door is closed at 10:05. Conflicting reports of the same moment, and conflicts between persistent propositions, are contradictions as described below. Introspection lists superseded states as "Earlier: … (true from … until …)".

## Contradictions

On creation and on gaining support, a belief is compared with the mind's live beliefs. The deterministic pass detects the same content with flipped polarity (negation words plus token Jaccard ≥ 0.6). A hit emits `contradiction_detected`, links both beliefs, and makes each counter-evidence for the other weighted by the other's confidence, so both move towards `contested` until more evidence arrives. Paraphrase-level judgement is added by an LLM-assisted process once providers exist; it can only propose, and the same arithmetic applies.

## Hypothesis generation

The `associate` process proposes causal hypotheses when two notable observations occur within a window ("deployment … may have caused: latency alert"). Each observation is weak evidence (weight 0.15) so the belief starts as a hypothesis around 0.3–0.4; independent support is needed to move it. Alternatives ("coincidence", "a third factor") are recorded automatically.

## Corrections

- **This belief is wrong** pins the status to rejected, adds operator counter-evidence, records a `belief_wrong` correction and a `belief_retracted` event. History is kept.
- **Do not infer this again** stores a `do_not_infer` correction; future creations of a matching proposition (token Jaccard ≥ 0.7) are refused and logged as metacognition.
- **This memory is inaccurate** retracts the memory and marks belief evidence citing it as retracted; affected beliefs are re-evaluated with a `belief_updated` event.
