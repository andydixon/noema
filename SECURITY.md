# Security

## Security model in one paragraph

Noema is an operator-controlled system that reads hostile data and may be granted the ability to act on real infrastructure. Its security rests on four boundaries: authenticated, role-checked access to the API and UI; treating every ingested byte and every model output as untrusted data; routing every external effect through an explicit capability and policy layer with human approval by default; and keeping minds isolated from one another. Everything the system does is recorded in an append-only event and audit log.

## Threat assumptions

- The operator and the host are trusted. Anyone with database or filesystem access owns the system.
- Users of the web UI are authenticated humans with one of three roles: `administrator`, `operator`, `observer`.
- Every document, file, web page, log line, metric label, repository, webhook body, API response and LLM response is potentially attacker-controlled and may contain instructions aimed at the mind. Such instructions are data. They never override policy.
- Language models are fallible and can be manipulated. Nothing a model says is executed, believed, or stored as fact without passing through code that validates structure, attaches provenance and applies policy.
- Network peers (LLM APIs, integrations) may be slow, wrong or malicious. Timeouts, size limits and schema validation apply everywhere.

The detailed enumeration is in `THREAT_MODEL.md`.

## Secret handling

- Secrets come from environment variables (`env:NAME`), secret files (`file:/path`) or values encrypted at rest with AES-256-GCM under `NOEMA_MASTER_KEY` (`enc:...`). The database stores references and ciphertext, never plaintext.
- Secrets are never rendered to HTML, returned by the API, written to logs, included in exports or snapshots, or sent to a model. Log fields carrying secrets are redacted by type at the logging layer.
- Provider test calls report success/failure only.
- Session tokens are stored hashed. Passwords use Argon2id. TOTP secrets are encrypted with the master key. Recovery codes are stored hashed.

## Authentication and sessions

Local authentication is implemented first: Argon2id, opaque random session tokens in `HttpOnly; Secure; SameSite=Lax` cookies, per-session CSRF tokens on every state-changing request, login rate limiting per IP and per account with exponential backoff, optional TOTP with recovery codes, session listing and revocation. OIDC can be added behind the same `auth.Authenticator` boundary.

## External integration risk

Integrations are disabled until configured, run at a declared trust level, are rate-limited, and only *observe* unless a capability is explicitly granted. Outbound HTTP uses a hardened client that blocks private and link-local addresses (SSRF), follows a bounded number of redirects, and caps response sizes. The research folder never executes content, extracts text in-process with size limits, and treats archive members as hostile paths.

## Capability policy defaults

Read-only capabilities default to `ASK`; anything that writes, sends, restarts, deletes or spends money defaults to `ASK` or `DENY`. `sudo_shell` and `delete_database` are `DENY` and cannot be set to `ALLOW` through the UI. Dreaming may never invoke a capability. There are no self-preservation, replication, concealment or permission-seeking drives, and shutdown is an operator control the mind cannot influence.

## Responsible disclosure

Please report vulnerabilities privately to the maintainer (see repository owner contact) rather than opening a public issue. Include reproduction steps and affected version. You will receive an acknowledgement within a few days. Fixes will be released with a CHANGELOG entry crediting the reporter unless anonymity is requested.

## Security testing

`make security` runs gosec and govulncheck. The test suite includes XSS, CSRF, SQL and command injection, path and archive traversal, webhook signature, prompt injection, cross-mind leakage, capability escalation and session lifecycle tests. See `PLAN.md` phase 16 and `tests/security`.
