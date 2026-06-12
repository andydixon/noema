# ADR-0008: PostgreSQL with explicit SQL and embedded migrations

Date: 2026-09-14  
Status: Accepted

## Context
Requirement: PostgreSQL, proper migrations, no ORM auto-mutation, optional pgvector.

## Decision
pgx v5 with pgxpool; repository types per domain with hand-written SQL; numbered `NNNN_name.up.sql` / `.down.sql` files embedded from `migrations/` and applied by a small runner (advisory lock, `schema_migrations` table with checksum). The embeddings migration uses a PL/pgSQL block that creates vector objects only if the extension is available; the runtime detects availability at start.

## Consequences
- No hidden schema changes; migration state visible in the System page and `noemactl migrate status`.
- Tests needing the database use a per-run schema and skip when no URL is configured.

## Alternatives considered
- golang-migrate/goose: fine tools, but a 150-line runner avoids a dependency and matches our exact needs.
- sqlc: attractive later; deferred to keep the toolchain minimal.
