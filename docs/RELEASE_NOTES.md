# Noema 0.2.0

Second release: every phase of the plan is implemented, tested and documented, and the epistemic honesty review pass is complete.

## What you get

- A daemon (`noemad`) hosting many minds, each with identity, personality, configuration, working memory, workspace, memories, beliefs with evidence arithmetic, goals, drives, affect, predictions, self-model, narrative, notifications and an append-only cognitive history.
- Deterministic attention and executive logic; language models only as replaceable services (OpenAI-compatible, Anthropic, Ollama, Amazon Bedrock) with recorded, replayable calls and encrypted secret references.
- A capability layer that is the only way a mind can act: ALLOW / ASK / DENY policies, approvals bound to exact requests, locked capabilities, tool output treated as data.
- Integrations: research folder, git mirrors, Prometheus/VictoriaMetrics, signed webhooks.
- Background cognition: consolidation, reflection, metacognition, dreaming (opt-in, cannot act).
- Study tools: snapshots, forks sharing history by reference, comparison, export/import, cognitive replay (exact / approximate / diverged), experiments with exportable results.
- A complete web UI, a CLI (`noemactl`), an OpenAPI document, Prometheus metrics, audit log, roles, sessions, API tokens and TOTP.

## Verification

`go vet`, `go test -race ./...`, staticcheck, gosec (G104 excluded) and govulncheck are clean with Go 1.26.8.

## Since 0.1.0

The review pass recorded in PLAN.md §7 and summarised in [epistemics.md](epistemics.md), plus the Amazon Bedrock provider, are the 0.2.0 section of the changelog. Its remaining limitations are listed at the end of that document.

## Known limits

- Replays diverge honestly when retrieval scoring depends on wall-clock recency; a virtual clock for replay is future work.
- No screenshots yet; the build environment has no browser.
- Model comparisons in experiments are made by changing role routing between runs; the routing in force is recorded with each result.
- Embeddings need pgvector; without it retrieval is lexical and structural only.
