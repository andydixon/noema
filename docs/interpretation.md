# Interpretation boundary

Noema models cognitive processes. It does not prove, and its authors do not claim, that it is conscious, sentient, self-aware or a person.

The system contains components named after psychological concepts: attention, working memory, emotion-like state, curiosity, drives, self-model, reflection, dreaming. These names describe the *functional role* each component plays in the architecture. They are implemented as ordinary data structures and deterministic rules, sometimes assisted by language models.

When a mind reports that it is "curious" or "concerned", it is reporting the value of a stored scalar that the architecture updates by rule. When it says "I believe", it refers to a row in a belief table with evidence links. Such reports are useful because they are grounded in inspectable state, not because they indicate experience.

Users and researchers should therefore:

- treat resemblance to emotion, preference or identity as a property of the model being run, to be studied, not as evidence of subjective experience;
- rely on the evidence and provenance views rather than on fluent self-description;
- avoid attributing moral patienthood to a mind on the basis of its behaviour within Noema.

The platform is deliberately not crippled to avoid these resemblances, because they are what makes the mechanisms worth studying. The interpretive restraint is the user's responsibility, and the UI states this boundary on the about page and in mind exports.

## Operational meanings

Every psychological word used in the interface, the documentation and a mind's own reports has one operational meaning: a piece of stored state, the rule that changes it, and a page where it can be inspected. The table is the reference; where the interface or a generated sentence uses one of these words, this is what it refers to and nothing more.

| Word | What it names in Noema | What changes it | Where to inspect | What it does not mean |
|---|---|---|---|---|
| **believes** | a row in `beliefs` with a proposition, an epistemic kind and evidence links, whose status is not retracted | evidence rows added by observation, assertion, prediction outcome or operator action; the arithmetic in docs/beliefs.md | mind → beliefs, belief detail, provenance | assent, conviction or a probability that the proposition is true |
| **confidence** (belief) | the epistemic score in [0.02, 0.98] computed from evidence families | the same arithmetic; source reliability adjusts credibility once history exists | belief detail ("what this number means") | a calibrated probability; the page shows a separate calibrated estimate when predictions justify one |
| **confidence** (affect) | one of ten affect-like scalars in the mind's state | the rule table `motivation.Rules` (prediction errors lower it) and hourly decay to the personality baseline | mind overview, `noemactl state show` | a feeling; unrelated to any belief's score |
| **knows / unknown** | a belief with live evidence, or the `unknown` status when none is live | evidence expiry, supersession of transient states, retraction | belief detail, introspection replies | certainty or ignorance in the human sense; "unknown" is the absence of recorded evidence |
| **remembers** | retrieval of an episodic or semantic memory row by similarity, recency and importance | the retrieve process each cycle; consolidation writes semantic memories from episodes | mind → memory, memory detail with retrieval scores | recollection or personal experience |
| **forgets** | a memory falling below retrieval thresholds or being redacted | decay of retrieval weight; the audited redaction path (docs/privacy.md) | memory detail, mind → redactions | deletion of history; rows are never deleted |
| **attends to / notices** | a candidate winning the workspace competition for one cycle | attention feature weights (novelty, goal relevance, contradiction, context relevance) modulated by drives | workspace page, per-cycle candidates and scores | awareness |
| **working memory** | the small decaying set of items in the workspace snapshot (default nine) | admission of winners, decay per cycle | workspace page | short-term memory in a person |
| **salience** (memory) | how strongly an item presented itself when stored: a score set by the producer | fixed at write time | memory detail | importance or truth; those are separate fields |
| **curious / curiosity** | a drive value and an affect scalar; and the `curiosity` process that turns unexplained observations, prediction errors and contradictions into investigation goals | drive baselines from personality, modulation by events, decay | mind overview (drives), goals of kind investigation | interest or desire |
| **wants / goal** | a row in the `goals` tree with kind, status, priority and origin | operators (tasks), curiosity (investigations), reflection (maintenance); the goal monitor updates status | mind → goals | volition; goals never grant capability |
| **drives** | eight configured scalars with ranges that scale attention weights | events through the rule table; six-hour decay | mind overview, workspace effective weights | needs; the set is closed and contains no self-preservation drive |
| **feels / mood / valence / arousal / concern / frustration** | affect-like scalars in [0, 1] or [−1, 1] | the rule table and decay; changes above 0.1 emit `emotion_changed` with reasons | mind overview, event log | experience; the scalars exist so that their influence on attention can be studied |
| **expects / predicts** | a row in `predictions` with a stated confidence and a due time | the prediction process; resolution against later observations | mind → predictions; belief detail shows the calibrated estimate | foresight; predictions are the only place a score is tested against outcomes |
| **surprised** | a prediction resolved as a miss, or an observation with high novelty | resolution and the novelty feature | prediction detail, event log | emotion; the affect rule raises arousal from the same event |
| **trusts / trust level** | the channel trust of a source (operator, user, system, tool, source, low) and its recorded reliability per subject | configuration for the level; outcomes of beliefs it supported for reliability | integrations page, belief evidence rows | a judgement of character; reliability is per family and per subject |
| **understands / interprets** | the deterministic reading of an utterance into an assertion (speaker, attribution, stance, certainty, modality, hypothetical, quoted) with an interpretation confidence | the rule set in `epistemics.Interpret`, no model involved | conversation view, assertions list | comprehension; the confidence is about the reading, not the world |
| **thinks / considers** | a thought candidate produced by a process and scored for the workspace | the producing process | mind → thoughts, workspace | deliberation |
| **reflects** | the scheduled reflection job that reviews recent activity and writes maintenance goals and notes | the schedule and `reflection_enabled` | mind → consolidation, reflection events | introspection in the human sense |
| **dreams** | the optional idle mode that replays a deterministic sample of memories, links similar ones with low confidence and proposes speculative hypotheses | the dream schedule and allowed hours; off by default | dreams page, dream detail | imagination; dreaming never executes actions or changes authority |
| **self-model** | the mind's stored description of itself: personality traits and values plus a capability list kept equal to the effective policy | personality edits by operators; `SelfModelSync` after policy changes, emitting `self_model_updated` | personality page, `/api/v1/minds/{mind}/selfmodel` | self-awareness; a mind cannot claim tools it does not have |
| **metacognition** | explicit checks over the mind's own beliefs (insufficient evidence, single source, overconfidence, …) that add counter-evidence or warnings | the per-cycle process and its thresholds | mind overview warnings, metacognition events | wisdom; each check is a rule with a named threshold |
| **decides / acts** | a proposed action passing through the capability layer to ALLOW, DENY or REQUIRE_APPROVAL | policy rows, operator approvals | approvals page, policies page, audit | agency; nothing a mind believes or is told can widen what it may do |

A report such as "I believe the pump is failing, though I am uncertain" therefore expands to: a belief row exists for that proposition, its score is in a range the introspection template calls uncertain, and its primary uncertainty is named on the belief page. The sentence is a rendering of state, and the state is the thing to check.
