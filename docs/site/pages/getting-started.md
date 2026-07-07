This page takes you from nothing to a running mind that you can talk to, inspect, snapshot and replay. It assumes a Linux or macOS machine with Docker available; the daemon itself is a single static binary and needs only PostgreSQL 16 (pgvector recommended, optional).

## Prerequisites

- Go 1.26 (the module pins a patched toolchain and will fetch it automatically), or the prebuilt container image.
- Docker with Compose, used here for PostgreSQL. Any PostgreSQL 16 works; the `pgvector/pgvector:pg16` image adds semantic retrieval.
- A terminal and a browser. No Node, no build step for the interface; templates and assets are embedded.

## 1. Clone and build

```
git clone git@github.com:andydixon/noema.git
cd noema
make build          # produces ./bin/noemad and ./bin/noemactl
```

`make tools` installs staticcheck, gosec and govulncheck if you intend to develop; they are not needed to run.

## 2. Start the database

```
make dev-db         # docker compose: PostgreSQL 16 with pgvector on 127.0.0.1:5433
```

The compose file lives in `deploy/docker-compose.yml`. It publishes the database on port 5433 to avoid colliding with a local PostgreSQL, and stores data in a named volume.

## 3. Configure

Copy the example environment file and set the two values that matter:

```
cp .env.example .env
openssl rand -base64 32      # paste as NOEMA_MASTER_KEY
```

| Variable | Purpose |
|---|---|
| `NOEMA_DATABASE_URL` | connection string; the example points at the compose database |
| `NOEMA_MASTER_KEY` | 32 bytes, base64. Encrypts provider API keys, integration secrets and TOTP secrets at rest. Without it those features are unavailable, and the daemon says so. |
| `NOEMA_BOOTSTRAP_ADMIN_USER` / `_PASSWORD` | optional first administrator, used only when the users table is empty |
| `NOEMA_HTTP_ADDR` | listen address, default `127.0.0.1:8080` |
| `NOEMA_RESEARCH_DIR` | default watched folder for the research integration |

Secrets never go into the database in clear. Provider keys are entered once in the interface and stored as `enc:` references; you can also reference an environment variable (`env:OPENAI_API_KEY`) or a file (`file:/run/secrets/key`). The [configuration page](configuration) has the full list.

## 4. Migrate and run

```
make migrate        # applies the embedded, numbered migrations
make run            # starts noemad with .env
```

The daemon applies migrations itself on start as well, under an advisory lock, so a fleet of instances cannot race. The log line `noemad ready url=http://127.0.0.1:8080` means the interface is up.

## 5. First login

Open the address in a browser. With no users, the interface shows a one-time **setup** page to create the first administrator (unless you set the bootstrap variables). Passwords are hashed with Argon2id; sessions are cookie-based with CSRF protection; you can add a second factor later from the account page.

Create an API token from **Account → API tokens** for the command line:

```
export NOEMA_URL=http://127.0.0.1:8080
export NOEMA_API_TOKEN=noema_…
./bin/noemactl status
```

## 6. Create a mind

Either click **Minds → New mind** and pick a personality template, or use the sample mind shipped with the repository:

```
./bin/noemactl mind create --slug dani --name Dani --template investigator
```

The sample seed `docs/samples/dani-seed.json` gives Dani a personality, a few memories and some claims to reason about. Import it from the minds page or:

```
curl -s -H "Authorization: Bearer $NOEMA_API_TOKEN" -H 'Content-Type: application/json' \
     --data @docs/samples/dani-seed.json $NOEMA_URL/api/v1/minds/import
```

The mind starts immediately. Its runtime wakes on new events, runs an idle cycle on a timer, and persists its workspace after every cycle.

## 7. Talk to it, and watch it think

Open the mind, go to **Conversation** and say something. Or from the shell:

```
./bin/noemactl ask dani "The payments service failed right after the database migration finished."
```

Then open **Thoughts**. You will see the message arrive as an observation, the perception that analysed it, the memories it recalled with their retrieval scores, the thought candidates and which ones won the workspace, and, if the mind decided to answer, the response intent and the response with the evidence it rests on. Everything in that list is an event; click any of them for its full context: parents, children, payload, and the model call if one was made.

Without a language model configured, all of this still works: perception is deterministic, retrieval is lexical and structural, and responses are rendered from templates over real state. Adding a model under **LLM** makes perception richer and answers more fluent; it does not change what the mind knows.

## 8. Inspect beliefs and memories

Under **Beliefs**, each proposition shows its confidence, status, evidence for and against, and the formula that produced the number. Mark a belief wrong and the correction becomes counter-evidence and, if you choose, a "do not infer this again" rule. Under **Memory**, search recalls with a visible score breakdown: lexical, semantic (if pgvector and an embedding model are configured), recency, importance, activation and relationship terms.

## 9. Snapshot, fork, replay

From **Snapshots**, take a snapshot. Fork it into `dani-b`, change something in the fork's personality, and use **Compare** to see the two diverge. Start a **Replay** from the snapshot: the recorded stimuli are fed to a disabled sandbox copy and every step is labelled exact, approximate or diverged. This is the loop the rest of the documentation keeps coming back to: change something, run it again, look at the difference.

## 10. Where to go next

- The [interface tour](ui-tour) walks every screen.
- [Configuration](configuration) explains the per-mind cognitive settings (workspace capacity, attention weights, budgets, background cognition).
- [Language models](llm-providers) covers providers, roles, budgets and prompt versions.
- [Capabilities](capabilities) explains how to let a mind do anything at all beyond thinking, and why the defaults say ASK.
- [Operations](operations) covers running it for real: backups, metrics, audit, upgrades.
