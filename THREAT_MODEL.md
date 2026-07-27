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
M: content sniffing, not extension trust; size caps; text extraction in-process with bounded memory; no external converters; **archives (zip, tar, gz, 7z, rar), executables, disk images, symlinks and binary files are rejected and recorded as rejected; nothing is ever extracted or executed**. V: `internal/integrations.TestResearchFolder*` (archive, executable, symlink and binary rejection; size caps; sniffing). ☑

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
M: the research folder is opened with `os.OpenRoot`, so every member path is resolved inside the root and symlinks cannot escape; git file reads go through `git show ref:path` with paths validated against traversal; there are no file-path capabilities. V: `internal/integrations` git `ReadFile` traversal refusal and research-folder containment tests. ☑

### T8 Command injection
Surface: shell/ssh capabilities, git integration.
M: no shell string concatenation; `exec.Command` with argument vectors; allow-listed binaries; git via argument vectors only; capability parameters validated by type. V: git runs via argument vectors with hooks disabled (`internal/integrations` tests); no shell anywhere (`grep -r "sh -c"` is empty). ☑

### T9 SQL injection
Surface: all queries, search inputs, tsquery construction.
M: pgx positional parameters for every value; search terms go to `websearch_to_tsquery` as parameters; the only SQL assembled from strings uses identifiers from code-level allow-lists (`snapshots.Tables`, `information_schema` column names) and never request input; NUL bytes are refused at the request edge with a 400 so they cannot reach the database. V: `tests/security.TestSQLInjectionHasNoEffect` (metacharacter payloads through every search, filter and free-text route, asserting that users, policies, sessions and tokens are unchanged, seeded content is intact and payloads are stored verbatim) and `TestDynamicSQLIdentifiersAreAllowListed` (static scan for `Sprintf`-built statements outside the allow-list). ☑

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
M: the archive is one JSON document (`kind`, `version`, whole-archive SHA-256 checksum); import decodes with `DisallowUnknownFields`, caps size (32 MiB) and rows, allow-lists table and column names, requires the checksum to match, remaps every id and forces `mind_id`; archives contain no secrets or references to them; **policy rules in an archive are never imported** (forks on the same instance keep theirs) and the omission is recorded in the audit log and the mind's history. V: `internal/snapshots.TestSnapshotForkImportCompare` (tampering, unknown tables, policy exclusion on import versus fork) and `tests/security.TestHostileArchiveImport` (malformed, tampered, oversized and role-restricted imports). ☑

### T17 Archive traversal / file upload attacks
The only uploads are JSON documents (seed, archive, personality) decoded strictly with size caps; nothing is written to disk under a caller-chosen name, and research-folder archives are rejected (T3). V: `tests/security.TestHostileArchiveImport`, `internal/integrations` archive rejection. ☑

### T18 Resource exhaustion, unbounded goroutines, DoS
M: goroutines are owned by the scheduler with bounded worker pools; request bodies are capped and buffered at the edge; list limits are clamped in every store; SSE fan-out uses per-client bounded buffers; cycles are rate-budgeted per mind; webhook deliveries are rate-limited per address; integrations run with timeouts. V: `internal/events.TestBusBoundedFanout`, brain cycle-budget coverage in `TestPauseResetAndCancellation`, webhook rate-limit tests, `tests/perf` ceilings. Not yet covered: goroutine-leak tests and a database `statement_timeout`. ◐

### T19 Runaway LLM spend
M: per-mind and global daily token budgets with hard stops, per-cycle call caps, consolidation, reflection and dreaming call budgets, cost estimates recorded per call. V: `internal/llm/router.TestRouterOverrideBudgetRecordingAndReplay` (budget exhaustion with a fake provider); dreaming and consolidation budget tests. Not implemented: an alert at 80 %. ◐

### T20 Runaway cognitive loops
M: candidate fingerprint loop detector with suppression and a metacognition warning; cycle rate budget skips excess triggers. V: `internal/brain.TestLoopDetectorSuppressesRepeats`. ☑

### T21 Insecure secrets handling at rest
M: AES-256-GCM under `NOEMA_MASTER_KEY`; without the key, `enc:` references and TOTP enrolment are unavailable rather than falling back to plaintext. V: `internal/security/secrets` tests, `internal/auth.TestTwoStepLoginAndRecovery` (enrolment refused without a key). ☑

### T22 Forged webhooks
M: HMAC-SHA256 over `timestamp.body` with a per-integration secret, constant-time compare, ±300 s tolerance, replay cache, 256 KiB cap, per-address rate limit; a webhook with no secret refuses every delivery. V: `internal/integrations` signature, tolerance and replay tests. ☑

### T23 Poisoned repository data
M: repository content is source-trust data; git runs with argument vectors, `core.hooksPath=/dev/null` and `protocol.ext.allow=never`, bare and shallow, no submodule recursion; there is no repository write capability; `repository.read` is ASK by default and refuses traversal. V: `internal/integrations` git ingestion and `ReadFile` tests. ☑

### T24 Malicious model output
M: model answers are decoded into typed Go structures (unknown fields ignored, wrong shapes discarded, numbers clamped); the JSON schema sent to providers is a request, not the validation; capability-shaped content in output is at most a claim; free text is rendered escaped; no model output builds SQL, paths, commands or URLs. V: `tests/security.TestDeceivedMindHasNoAuthority` (tool calls and policy in perception output) and `TestGarbageModelOutputIsInert` (non-JSON and wrong-shape output). ☑

### T25 Operator-control subversion
M: no drive or goal kind for self-preservation, replication or permission acquisition; no capability touches policy, approvals, snapshots or the mind's lifecycle; shutdown, pause and disable are authenticated operator routes with no path from cognition. V: `tests/security.TestNoSelfPreservationVocabulary` (drives, goal kinds and the capability registry) and `TestDeceivedMindHasNoAuthority`. ☑

## Residual risks

- A compromised host or database compromises everything.
- Approximate replay of LLM-driven steps cannot prove the model would behave identically.
- A mind may be deceived by what it reads; the system guarantees that deception cannot become authority, not that beliefs are true. See "Authority lives outside cognition" in `SECURITY.md`.
- Operators who set broad `ALLOW` policies accept the consequences; the UI warns on risky policies.
