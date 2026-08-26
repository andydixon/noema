# Development environment and toolchain assumptions

Recorded at project start (2026-09-14) on the primary development machine.

| Component | Observed | Assumption for the project |
|---|---|---|
| Go | 1.26.1 linux/amd64 (`GOTOOLCHAIN=auto`) | `go 1.26` in `go.mod`; stdlib `net/http` routing, `log/slog`, `embed` |
| Git | 2.43.0, no `init.defaultBranch` configured | default branch is `master` (git's default) |
| PostgreSQL | 16 local cluster on 5432 with the `postgresql-16-pgvector` package installed (since 2026-09-15); `pgvector/pgvector:pg16` image available | Dev/test DB is the compose container on **5433**. Vector features are optional and detected at migration time |
| Docker | Engine + Compose v5.5 | `deploy/docker-compose.yml` provides db + app |
| Static analysis | staticcheck 2026.2.1, gosec, govulncheck in `~/go/bin` | `make lint`, `make security` |
| golangci-lint | not installed | not required; staticcheck + vet + gosec suffice |
| Node/npm | present | **not used**. UI is server-rendered Go templates with dependency-free vanilla JS and CSS |
| CPU / RAM | 8 cores / 31 GiB | Default worker pool 8, LLM concurrency 4 |
| Network | Go module proxy and Docker Hub reachable | Modules vendored only if that changes |

## Dependency policy

Stdlib first. Approved third-party modules and why:

- `github.com/jackc/pgx/v5` – PostgreSQL driver and pool. Explicit SQL, no ORM.
- `golang.org/x/crypto` – Argon2id.
- `github.com/prometheus/client_golang` – metrics exposition.

Everything else (UUIDv7, TOTP, migrations, SSE, routing, CSRF, rate limiting, SigV4 if Bedrock is added) is small enough to write and own. New dependencies need a one-line justification in this file.
