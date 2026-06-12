# ADR-0003: Memory representation

Date: 2026-09-14  
Status: Accepted

## Context
Noema needs episodic, semantic, procedural and autobiographical memory, working memory, decay without deletion, several retrieval mechanisms, provenance and relationships, and must not collapse epistemic categories.

## Decision
- One `memories` table with a `kind` discriminator and shared columns (importance, salience, activation, retrieval stats, source, credibility, epistemic kind, timestamps) plus a typed `content` JSON document per kind. Go exposes distinct types; the shared table keeps relationships, search and retrieval uniform.
- `epistemic_kind` is a separate enum (observation, fact, memory, claim, hypothesis, belief, prediction, assumption, inference, opinion) present on memories, beliefs and evidence links.
- Full-text search via a generated `tsvector` column. Embeddings live in `memory_embeddings(memory_id, model, dims, embedding)` created only if pgvector is available, so representations from different models are never mixed.
- Relationships live in `memory_relationships` and the general `entity_relationships` graph with confidence, provenance and timestamps.
- Decay is a scheduled deterministic update of `activation`; rows are never deleted by decay.
- Working memory is an in-process bounded structure persisted in `workspace_states` after each cycle.

## Consequences
- Retrieval is a deterministic fusion of text, vector, recency, activation, importance and graph distance with per-component scores recorded.
- Loss of the embedding service degrades retrieval quality but never availability.

## Alternatives considered
- Table per memory kind: cleaner typing but duplicated retrieval/relationship code and cross-kind queries become unions.
- Vector-only store: rejected by requirement; loses provenance and structure.
