# Configuration

Precedence (later wins): built-in defaults → JSON file given by `noemad -config path` → environment variables → runtime settings stored in the `settings` table and edited in the UI (only for tunables that are safe to change at runtime).

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `NOEMA_HTTP_ADDR` | `127.0.0.1:8080` | Listen address |
| `NOEMA_ENV` | `development` | `production` forces Secure cookies and disables the setup page after bootstrap |
| `NOEMA_LOG_LEVEL` | `info` | debug, info, warn, error |
| `NOEMA_LOG_FORMAT` | `text` | text or json |
| `NOEMA_DATABASE_URL` | – | PostgreSQL DSN (required) |
| `NOEMA_MASTER_KEY` | – | base64 32-byte key for `enc:` secret references |
| `NOEMA_BOOTSTRAP_ADMIN_USER` / `_PASSWORD` | – | Creates the first administrator when no users exist |
| `NOEMA_RESEARCH_DIR` | `./research` | Watched ingestion folder |
| `NOEMA_MAX_WORKERS` | `8` | Cognitive worker pool size |
| `NOEMA_MAX_CONCURRENT_LLM` | `4` | Concurrent model calls |
| `NOEMA_DAILY_TOKEN_BUDGET` | `2000000` | Global hard stop |

## Secret references

Wherever a secret is configured (provider API keys, webhook secrets) the stored value is a reference:

- `env:OPENAI_API_KEY` – read from the environment at use time
- `file:/run/secrets/openai` – read from a file
- `enc:<base64 ciphertext>` – encrypted with `NOEMA_MASTER_KEY` (AES-256-GCM); produced when a secret is typed into the UI

Plaintext secrets are never stored, exported or displayed.

## Config file

The JSON file mirrors the environment variables in lower snake case, e.g.

```json
{ "http_addr": "0.0.0.0:8080", "log_level": "debug", "database_url": "postgres://..." }
```

Secrets should not be placed in the config file; use environment variables or secret files.
