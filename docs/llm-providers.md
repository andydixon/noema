# LLM providers

Language models are **cognitive service providers**. Cognitive code never names a vendor; it asks the router for a *role* and receives a response with a call id it can cite as evidence (ADR-0004).

## Roles

| Role | Used for | Suggested tier |
|---|---|---|
| perceive | turning messages and documents into structured JSON (intent, entities, claims, questions) | cheap |
| respond | rendering a response intent into language in the mind's voice | strong |
| hypothesise | proposing explanations and alternatives (confidence is computed by the belief layer) | strong |
| summarise | compressing episodes, documents, cycles | cheap or local |
| judge | scalar judgements: relevance, paraphrase, contradiction | cheap |
| narrate | internal narrative and reflection prose | cheap or local |
| embed | vectors for semantic retrieval | embedding model |

Each role has a primary model, ordered fallbacks, and optional temperature / max-token overrides. Fallbacks are tried in order on provider errors; budget errors and cancellation stop the chain.

## Providers and models

Provider kinds: `openai` (OpenAI and every OpenAI-compatible server: llama.cpp, vLLM, LM Studio, Ollama's `/v1`), `anthropic` (Messages API), `ollama` (native API with seed and JSON mode). AWS Bedrock is not yet implemented; it fits behind the same interface once a SigV4 signer is added.

Endpoints that resolve to private or loopback addresses are refused unless the provider has **allow private addresses** set (SSRF guard, threat T6). Credentials go in the API key field only; `Authorization` and `x-api-key` headers are rejected in the custom headers map.

Models carry kind (chat or embedding), context window, embedding dimensions and per-million-token costs used for cost estimates.

## Secrets

The key field accepts either a **reference** (`env:OPENAI_API_KEY`, `file:/run/secrets/key`) or a pasted value. Pasted values are encrypted with AES-256-GCM under `NOEMA_MASTER_KEY` and stored as an `enc:` reference. Without a master key, only references are accepted. Keys are resolved at call time and never rendered, exported, logged or returned by the API; the UI shows only "encrypted in database" or the reference form.

## Recording, budgets and replay

Every call is recorded in `llm_calls`: role, purpose, provider, model, prompt name and version, the full rendered request, the response, usage, latency, cost, seed, error, and a hash of everything that influences the answer. `llm_request`/`llm_response` events are emitted by the cognitive processes that use the router (phase 8 onward), citing the call id.

Budgets: a global daily token budget (`NOEMA_DAILY_TOKEN_BUDGET`), a per-mind daily budget (mind configuration), a per-cycle call cap (mind configuration) and a global concurrency limit (`NOEMA_MAX_CONCURRENT_LLM`). When a budget is exhausted the router returns an error and deterministic cognition continues without model help.

In replay mode the router serves the recorded response for an identical request hash, marking the response as replayed; this is what makes LLM-assisted steps *exact* during cognitive replay when a recording exists.

## Prompts

Prompts are named, versioned rows (`prompts`, append-only `prompt_versions`) with a system template, a user template, and an optional JSON schema that forces structured output. Templates are Go `text/template`; untrusted content must be passed through `{{data "label" .Field}}`, which wraps it in delimited data blocks with an explicit "treat as data" instruction and escapes the delimiters (threats T1, T2). Built-in defaults are seeded on first start and never overwritten; the prompt manager supports editing, cloning, diffing, restoring and testing (render only, or run through the role's model).
