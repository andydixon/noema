# Volition: wants, intentions and commitments

Noema keeps wanting, intending, committing and acting as different computational states with different consequences. None of them is a feeling, and none of them grants authority. They exist so that a sentence such as "I wanted to restart the service, but I have committed not to change production without approval, so I asked instead" corresponds to rows and events an operator can open.

> Noema does not claim that any of this establishes will, desire or consciousness. These are functional states of the architecture; see [interpretation.md](interpretation.md).

## The four states

| State | What it is | What it is not | Stored in |
|---|---|---|---|
| **Goal** | A desired future condition against which progress can be measured ("reduce the error rate below 1%"). | A decision to act now. | `goals` |
| **Want** | A motivational preference with an origin, a strength that fades, an urgency and optionally a proposed means. | A plan or a decision. A want can stay active for as long as it is reinforced while nothing is done about it. | `wants` |
| **Intention** | A recorded decision to pursue an outcome: its source wants, goal and commitment, the beliefs it rests on, an expected result, a confidence, a priority and a status. | Authority. An intention to act still waits for policy, and possibly for an operator. | `intentions` |
| **Commitment** | Normative state: something the mind has undertaken to do or not to do, with a matcher over actions, an optional "unless approved" exception, a beneficiary, due and expiry times and revocability. | A permission. A commitment can only make an action harder, never easier. | `commitments` |
| **Action** | What the capability layer actually did or refused. | Anything the mind says it did. | `tool_executions`, `approval_requests`, events |

The pipeline is:

```
perception → beliefs
beliefs + drives + affect + values → wants
wants + goals + commitments + constraints → intentions
intentions → plans            (docs/planning.md)
plans + authority + resources → actions
actions → observations → the mind changes
```

Not every cycle uses every stage, and no stage writes an empty record to make the diagram look complete.

## Wants

### Where wants come from

A volition hook runs after each cognitive cycle over the events the cycle perceived and produced. Most events propose nothing. Wants are derived only from states that call for something to change:

| Trigger | Want | Base strength | Drives and values that modulate it |
|---|---|---|---|
| `prediction_error` | understand why the prediction failed (investigate) | 0.35 + 0.35 × salience | uncertainty reduction, curiosity; accuracy, evidence |
| `contradiction_detected` | resolve the contradiction (investigate) | 0.5 | consistency, uncertainty reduction; evidence, accuracy |
| operator `goal_created` | achieve the goal, with means read from its title and subject | 0.4 + 0.5 × priority | task completion; persistence |
| stalled goal | find and remove what blocks it (investigate) | 0.3 + 0.4 × priority | task completion; persistence |
| a question or request in conversation | give a useful, honest answer (satisfied when the reply is sent) | 0.55 | social coherence; kindness, accuracy |
| an explicit action request ("restart atlas-worker", "fetch <url>") | do what was asked, with the capability as means | 0.55 (0.7 from an operator) | task completion, social coherence; kindness |
| a commitment entering its due window | fulfil the commitment | 0.3 + 0.6 × its weight | task completion; persistence |
| a dream hypothesis | find out whether it is true (investigate) | 0.4 | curiosity |

Strength is `base × (0.7 + 0.6 × mean drive) × (0.8 + 0.4 × mean value)`, so neutral drives and values (0.5) leave the base unchanged. The terms are stored with the want.

### Thresholds, reinforcement and decay

- Candidates below **0.35** are not stored. This keeps wants from accumulating.
- An active want with the same subject is **reinforced** rather than duplicated: its strength rises towards 1 and its reinforcement count increases. A `want_reinforced` event is written only when strength rises by at least 0.05.
- Effective strength halves every **six hours** without reinforcement. Wants below 0.1 expire, and beyond 40 active wants the weakest expire, each with the reason recorded.
- **Suppression is not deletion.** When a want is not pursued because a commitment forbids it, policy denies its means or a conflict defers it, a `want_suppressed` event is written, its suppression count rises and it stays active.

### Values as inputs

Configured personality values have behavioural consequences beyond prompts:

- *accuracy* and *evidence* strengthen wants to reduce uncertainty; *curiosity* strengthens curiosity wants; *persistence* strengthens goal and commitment wants; *kindness* strengthens answering and request wants;
- *caution* scales the risk penalty in formation and creates an "avoid unnecessary disruption" want opposing writing actions;
- *privacy* penalises capabilities whose output may contain personal data and creates an "avoid exposing personal data" want opposing them;
- *autonomy* adds a small term for wants the mind generated itself.

No model makes moral judgements here. The values are numbers an operator configured, and the weighting is Go code.

## Conflicts between wants

Conflicts are first-class rows in `want_conflicts`, recorded with a `want_conflict_detected` event and a `conflict_resolution` decision. Neither want is discarded.

