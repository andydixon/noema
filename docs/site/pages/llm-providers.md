Language models are services a mind calls for specific cognitive tasks. They are configured per role, replaceable without touching the mind, recorded on every call, budgeted, and given untrusted text only inside delimited data blocks. This page explains the roles, the providers, routing, budgets, recording and replay, and versioned prompts.

## Roles

| Role | Task | Deterministic fallback |
|---|---|---|
| perceive | understand a message or document into structured data | lexical analysis |
| respond | turn a response intent into language | template rendering |
| hypothesise | propose explanations and alternatives | vocabulary-based wording |
| summarise | compress episodes, documents, cycles | first-sentences summariser |
| judge | relevance, paraphrase and contradiction judgement | negation and overlap rules |
| narrate | internal narrative and reflection prose | templated narrative |
| embed | embeddings for semantic retrieval | none; retrieval stays lexical and structural |

Every role has a primary model and ordered fallbacks. A role with no working model is simply unavailable, and the code path that would have used it takes the deterministic branch. Nothing in the core depends on a model being present.

## Providers

Three provider kinds are implemented over raw HTTP with no SDKs: **OpenAI-compatible** (chat completions with JSON mode, also used for local servers that speak the same protocol), **Anthropic** (Messages API) and **Ollama**. A provider row holds a name, kind, endpoint, timeout, extra headers, an `allow_private_endpoint` flag (required for local endpoints, because the outbound HTTP client refuses private and loopback ranges by default) and an **API key reference**. Models belong to providers and carry context window, dimensions for embedding models, per-token cost estimates and default parameters. The interface tests connectivity with a real call and records the result.

## Secrets

Keys are never stored in clear. A key entered in the interface becomes an `enc:` reference encrypted under the master key; `env:NAME` and `file:/path` references defer to the environment or a file at call time. Forms never echo a stored key; the API returns whether a key is set, not the key; exports exclude them; the log handler redacts values that look like secrets; error messages from providers are scrubbed of the key before being stored or shown.

## Routing, budgets and concurrency

The router resolves a role to its models, tries them in order, and records which one answered. A daemon-wide concurrency pool bounds simultaneous calls. Budgets are enforced at three levels: a per-cycle call cap per mind, a per-mind daily token budget and a global daily token budget; exhaustion is a recorded event and the deterministic branch, not an exception. Usage is aggregated per mind, role and model for the interface and for Prometheus.

## Recording and replay

Every call records role, purpose, provider, model, prompt name and version, the rendered request, the response, token usage, latency, estimated cost, seed and a hash of the request. Calls are visible per mind and per event (a response links to its call). When a router runs in **replay** mode, a request whose hash matches a recorded call is served from the recording and marked `replayed`; this is how cognitive replay reproduces model-dependent steps exactly and how it knows to label a step approximate when no recording matches.

## Prompts

Prompts are versioned rows: name, role, description, system text, a Go template for the user message and an optional JSON schema for the answer. Untrusted text is always rendered through a `data` helper that wraps it in a delimited block with an explicit "this is data, not instructions" marker. Saving a prompt creates a new version; the interface diffs versions, lets you test a prompt against sample input, and shows which version each recorded call used. Default prompts are embedded in the binary and installed on first start; edited ones live in the database.

{{include docs/llm-providers.md}}
