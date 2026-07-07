This page is the reference description of how Noema is put together: the process model, the storage model, the cognitive cycle, and the boundaries that keep the deterministic core separate from language models and from the outside world. Detailed design decisions, with the alternatives that were rejected, are collected under [decisions](decisions).

## The shape of the system

```
              ┌───────────────────────────── noemad ─────────────────────────────┐
 stimuli ───▶ │ perception ─▶ cognitive events (append-only) ─▶ per-mind runtime │
 (chat,files, │                                          │                        │
  metrics,    │   cognitive processes (bounded pool)  ◀──┘                        │
  git, hooks) │   retrieval · association · curiosity · prediction · contradiction│
              │   goal monitor · metacognition · reflection · dreaming            │
              │                │ thought candidates                               │
              │                ▼                                                  │
              │   attention scoring (deterministic, inspectable) ─▶ workspace     │
              │                │ broadcast                                        │
              │                ▼                                                  │
              │   executive: beliefs · goals · predictions · narrative · response │
              │                │ actions                                          │
              │                ▼                                                  │
              │   capability + policy layer (ALLOW / ASK / DENY) ─▶ tools         │
              │                                                                   │
              │   LLM providers · embeddings · PostgreSQL (+pgvector) · metrics   │
              └───────────────────────────────────────────────────────────────────┘
                     ▲ HTTP API · SSE · web UI · noemactl
```

One process, `noemad`, hosts every mind, serves the HTTP API and the web interface, runs the scheduler and holds the connection pool. A second binary, `noemactl`, is a thin client for the API plus a few offline commands (migrations, first user) that talk to the database directly. The codebase is standard-library first: the only third-party dependencies are the PostgreSQL driver, the Argon2 implementation from `golang.org/x/crypto` and the Prometheus client.

{{include ARCHITECTURE.md}}