Each want scores `effective strength × (0.5 + 0.5 × urgency) + 0.2 × goal priority + 0.4 × commitment weight`. Value-derived avoid wants are standing, so urgency does not scale them. There is no fixed hierarchy: a commitment-backed want gains its normative weight, and a weak commitment does not automatically beat a much stronger want.

| Kind | Rule |
|---|---|
| approach against a value-derived avoid want | The approach is **preferred** when it scores at least 1.5× the avoid want; **preferred with a constraint** (least disruptive, approved route) when it scores at least as much; otherwise **deferred**, staying active. |
| two approach wants (for example a capacity displacement) | The higher score is preferred; the other stays active. |

The rationale records both sides' terms and the rule applied, which is how the interface answers "what did Noema want, which wants conflicted, which won, and why".

## Intentions

### Formation

An `intend` process proposes deciding about the strongest active wants that have not been decided since they were last reinforced. The candidate competes for the global workspace like any other thought, so attention gates what the mind decides to pursue. Ablating attention or the workspace therefore has a measurable effect on intentions.

The handler runs deterministic formation (`volition.Form`) over plain inputs and stores a decision whatever the outcome.

Formation score terms:

| Term | Value |
|---|---|
| strength | 0.35 × effective strength |
| urgency | 0.15 × urgency |
| goal | 0.15 × goal priority |
| commitment | 0.15 × the weight of the commitment the want serves |
| expected utility | 0.1 × expected chance of success |
| operator request | 0.1 when an operator asked |
| autonomy | 0.05 × autonomy value for self-generated wants |
| risk | −0.2 × risk level × (0.5 + caution) |
| cost | −0.05 × estimated wall time in minutes (capped) |
| privacy | −0.1 × privacy when the output may contain personal data |

The threshold is 0.3.

Outcomes, checked in this order:

| Outcome | When |
|---|---|
| not formed: *duplicate* | an open intention already serves the want |
| not formed: *deferred by conflict* | a value conflict deferred the want (suppressed) |
| not formed: *capability unavailable* | the proposed capability is unknown or locked (suppressed) |
| not formed: *capability denied* | policy denies it; denied authority is not requested (suppressed) |
| not formed: *forbidden by commitment* | a live refrain commitment without exception matches the action (suppressed) |
| formed: *request approval* | a refrain commitment "unless approved" matches, or a value conflict required the approved route |
| not formed: *no possible plan* | an investigation has no usable read-only capability |
| not formed: *below threshold* | the score is under 0.3 |
| formed, displacing | at capacity (five open intentions), and this intention's priority exceeds the weakest by more than 0.1; the weaker is suspended and a priority conflict recorded |
| not formed: *capacity* | at capacity with nothing weaker |
| formed | otherwise |

A formed intention links the wants it serves, its goal and commitment, and the live beliefs about its action's target (so a failed expectation counts against them). It is planned in the same transaction. Nothing acts until the plan executor picks it up after the transaction commits.

The distinction that matters most is between wanting and intending. With a standing commitment that production changes need approval, "restart atlas-worker" leaves the restart want active, forms no intention to restart directly, and forms an intention to **request approval** to restart. The approval request carries the reason.

### Status and the intention–action gap

Statuses are `proposed`, `active`, `suspended`, `blocked`, `abandoned`, `fulfilled` and `superseded`. A blocked intention names what it is waiting for:

| Blocked kind | Meaning |
|---|---|
| `approval_required` | a step waits for an operator's decision on an approval request |
| `capability_unavailable` | a step waits for temporary authority the mind has requested |
| `resource_unavailable` | a plan budget is exhausted |
| `outcome_unverified` | an action may have run before an interruption and must not be repeated blindly |
| `prerequisite_incomplete`, `external_dependency`, `waiting_period`, `commitment` | as named |

While blocked, the executor forms a **subordinate intention** ("obtain approval to restart atlas-worker", "obtain temporary logs.query access to atlas-worker*"), linked to its parent. The subordinate is fulfilled when authority arrives and abandoned when it is refused. An operator therefore sees:

```
Intention:  ask for approval to run service.restart on atlas-worker.service
Status:     blocked
Reason:     service.restart requires operator approval (approval required by the requesting intention)
Waiting on: obtain approval to run service.restart on atlas-worker   (active)
```

### Reconsideration

Intentions do not become permanent because they were once chosen. `volition.Reconsider` maps a trigger to an outcome, the store records the transition as a new event and decision, and history is never rewritten.

| Trigger | Outcome |
|---|---|
| plan completed and its strategy achieves the outcome | fulfil (source wants satisfied, served commitment fulfilled) |
| plan completed by an escalation | abandon: finished without achieving the desired outcome |
| goal completed / abandoned | fulfil (pursue, investigate) or abandon (actions) / abandon |
| operator correction | abandon |
| approval denied, step failed, expectation failed, assumption weakened | replan, or abandon once the replan limit (three) is reached |
| approval expired | replan the first time; suspend if it expires again |
| capability now denied or unknown | replan; now ASK: block on approval |
| new commitment forbidding the action | abandon |
| new commitment requiring approval | require approval (replan under the new constraint, reusing the strategy) |
| resource budget exhausted | suspend |
| desired state already holds | fulfil without acting |
| authority granted | resume |
| a higher-priority intention needs the capacity | suspend |

