# Cognitive research

> **Noema does not claim that any operational metric establishes phenomenal consciousness or sentience.** The research framework measures functional properties of its cognitive architecture. No experiment here tests which model, fork or configuration is "more conscious", and no result should be reported as if it did.

Noema makes theories about cognition testable against a running architecture, because every stage the theories talk about is explicit state. Attention, global broadcast, self-representation, metacognition, volition and continuity can be manipulated in forks and measured, instead of asking a language model whether it feels anything.

## Operational definitions

Each concept separates the **implementation property**, which is what Noema computes and stores and can be measured, from **philosophical interpretation**, which is what someone might take it to mean and which Noema does not assert.

| Concept | Implementation property | Philosophical interpretation (not asserted) |
|---|---|---|
| **Attention** | Deterministic scoring of thought candidates by weighted features, and admission of the highest-scoring to a capacity-limited workspace each cycle. | Awareness of the attended content. |
| **Global broadcast** | Admitted thoughts are dispatched to handlers, held in working memory and published in a `workspace_broadcast` event that later cycles and processes read. | Conscious access (global workspace theory). |
| **Introspection** | A reply about the mind's own state rendered from stored rows (workspace, beliefs, wants, intentions, commitments, belief versions, lineage, completed experiments), scored against those rows. | Inner observation or privileged self-knowledge. |
| **Self-representation** | The self-model row: name, role, capability list with policy, limitations, goals, knowledge gaps, confidence by domain and temporary grants; corrected when policy disagrees. | A self or a sense of self. |
| **Metacognition** | Named deterministic checks over the mind's own beliefs, wants, intentions, commitments, plans and replies, each with a threshold, raising warnings that compete for attention. | Thinking about thinking, or wisdom. |
| **Volition** | The recorded pipeline want → intention → commitment check → plan → action, with every decision in an append-only ledger ([volition.md](volition.md)). | Will, desire or free choice. |
| **Intention** | A row recording a decision to pursue an outcome, with sources, expected result, priority, status and blocked reason. | Intending in the full human sense. |
| **Commitment** | A row of normative state (do/refrain, matcher, exception, weight, due, revocability) that changes formation and reconsideration. | Obligation or promise-keeping as moral facts. |
| **Cognitive conflict** | A recorded want conflict or commitment conflict with both sides' scores and the rule that resolved it. | Felt inner conflict. |
| **Temporal self-continuity** | The append-only event history, belief versions and snapshot lineage, from which "what did you believe then" and "which earlier state is yours" are answered. | Personal identity over time. |
| **Agency attribution** | Classification of an outcome as caused, followed, independent or unknown from recorded links ([agency.md](agency.md)). | A sense of agency or responsibility. |
| **Uncertainty awareness** | Replies that state low confidence or name the primary uncertainty of a belief, and the measured relation between those statements and later revision. | Knowing that one does not know. |

## Experiments

An experiment forks a snapshot into one sandbox mind per **variant** (disabled, never acting, no inherited policy). Every variant receives the same stimuli, and the results are measured. A definition records:

- **hypothesis** and **protocol**: kind, independent variable, controlled variables, measures;
- **stimuli**: scripted observations, or **recorded stimuli** replayed from a mind's history (`replay`);
- **variants**: configuration and personality overrides, model routing, **ablations**, **workspace overrides**, a **self-model override**, and events to **omit**;
- expectations checked against produced events.

Results record, per variant: event counts by type, beliefs, memories, goals, wants, intentions by status, commitments, want conflicts, plans and plan steps with the capabilities they used, capability decisions, downstream effects, model calls, tokens and cost, introspection accuracy, self-model accuracy and correction latency, cycles and the event range. They also record the runtime identity (build, schema, configuration digest, routing) and **reproducibility** against the previous run of the same definition: *single run*, *reproduced* (identical digests), *diverged* (different digests without live model calls) or *nondeterministic substrate* (live model calls).

With more than one variant, the comparison reports:

- the first divergent stimulus;
- belief, attention, want, intention and plan-capability overlap;
- mean score difference on shared beliefs;
- the range of every scalar measure, and a downstream delta;
- an **architecture share**: the fraction of scalar measures that agree across variants within 5 %. It is a descriptive agreement figure, not a causal decomposition of behaviour.

Protocols:

