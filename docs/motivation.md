# Goals, drives and cognitive state

## Goals

Goals form a tree (`goals`) with kind (task, investigation, question, maintenance), status, priority, progress, blockers, owner and origin event. Operators create task goals; curiosity creates investigation goals from unexplained observations, prediction errors and contradictions; reflection (phase 10) adds maintenance goals.

Goals influence cognition through the `goal_relevance` attention feature: each candidate's text is compared with active goals (token overlap weighted by priority). The goal monitor process raises stalled goals and new observations that touch a goal. Goals never grant capabilities; acting still goes through the policy layer.

## Drives

Eight drives with operator-configured ranges: curiosity, uncertainty reduction, task completion, consistency, novelty, social coherence, exploration, rest. Baselines derive from personality traits and values. The set is closed in code; there is no self-preservation, replication or acquisition drive and no runtime way to add one (threat T25).

Drives modulate attention weights each cycle (`motivation.Modulate`): curiosity scales novelty, task completion scales goal relevance, uncertainty reduction scales contradiction, exploration lowers the pull of current context. The workspace page shows the effective weights next to the configured ones.

## Affect-like state

Ten scalars: valence, arousal, curiosity, concern, frustration, confidence, uncertainty, engagement, task pressure, load. They are updated by an explicit rule table (`motivation.Rules`) from events — contradictions raise concern and uncertainty, prediction errors raise arousal and lower confidence, completed goals raise valence and relieve pressure, and so on — then decay towards personality baselines (half-life one hour; six hours for drives). Changes above 0.1 emit `emotion_changed` / `drive_changed` events with the reasons.

This is a functional model, not a claim of feeling (docs/interpretation.md). Everything is visible on the mind overview and via `noemactl state show`, and operators can reset it to baseline or adjust drive ranges; both are recorded as operator actions.
