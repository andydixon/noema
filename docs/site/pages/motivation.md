Motivation is the set of internal pressures that make a mind attend to some things rather than others when nothing external forces the choice. Noema implements it with three small, explicit structures, each visible and resettable.

## Goals

Goals form a tree: an investigation may have sub-goals, a sub-goal may have blockers. Each has a kind, a status (active, completed, abandoned), a priority, progress, an owner (operator or the mind itself) and an origin event. The goal monitor process watches for stalls and blockers and raises them as thoughts; curiosity can create investigation goals from unresolved contradictions; operators create, edit and abandon goals from the interface, the API or `noemactl goal`. Active goals feed the `goal_relevance` attention feature, so a mind with a goal about the payments service notices payments events more readily.

## Drives

Drives are a **closed set** of eight named pressures: `curiosity`, `uncertainty_reduction`, `task_completion`, `consistency`, `novelty`, `social_coherence`, `exploration` and `rest`. Each has a value, a baseline computed from personality (accuracy raises uncertainty reduction, persistence raises task completion, evidence raises consistency, sociability raises social coherence, curiosity and risk tolerance raise exploration), a floor and a ceiling. Drives move by a rule table in response to events (a resolved contradiction lowers uncertainty reduction; a completed goal lowers task completion; a long quiet period raises curiosity and novelty) and decay towards baseline. There is deliberately **no self-preservation drive**, no replication drive and no drive to seek permissions; the set is defined in code and cannot be extended at runtime.

## Affect

Affect is a vector of ten dimensions: valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task pressure and load. It is moved by the same rule table (a refuted prediction raises uncertainty and lowers confidence; a contradiction raises concern; repeated loops raise frustration; an operator correction raises engagement) and decays towards a baseline derived from personality. Affect modulates attention weights within bounds: high arousal raises urgency weight, high concern raises contradiction weight, high load lowers novelty weight. Every change is an `emotion_changed` or `drive_changed` event with the rule that fired.

## What this is and is not

These structures make behaviour explainable ("it attended to the alert because concern was high after two contradictions") and steerable (an operator can reset them or change the personality that sets their baselines). They are not claims about feeling. The [interpretation boundary](concepts#the-interpretation-boundary) applies in full.

{{include docs/motivation.md}}
