Every function of the interface is available over a JSON HTTP API under `/api/v1`. The machine-readable description is served by the daemon at `/api/v1/openapi.json` and a copy is published alongside this documentation as [openapi.json](openapi.json). A test in the codebase fails if a route is added without being documented.

## Authentication

Two credentials are accepted. **Bearer API tokens** (`Authorization: Bearer noema_…`) are created per user from the account page, stored hashed, optionally expiring, and revocable. **Browser sessions** are cookies set by the login form; a session may call the API too, but unsafe methods then require the `X-CSRF-Token` header carrying the session's token. Anonymous requests get `401`.

## Roles

Users have one of three roles. **Observer** can read everything. **Operator** can additionally act: post messages and observations, create goals and predictions, approve requests, run background jobs, take snapshots, start replays and experiments. **Administrator** can additionally configure: minds' lifecycle, policies, providers, integrations, users, forks and imports. Every operation in the reference below lists its minimum role; a call below it gets `403`.

## Conventions

- Mind-scoped paths accept a slug or an id in `{mind}`.
- Lists return `{"items": [...]}` and often a `next` cursor; cursors are sequence numbers or ids, never offsets.
- Errors use one envelope: `{"error": {"code": "...", "message": "...", "request_id": "..."}}` with `400` for validation, `401`/`403` for authentication and roles, `404` for missing or out-of-scope resources (a resource belonging to another mind is a 404, not a 403), `409` for conflicts and replays, `429` for rate limits.
- Bodies are JSON; unknown fields are rejected on inputs that create or import things.
- Long-running operations (replay, experiment run) return `202` immediately and are polled.
- Timestamps are RFC 3339; ids are UUIDv7 and sort by time.

## Streaming

`GET /api/v1/events/stream` and `GET /api/v1/minds/{mind}/events/stream` are server-sent event streams. They accept the same filters as the event list, honour `Last-Event-ID` to replay missed events from the sequence, send heartbeats, and use per-client bounded buffers so a slow client cannot back-pressure the daemon.

## Webhooks

`POST /api/v1/webhooks/{id}` is the one unauthenticated route. It requires the `X-Noema-Timestamp` and `X-Noema-Signature` headers described under [integrations](integrations) and refuses everything else.

## Added in 0.2

`GET /api/v1/minds/{mind}/assertions` and `/assertions/actors` list what was said to a mind and by whom, apart from what it believes. `GET /api/v1/events/{id}/chain` returns the bounded causal chain around an event. `POST /api/v1/minds/{mind}/redactions` (administrators) performs an audited redaction and `GET` lists the ledger. Experiments accept `variants`, and snapshot, replay and experiment responses carry the runtime identity and, for replays, the compatibility label and divergence analysis.

## Route reference

The table below is generated from the OpenAPI document.

{{openapi}}