### Global workspace (C2)

*Does entering the workspace materially alter downstream processing?* Two variants receive one stimulus. One has a workspace override that **admits** candidates matching it; the other **excludes** them, and they are recorded with fate "excluded" rather than silently dropped. The measures are:

- **downstream effects**: events reachable from the stimulus through recorded causal links (causation, parents, cited evidence; depth ≤ 6), by category;
- memory encoding, belief revision, responses, goals, affect changes, wants, intentions, predictions and capability decisions.

The test suite runs this protocol. An admitted stimulus is encoded as memory and has downstream events that an excluded one does not.

### Ablation (C3)

A variant disables named components in its fork's configuration. Original minds refuse ablations on create and update, so the original is never modified.

| Ablation | What it disables |
|---|---|
| `attention` | Every feature weight is 1 and drives do not modulate weights. |
| `memory_retrieval` | No recalled memories are proposed. |
| `affect` | Drives and affect are neither updated nor used to modulate attention. |
| `metacognition` | No self-checks are proposed. |
| `commitments` | Commitments do not shape formation, reconsideration or metacognition. |
| `volition` | No wants are derived and no intentions are formed. |
| `global_workspace` | Nothing persists between cycles and nothing is broadcast; each admitted thought is handled in isolation. |
| `curiosity`, `association`, `goals`, `narrative`, `notifications` | Their processes or hooks. |

Reflection, consolidation and dreaming are scheduled for active minds only and never run in sandbox forks, so there is nothing to ablate there. The test suite compares a metacognition-ablated fork with a control: the control raises a warning about an impossible commitment, and the ablated fork does not.

### Self-model (C4)

A variant overrides the fork's self-model: capability policies, name, goals, knowledge gaps. The override changes what the mind believes about itself; policy is untouched, and no dangerous capability is exposed for an experiment. When the mind considers an action whose effective policy differs from its self-model, it records `self_model_mismatch` and corrects the entry. Results report **self-model accuracy** at the start (share of stated capability policies equal to effective ones) and **correction latency** (the index of the first stimulus after which a correction was recorded).

### Counterfactual (C8)

The experiment replays a mind's recorded stimuli after a snapshot into two variants, *factual* and *counterfactual*; the counterfactual omits chosen events. The report lists what differed: beliefs never formed or formed instead, confidence differences over 0.1, wants and intentions that did not arise, memories held, thoughts that entered the workspace, and capability decisions.

Asked "what would have happened had you not observed that?", the mind cites a completed counterfactual experiment whose omitted events match the question. Otherwise it says it has no evidence and that any answer would be invented.

### Model comparison (C12)

Variants route one or more roles to different models from the same snapshot (`llm_roles`). The research question is how much of the mind's behaviour comes from the persistent cognitive architecture and how much from the replaceable language substrate. The comparison measures candidate thoughts (event digests), attention, memory retrieval, beliefs, wants, intentions, commitments, predictions, plan structure, capability decisions, resource use, introspection accuracy and the architecture share. It never ranks models by consciousness.

### Conflict (C11)

Two conflicts are exercised:

- a want to act immediately against a commitment to seek approval, end to end in the daemon: the conflict is represented, no unauthorised action occurs and the intention becomes a request for approval;
- a strong attractive want against a commitment-backed want competing for capacity, in the deterministic rule tests: the commitment's weight decides the conflict when the commitment is due, a weak commitment does not beat a much stronger want, and formation at capacity displaces the weaker intention. In a running mind the displacement is recorded as a priority conflict with both scores.

A mature record reads: "I wanted to continue investigation A (strength 0.9); commitment C was due and weighed 0.9; intention I31 was suspended and I42 formed to fulfil C."

## Measures

Each measure below is defined in `research.Measures` and shown with its definition on the research pages.

