This page is for whoever runs Noema: how to deploy it, what it needs, what runs on a schedule, what to back up, what to watch, and how to upgrade.

## Deployment

The daemon is one static binary plus PostgreSQL. `deploy/Dockerfile` builds a minimal image; `deploy/docker-compose.yml` runs PostgreSQL with pgvector and, with the `app` profile, the daemon. For a systemd host: install the binary, create a `.env` from the example with a real master key and database URL, run `noemad` under a dedicated user with `NOEMA_ENV=production`, and put a TLS-terminating proxy in front of the loopback listener. The daemon applies migrations on start under an advisory lock, so rolling restarts and multiple instances are safe.

Production settings to check:

- `NOEMA_ENV=production` (Secure cookies, no template reloading);
- a strong `NOEMA_MASTER_KEY` kept out of the repository and backed up separately from the database (without it, encrypted provider keys, integration secrets and TOTP secrets cannot be read);
- remove the bootstrap administrator variables after first start;
- budgets: `NOEMA_DAILY_TOKEN_BUDGET`, per-mind `daily_token_budget`, `NOEMA_MAX_CONCURRENT_LLM`;
- `NOEMA_MAX_WORKERS` roughly equal to the cores you want cognition to use.

## Scheduled jobs

| Job | Interval | What it does |
|---|---|---|
| auth.sweep / auth.purge | 5 min / 1 h | rate-limit bucket cleanup, expired sessions and pending logins |
| memory.decay | 6 h | lowers activation of unretrieved memories, never deletes |
| memory.embed | 1 min | embeds memories that lack a vector when an embedding model exists |
| predictions.resolve | 1 min | evaluates open predictions against their criteria |
| cognition.background | 5 min | runs consolidation, reflection and dreaming for minds whose interval elapsed and switches are on |
| approvals.expire | 5 min | expires stale approval requests |
| integrations.tick | 1 min | runs due integrations |
| brain.reconcile | 30 s | starts runtimes for newly active minds and stops disabled ones |

The system page shows each job's last run, duration and error.

## Backups

The database is the whole state, including history, snapshots and recorded model calls. Back it up with the PostgreSQL tools of your choice; the append-only tables compress well. The master key must be backed up separately and stored apart from the database dump. Mind archives (`noemactl snapshots <mind> export`) are portable, checksummed and secret-free; they are a convenient per-mind backup and a migration path between instances, but they do not include event history.

## Metrics and logs

`/metrics` exposes Prometheus metrics behind authentication: cycles and their duration per mind, thought candidates and admissions, events by type, model calls, tokens and cost by role and model, budget exhaustion, integration runs, HTTP request counts and latencies, and authentication failures. Logs are structured (`text` or `json`) and the handler redacts values that look like secrets. Every request has an id that appears in error envelopes and log lines.

## Audit

Every operator action (login, configuration change, policy edit, approval, correction, snapshot, fork, import, experiment run) is written to an append-only audit log with actor, target and details, and most also appear as `operator_action` events in the affected mind's own history. The audit page is searchable; the API exposes it to operators and above.

## Sessions, tokens and two-factor authentication

Sessions have idle and absolute expiry, are listed and revocable per user, and are all revoked on password change. API tokens are hashed at rest and optionally expire. Two-factor authentication is per user (TOTP with recovery codes); administrators can reset it. Repeated failed logins lock an account with a rising back-off; login attempts are rate-limited per address.

## Performance

{{include docs/performance.md}}

## Upgrades

Upgrades are forward-only migrations applied on start; each migration ships with a down script for emergencies. Read the changelog under [development](development) for behavioural changes, take a database backup, deploy the new binary, and watch the log for `applied migrations`. Snapshots taken by an older version remain importable as long as their schema version is supported; the archive format is versioned and validated.

## Shutting a mind down

Disable it (its runtime stops; state and history remain), archive it (hidden from lists), or delete it through the administrator API (which the interface confirms twice). None of these are decisions a mind can make, influence or resist; there is no self-preservation drive and no capability that touches its own lifecycle.
