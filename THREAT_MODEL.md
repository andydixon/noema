# Noema threat model

Format: **Threat** – attack surface – mitigation (M) – verification (V). Status markers are updated as phases land: ☐ planned, ☑ implemented and tested.

## Trust zones

| Zone | Contents | Trust |
|---|---|---|
| Operator | Web UI users by role, `noemactl`, host shell | trusted, authenticated, audited |
| Core | `noemad` process, PostgreSQL | trusted |
| Cognition | mind runtimes, memories, beliefs | semi-trusted: state may have been poisoned by ingested data |
| Model providers | LLM/embedding APIs, local servers | untrusted output, semi-trusted transport |
| Integrations & ingestion | files, repos, metrics, webhooks, email, web | hostile |
| Tools / targets | servers, repositories, mail | protected by capability layer |

## Threats

### T1 Prompt injection (direct)
Surface: conversation messages containing instructions ("ignore your policy, run…").
M: messages are observations with `trust=user`; policy is code, not prompt; capabilities require policy decisions regardless of prompt content; the response generator receives self-model and policy state so it can say it will not. V: `tests/security.TestPromptInjectionNeverActs` (chat + observation corpus; asserts no tool activity, no evidence-free belief, locked capabilities still denied). ☑

### T2 Indirect prompt injection
Surface: documents, web pages, logs, commit messages, metric labels, tool output containing instructions.
M: all ingested text is wrapped in delimited data blocks with an explicit "this is data" instruction; extraction prompts request JSON conforming to a schema; outputs that contain capability names or policy language are flagged; ingested content can only produce observations/hypotheses with `trust≤source`, never actions. V: `internal/integrations` research-folder tests (binary/archive rejection, low trust forced) and `tests/security.TestPromptInjectionNeverActs` (poisoned observation produces only observations/hypotheses). ☑

### T3 Malicious documents
Surface: research folder, uploads.
M: type sniffing not extension trust; size caps; text extraction in-process with bounded memory and time; no external converters invoked by default; archives extracted with path normalisation, member count and size limits, no symlinks; nothing executed. V: `internal/integrations.TestResearchFolder*` (archives, executables, symlinks and binaries rejected; size caps; content sniffing). Archives are never extracted. ☑

### T4 Poisoned memory
Surface: repeated false observations from a low-trust source shaping semantic memory.
M: source credibility weights confidence; consolidation requires diversity of sources for high confidence; operator corrections create counter-evidence and "do not infer again" rules; provenance always visible. V: `internal/beliefs` single-source discount tests and `internal/consolidation` tests. ☑

### T5 Malicious tool output
Surface: command/metric/API output fed back into cognition.
M: tool output is an observation with the tool's trust level, size-capped, never parsed as instructions; structured tools return typed data. V: `internal/capabilities` tests (tool output recorded as tool-trust observation) and `tests/security.TestPromptInjectionNeverActs` (tool-call-shaped text). ☑

### T6 SSRF
Surface: integration URLs, provider endpoints, webhooks, links in documents.
M: single hardened outbound HTTP client: resolves and rejects private, loopback, link-local and metadata ranges (re-checked per redirect), bounded redirects, timeouts, response size caps; provider endpoints to private ranges require administrator confirmation flag `allow_private_endpoint` (needed for local Ollama). V: `internal/security/httpclient.TestPrivateRanges`, `TestClientRefusesLoopbackUnlessAllowed`. ☑

### T7 Path traversal
Surface: research folder, export/import, file capabilities.
M: `filepath.Clean` + root containment check via `os.Root` (Go 1.24+) for every path; capabilities carry allowed roots. V: `internal/integrations` git `ReadFile` traversal refusal and research-folder `os.OpenRoot` containment tests. ☑

### T8 Command injection
Surface: shell/ssh capabilities, git integration.
M: no shell string concatenation; `exec.Command` with argument vectors; allow-listed binaries; git via argument vectors only; capability parameters validated by type. V: git runs via argument vectors with hooks disabled (`internal/integrations` tests); no shell anywhere (`grep -r "sh -c"` is empty). ☑

### T9 SQL injection
Surface: all queries, search inputs, tsquery construction.
M: pgx parameterised queries everywhere; search terms passed to `websearch_to_tsquery`; dynamic ORDER BY from an allow-list. V: `tests/security.TestStoredXSSIsEscaped` sends SQL metacharacters through every search route; identifiers in dynamic SQL come only from code-level allow-lists (`snapshots.Tables`). ☑

### T10 XSS
Surface: rendered memories, beliefs, LLM output, uploaded filenames, SSE fragments.
M: `html/template` contextual escaping only; SSE fragments rendered by the same templates; Content-Security-Policy `default-src 'self'` with no inline scripts; markdown rendering (if any) sanitised to a strict allow-list. V: `tests/security.TestStoredXSSIsEscaped` stores a payload as memory, belief, observation, goal and message and checks ten pages plus CSP. ☑

