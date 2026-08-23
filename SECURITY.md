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

Integrations are disabled until configured, run at a declared trust level, are rate-limited, and only *observe* unless a capability is explicitly granted. Outbound HTTP uses a hardened client that blocks private and link-local addresses (SSRF), follows a bounded number of redirects, and caps response sizes. The research folder never executes content, extracts text in-process with size limits, and rejects archives, executables, symlinks and binaries outright rather than opening them.

## Authority lives outside cognition

> A mind may be deceived. Deception must not imply authority.

Prompt injection, poisoned documents, hostile model output and manipulated memories can all change what a mind *thinks*. None of them can change what it may *do*. The boundary is `capabilities.Service.Invoke`: it looks the capability up in a registry built in code, validates parameters, evaluates the operator's policy rules, records the decision, and only then executes, asks or denies. Cognition can produce a request; it cannot produce a permission. There is no capability that edits policy, grants approvals or touches a mind's lifecycle, so there is nothing a deceived mind could ask for that would widen its own authority. Prompt delimiters and "this is data" markers are defence in depth for the quality of cognition; they are not the security boundary. `tests/security.TestDeceivedMindHasNoAuthority` forges action requests and hostile model output and checks that nothing executes and policy is unchanged.

## Privacy and redaction

Cognitive history is append-only, and rows are never deleted. Sensitive content is removed through one audited path: an administrator redacts an event, memory, message, model call, belief or assertion; the content columns become `[redacted]`, a ledger row keeps a hash of what was removed, a `redaction` event is appended, derived rows can be cascaded, and stored snapshot archives are scrubbed. Identifiers, links, timestamps and scores survive so provenance and replay keep their shape. Backups and exports made before a redaction are outside the system's reach. Definitions, the threat model and the reasons cryptographic erasure was not adopted yet are in `docs/privacy.md` (ADR-0009).

## Capability policy defaults

Read-only capabilities default to `ASK`; anything that writes, sends, restarts, deletes or spends money defaults to `ASK` or `DENY`. `sudo_shell` and `delete_database` are `DENY` and cannot be set to `ALLOW` through the UI. Dreaming may never invoke a capability. There are no self-preservation, replication, concealment or permission-seeking drives, and shutdown is an operator control the mind cannot influence.

## Responsible disclosure

Please report vulnerabilities privately to the maintainer (see repository owner contact) rather than opening a public issue. Include reproduction steps and affected version. You will receive an acknowledgement within a few days. Fixes will be released with a CHANGELOG entry crediting the reporter unless anonymity is requested.

## Security testing

- `make security` runs gosec (G104 excluded: unchecked errors are covered by staticcheck and explicit `//nolint:errcheck` markers) and govulncheck. Both are clean as of 0.1.0 with the pinned toolchain in `go.mod`.
- `go test -race ./tests/security/...` runs the cross-cutting suite: prompt injection through chat and observations (no tool activity, no evidence-free beliefs), role and CSRF enforcement on form and API paths, cross-mind isolation, stored-XSS escaping and CSP on every page, secret non-disclosure (API, pages, database, archives), hostile archive import, session lifecycle and security headers.
- Package tests cover the SSRF dialer, research-folder and git safety, webhook signatures and replay, capability policy and locked capabilities, TOTP vectors and two-step login.
- `THREAT_MODEL.md` maps each threat to its mitigation and the test that verifies it; a claim there cites only a test that asserts that claim.
- Request edge: bodies are size-capped and buffered; NUL bytes in paths, queries or bodies are refused with a 400 before any query runs.

## Two-factor authentication

Operators can enable TOTP (RFC 6238) on the account page. The secret is stored encrypted under `NOEMA_MASTER_KEY`; ten single-use recovery codes are stored hashed and shown once. Login becomes two steps: password, then a code exchanged against a five-minute pending token bound to the client address. Administrators can reset a user's second factor from the users page.
