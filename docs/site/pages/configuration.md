Each mind carries its own cognitive configuration: how many thoughts it can hold, how it weighs what to attend to, how fast it cycles, how much it may spend on models, and which background processes run. These settings are data, edited from the interface or API, normalised on every write and reloaded into the running mind without a restart. They are the knobs an experiment turns.

## The settings

Out-of-range values fall back to defaults rather than being rejected silently or accepted dangerously; every change is an operator action in the mind's history, and the mind encodes it as an autobiographical memory.

| Field | Default | Effect |
|---|---|---|
| `workspace_capacity` | 7 | thoughts admitted per cycle; the weakest are displaced when full |
| `working_memory_capacity` | 9 | decaying buffer of recently active items |
| `attention_weights` | see below | per-feature multipliers used by attention scoring |
| `cycle_min_interval` | 500ms | debounce between stimulus-triggered cycles |
| `idle_cycle_interval` | 60s | timer for idle cycles when nothing arrives |
| `max_cycles_per_minute` | 60 | rate budget; excess triggers are skipped and logged |
| `llm_calls_per_cycle` | 3 | shared cap for processes and handlers in one cycle |
| `daily_token_budget` | 500 000 | per-mind token budget, enforced by the router |
| `working_memory_decay` | 0.85 | activation multiplier per cycle |
| `loop_detect_window` / `loop_detect_threshold` | 20 / 5 | repeated candidate fingerprints that trigger a metacognition warning |
| `consolidation_enabled` / `_interval` / `_min_episodes` / `_llm_calls` | on / 6h / 3 / 5 | scheduled generalisation of episodes |
| `reflection_enabled` / `_interval` / `_llm_calls` | on / 12h / 2 | scheduled review of the period's mistakes and lessons |
| `narrative_llm` | off | let a model word the internal narrative (deterministic otherwise) |
| `notify_min_confidence` / `notify_min_salience` / `notify_cooldown` | 0.85 / 0.7 / 1h | thresholds and aggregation for operator notifications |
| `dreaming_enabled` / `dream_hours` / `dream_interval` / `dream_llm_calls` / `dream_max_memories` / `dream_max_hypotheses` | off / 01-06 / 24h / 2 / 40 / 5 | the dreaming mode; see [dreaming](dreaming) |

### Attention weights

Attention scoring uses a fixed set of ten features, each computed deterministically and multiplied by the mind's weight: `salience`, `urgency`, `novelty`, `affect`, `goal_relevance`, `consequence`, `confidence`, `contradiction`, `operator_priority` and `context_relevance`. Defaults favour urgency (1.2), goal relevance (1.1) and operator priority (1.5). Drives and affect modulate the effective weights within bounds each cycle, and the effective weights used are recorded on every `thought_selected` event, so you can always see why one candidate beat another. The [cognition page](cognition) describes each feature.

## Runtime settings

A small set of tunables that are safe to change while running (the global consolidation and dreaming pause switches, notification behaviour) are stored in the database and edited in the interface; they take effect on the next scheduler tick. Everything else requires a restart or, for per-mind settings, a `reload` command that the interface issues automatically after an edit.

## Daemon-wide limits

Above the per-mind settings sit a few daemon-wide limits: a global daily token budget that no mind can exceed, a cap on concurrent model calls, and the size of the worker pool that runs cognitive processes for every mind. Per-mind budgets are enforced under the global one, so one talkative mind cannot starve the others. Secrets used by any part of the system (provider keys, integration secrets, second-factor secrets) exist only as references encrypted under a master key, never as values in configuration.