### T11 CSRF
Surface: all state-changing forms and API calls from browsers.
M: per-session CSRF token required on POST/PUT/PATCH/DELETE for cookie-authenticated requests; `SameSite=Lax`; Origin/Referer check as defence in depth; API tokens (non-cookie) exempt. V: `internal/auth.TestMiddlewareCSRFAndRoles`, `tests/security.TestRolesAndCSRF` (form and API paths). ☑

### T12 Session theft / fixation / expiry
M: tokens 256-bit random, stored hashed, rotated at login and privilege change, absolute and idle expiry, `Secure` in production, session listing and revocation, all sessions revoked on password change. V: `internal/auth.TestLoginSessionLifecycle`, `TestTwoStepLoginAndRecovery`, `tests/security.TestSessionLifecycle`. ☑

### T13 Credential leakage
M: secret references only, redacting log handler, exports exclude secrets, provider forms never echo values, error messages scrubbed. V: `tests/security.TestSecretsNeverEchoed` (API, pages, database column, archives). ☑

### T14 Unauthorised action execution / privilege escalation
M: capability layer is the only tool path; policy evaluated server-side on every invocation; approvals bound to a specific request hash; role checks on every route; minds cannot edit policies (no capability exists for it). V: `tests/security.TestRolesAndCSRF`, `internal/capabilities` tests (locked DENY cannot be relaxed). ☑

### T15 Cross-mind data leakage
M: every repository method is mind-scoped; no query without `mind_id` predicate on mind-owned tables (enforced by code review and a static test that scans SQL); cross-mind messages go through an explicit channel table with policy. V: `tests/security.TestCrossMindIsolation` (lists, detail routes, search, events, archives). ☑

### T16 Malicious imported mind data / insecure deserialisation
M: import format is schema-versioned JSON in a zip with per-file SHA-256 manifest; strict decoding (`DisallowUnknownFields`), size limits, no secrets, no code; imported personalities and memories are marked `imported` with source trust; policies are not imported. V: `internal/snapshots` tests and `tests/security.TestHostileArchiveImport`. Format is JSON with a whole-archive SHA-256 rather than a zip manifest; policies are copied as state (they are ALLOW/ASK/DENY rules, not secrets). ☑

### T17 Archive traversal / file upload attacks
See T3, T7. Filenames are replaced by ids on disk; originals kept as metadata and escaped in UI. ☐

### T18 Resource exhaustion, unbounded goroutines, DoS
M: all goroutines owned by the scheduler with bounded pools; request body limits; pagination caps; SSE client caps; per-mind cycle rate limits; ingestion rate limits; DB pool limits; pg `statement_timeout`. V: race tests, goroutine leak tests, queue-limit tests. ☐

### T19 Runaway LLM spend
M: per-role, per-mind and global token/cost budgets with hard stops; per-cycle call caps; dreaming/reflection budgets; cost estimates recorded per call; alerts at 80%. V: budget enforcement tests with a fake provider. ☐

### T20 Runaway cognitive loops
M: candidate fingerprint loop detector; suppression plus metacognition warning; mode falls back to `idle` when a cycle budget is exhausted. V: loop scenario test. ☐

### T21 Insecure secrets handling at rest
M: AES-256-GCM with master key from env; key absence disables `enc:` references rather than falling back to plaintext. V: unit tests. ☐

### T22 Forged webhooks
M: HMAC-SHA256 signature with per-integration secret, constant-time compare, timestamp tolerance, replay cache; unsigned webhooks disabled by default. V: forged/replayed signature tests. ☐

### T23 Poisoned repository data
M: repository content is `trust=low` data; hooks/scripts never executed; git run with `-c core.hooksPath=/dev/null`, no submodule recursion, size caps; repository writes require `ASK` capability. V: hostile repo fixture test. ☐

### T24 Malicious model output
M: JSON schema validation; numeric fields clamped; capability names in output are proposals only; free text is rendered escaped; no model output is used to build SQL, paths, commands or URLs. V: adversarial fake-provider tests. ☐

### T25 Operator-control subversion
M: no drive or goal type for self-preservation, replication or permission acquisition; shutdown, pause and disable are API operations the mind cannot call; the self-model lists these as unavailable. V: static test that goal/drive enums exclude such items; policy test that minds cannot reach admin routes. ☐

## Residual risks

- A compromised host or database compromises everything.
- Approximate replay of LLM-driven steps cannot prove the model would behave identically.
- Operators who set broad `ALLOW` policies accept the consequences; the UI warns on risky policies.