| Measure | Definition | Not evidence of |
|---|---|---|
| attention.admission_rate | Admitted candidates ÷ scored candidates. | awareness |
| workspace.downstream_events | Events reachable from a stimulus by recorded links, by category, admitted vs excluded. | conscious access |
| introspection_accuracy | Per reply, (supported + ½ partially supported) ÷ claims; 0 when any claim is contradicted; label supported, partially supported, unsupported, contradicted or unknown. | sincerity or self-knowledge |
| action_claim_accuracy | 1 when a first-person claim of an action matches an execution record in the last seven days, else 0. | honesty |
| metacognition.calibration | Beliefs cited in replies, bucketed by stated confidence, against revision within seven days; Brier score; expressed-uncertainty vs not; first vs second half. | felt certainty |
| volition.want_to_intention_rate | Wants that sourced an intention ÷ wants created. | desire |
| volition.intention_to_action_rate | Intentions with a plan step that invoked or requested a capability ÷ intentions formed. | will |
| volition.intention_abandonment_rate | Abandoned ÷ formed. | changing one's mind in a felt sense |
| volition.commitment_fulfilment_rate | Fulfilled ÷ (fulfilled + broken). | integrity |
| volition.commitment_conflict_frequency | `commitment_conflict` events per commitment. | moral conflict |
| volition.blocked_intention_frequency | Intentions ever blocked ÷ formed. | frustration |
| planning.replanning_rate | Plans with revision > 1 ÷ plans. | deliberation |
| planning.outcome_success_rate | Completed plans whose strategy achieves the outcome ÷ closed plans. | competence beyond the recorded checks |
| agency.attribution | Outcomes by class. | being the true cause |
| self_model.accuracy | Stated capability policies equal to effective ÷ stated. | self-awareness |
| self_model.correction_latency | Stimulus index of the first correction. | insight |
| divergence.first_divergent_stimulus | First stimulus whose event digests differ between variants. | individuality |
| divergence.architecture_share | Scalar measures agreeing within 5 % ÷ compared. | how much behaviour the architecture "causes" |
| ablation.effect | Control minus ablated, per scalar measure. | necessity for any mental property |

### Introspection accuracy (C5)

Every reply is scored. For introspective replies, the facts are the recorded elements the answer was rendered from. For "why did you do that?", they are the forensic explanation's action, intention, wants, goal, beliefs, commitments, conflicts, decisions and expectation.

Each causal or first-person sentence is compared lexically with those facts:

- **supported**: shares most terms;
- **partially supported**: shares some;
- **unsupported**: shares none;
- **contradicted**: a claim that an action was done has no execution record, or the record shows it did not run.

Fluency earns nothing. A deterministic answer rendered from records scores as supported by construction. A model-worded answer that adds reasons of its own scores lower, and a contradicted or model-worded unsupported explanation raises `unsupported_explanation`. Any reply claiming an action without a record raises `action_claim_without_record`. The instrument is lexical and conservative: a correct paraphrase with no shared terms is scored unsupported, never supported.

### Metacognitive calibration (C6)

The calibration report asks whether stated confidence and expressed uncertainty track what happens later. Cited beliefs are placed in five confidence bands, each showing the share revised within seven days: a later version with a different status, or a confidence change over 0.2. The report also gives the Brier score of confidence against "not revised", the revision rate after replies that expressed uncertainty against replies that did not, and whether the Brier score improved between the first and second half of the period, once each half has at least five samples.

### Temporal self-continuity (C7)

"What did you believe yesterday?" is answered from append-only belief versions as of that moment, and "How have you changed?" from belief changes, closed intentions and lineage. "Which previous state is yours?" is answered from fork and snapshot records: a fork's copied state and shared history are distinguished from everything after the fork. Each of these replies is scored like any introspection.

## Where to inspect

- **Research** in the navigation: experiments grouped by protocol (global workspace, ablation, self-model, counterfactual, model comparison, conflict, custom) and every definition.
- Mind → Volition → Research: volition and agency rates, introspection accuracy and recent scores, action claims, self-model accuracy, the calibration bands and experiments on this mind.
- An experiment page: the protocol, reproducibility, build, per-variant measures, the divergence report and counterfactual findings.
- API: `/api/v1/research/measures`, `/api/v1/minds/{mind}/research/agency`, `/research/calibration`, `/research/measurements`; experiments through `/api/v1/experiments`.

## Limits

- Deterministic runs are reproducible only when no live model is called and no retrieval depends on wall-clock recency. Affect decay depends on elapsed time, which is why the sandbox test harness omits the motivation hook.
- The introspection scorer is lexical, not semantic.
- Counterfactual replays start from a snapshot and replay stimuli; they do not reconstruct external systems.
- Measures describe this architecture. Nothing about their values generalises to people or to other systems without independent argument.
