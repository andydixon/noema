Configuration has two layers. **Daemon configuration** is fixed at start: where to listen, which database, the master key, resource limits. **Cognitive configuration** belongs to each mind, lives in the database, is edited from the interface or API, and is reloaded into the running mind without a restart.

## Daemon configuration

{{include docs/configuration.md}}

### Environment reference

| Variable | Default | Notes |
|---|---|---|
| `NOEMA_HTTP_ADDR` | `127.0.0.1:8080` | bind to a loopback address and put a TLS proxy in front, or bind `0.0.0.0` inside a container |
| `NOEMA_ENV` | `development` | `production` forces `Secure` cookies and disables template reloading |
| `NOEMA_LOG_LEVEL` / `NOEMA_LOG_FORMAT` | `info` / `text` | `json` for log shippers; the log handler redacts values that look like secrets |
| `NOEMA_DATABASE_URL` | compose database | pgx connection string; `sslmode` as your database requires |
| `NOEMA_MASTER_KEY` | unset | 32 bytes base64; required for encrypted secret references and TOTP |
| `NOEMA_BOOTSTRAP_ADMIN_USER` / `_PASSWORD` | unset | first administrator when the users table is empty; remove after first start |
| `NOEMA_RESEARCH_DIR` | `./research` | default path offered when creating a research-folder integration |
| `NOEMA_MAX_WORKERS` | 8 | size of the cognitive worker pool shared by all minds |
| `NOEMA_MAX_CONCURRENT_LLM` | 4 | concurrent model calls across the daemon |
| `NOEMA_DAILY_TOKEN_BUDGET` | 2 000 000 | global hard stop on model tokens per day; per-mind budgets sit under it |
| `NOEMA_SESSION_IDLE_TTL` / `NOEMA_SESSION_ABSOLUTE_TTL` | 12h / 7d | browser session expiry; API tokens have their own optional expiry |

A JSON file passed with `noemad -config path` can set the same keys; environment variables override the file.

## Cognitive configuration (per mind)

Each mind carries a `CognitiveConfig` document. It is shown on the mind overview, edited from the personality and dreaming pages and through `PATCH /api/v1/minds/{mind}`, and normalised on every write: out-of-range values fall back to defaults rather than being rejected silently or accepted dangerously.

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

## Development environment record

{{include docs/ENVIRONMENT.md demote=1}}
