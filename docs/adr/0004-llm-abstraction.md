# ADR-0004: LLM as a replaceable cognitive service

Date: 2026-09-14  
Status: Accepted

## Context
Minds must survive provider changes; different tasks want different models; every call must be inspectable and replayable; secrets must not leak.

## Decision
`internal/llm` defines `LanguageModel` (`Complete(ctx, CognitiveRequest) (CognitiveResponse, error)`) and `Embedder`. Providers implement raw HTTP clients (OpenAI-compatible, Anthropic Messages, Ollama; Bedrock added in 0.2 behind the same interface with a standard-library SigV4 signer). Provider rows in the database hold endpoint, model, parameters and a *secret reference*. **Roles** map cognitive tasks to a primary model and ordered fallbacks. A `Recorder` wrapper writes every request/response (prompt name+version, rendered messages, parameters, seed, model, usage, latency, estimated cost) and enforces budgets; a `Replayer` serves recorded responses during replay. Prompts are versioned rows rendered with structured inputs; untrusted text is enclosed in explicit data blocks.

## Consequences
- Cognitive code depends only on roles; swapping vendors is configuration.
- Replay of LLM steps is exact when a recorded response exists and approximate otherwise, and is labelled as such.
- Budget enforcement and cost accounting live in one place.

## Alternatives considered
- Vendor SDKs: heavy dependency trees, inconsistent behaviour; raw HTTP is small and controllable.
- LangChain-style frameworks in Go: unnecessary abstraction.