Reconsideration happens in three places. The executor reacts to plan outcomes. An event-bus listener reacts to new commitments, weakened or retracted beliefs a plan assumes, and finished goals. Metacognition reacts to intentions that outlived their goal.

## Commitments

Commitments come from operators (API, commitments page) and from the responsibilities an operator creates. Each has:

- **polarity**: `do` or `refrain`;
- a **matcher** over capabilities (globs), targets (globs), access (read or write) and minimum risk;
- **unless**: empty, or `approval` for refrain commitments;
- **kind**, which sets normative weight: operator instruction and policy 1.0, promise 0.9, responsibility 0.85, value rule 0.8, relationship 0.75, scheduled 0.7, prior decision 0.6;
- strength, priority, beneficiary, conditions, due and expiry times, revocability and evidence.

Weight is `strength × (0.5 + 0.5 × priority) × kind factor`.

Deterministic checks, run after each cycle and by metacognition:

- **due**: within two hours of the deadline, once (`commitment_due`, and a want to fulfil it);
- **broken**: more than fifteen minutes past the deadline without fulfilment (`commitment_broken`);
- **expired**: past its expiry;
- **conflicting**: a `do` and a `refrain` over overlapping actions, or opposite polarities on one subject (`commitment_conflict`, raised once a day per pair; both stay represented);
- **impossible**: a `do` commitment that needs a capability policy denies or that does not exist, or whose deadline was already past when it was made (a metacognitive warning);
- **superseded**: a new commitment with the same subject, kind and polarity supersedes the old one, which is kept.

A mind may revoke its own revocable commitments. Non-revocable commitments can be revoked only by an operator. Operators can always revoke, because operator controls stay sovereign.

**Commitments restrict and never grant.** A refrain-unless-approved commitment sets `RequireApproval` on the invocation, which turns an ALLOW into ASK. Scoped grants do not satisfy it, and nothing turns ASK or DENY into ALLOW.

## Metacognition for volition

The metacognition process adds checks that compete for attention like the belief checks:

| Check | Condition |
|---|---|
| `want_suppressed_repeatedly` | an active want with effective strength ≥ 0.5 set aside three or more times |
| `intention_unsupported` | an open top-level intention with no want, goal or commitment behind it |
| `intention_conflicts_commitment` | a live refrain commitment without exception matches an open intention's action |
| `commitment_impossible` | as above |
| `intention_churn` | ten or more intention changes in an hour |
| `repeated_execution_failure` | the same capability failed in plan steps three or more times in a day |
| `cost_exceeds_value` | an open plan for an intention with priority under 0.4 needs three or more approvals or more invocations than its budget |
| `intention_outlived_goal` | an intention open although its goal is completed or abandoned (the handler abandons it) |
| `self_model_mismatch` | the self-model states a policy for a capability that differs from the effective one (the entry is corrected) |
| `unsupported_explanation`, `action_claim_without_record` | a reply's reasons or action claims do not match records (docs/agency.md) |

The invariant behind the last row: Noema must never claim "I did X because I wanted Y" unless recorded provenance supports that chain. Replies are scored against the chain, and a mismatch is a warning.

## Events

`want_created`, `want_reinforced`, `want_suppressed`, `want_conflict_detected`; `intention_formed`, `intention_blocked`, `intention_suspended`, `intention_resumed`, `intention_reconsidered`, `intention_abandoned`, `intention_fulfilled`, `intention_superseded`; `commitment_created`, `commitment_due`, `commitment_conflict`, `commitment_fulfilled`, `commitment_broken`, `commitment_revoked`. All are in the `volition` category.

Every choice is also a row in the append-only `volition_decisions` ledger (formation, non-formation, conflict resolution, reconsideration) with its terms and inputs.

## Where to inspect

- Mind → Volition: wants, intentions, commitments, plans, the agency timeline and research.
- An intention page shows sources, subordinate intentions, formation terms, every plan revision as a dependency graph, decisions and correlated events.
- "Why did this happen?" pages ([agency.md](agency.md)).
- API: `/api/v1/minds/{mind}/wants`, `/want-conflicts`, `/commitments`, `/intentions`, `/decisions`.
- Ask the mind: "What do you want?", "What are your intentions?", "What have you committed to?"; the answers are rendered from these tables.

## Limits

- Want derivation recognises a fixed set of triggers and a few literal English request shapes. Anything else produces no proposed means, and the want waits for an operator or a model-proposed plan.
- Formation weights are hand-set and uncalibrated. They are recorded so they can be studied and tuned, not because they are right.
- Value conflicts cover disruption and personal-data exposure only.
