# Memory architecture

Noema keeps four long-term memory systems in one table (`memories`) distinguished by `kind`, plus an in-process working memory. Every record carries an explicit **epistemic kind** (observation, fact, memory, claim, hypothesis, belief, prediction, assumption, inference, opinion) so the system never confuses what it saw with what it inferred.

| Kind | What it holds | Created by |
|---|---|---|
| episodic | things that happened, with time, participants, channel, source | the `attend` handler when an observation wins attention; conversation |
| semantic | propositions and general knowledge | operators, belief formation, consolidation |
| procedural | how to do things (steps, preconditions, outcomes) | reflection, operators |
| autobiographical | the mind's own significant history | operator actions on the mind, reflection |

## Fields that matter

- `confidence` how likely the content is true; `importance` how much it matters; `salience` how strongly it presented itself.
- `trust` and `credibility` come from the channel the information arrived through (operator > user ≈ system > tool > source > low). Credibility weighs retrieval and, later, belief confidence.
- `activation` is accessibility. It rises on retrieval and decays over time (half-life 14 days, slowed by importance). **Decay never deletes**: a memory with activation 0.01 is still there, still linked, still readable.
- `status` is active, retracted (an operator said it is inaccurate), superseded (replaced by a newer version) or archived. Non-active memories stay for provenance and are excluded from normal recall.
- `origin_event` links to the event that produced the memory; `memory_created` events link back. Relationships (`related_to`, `supports`, `contradicts`, `derived_from`, …) carry confidence and source.

## Retrieval

`memory.Store.Retrieve` fuses independent signals, each recorded on the result:

| Component | Weight | Source |
|---|---|---|
| text | 0.40 (0.65 when vectors are unavailable) | PostgreSQL full-text rank; any-term matching by default, websearch syntax when strict |
| vector | 0.25 | pgvector cosine similarity for the configured embedding model |
| recency | 0.10 | exp(−age/30 days) |
| activation | 0.10 | current accessibility |
| importance | 0.10 | stored importance |
| credibility | 0.05 | source credibility |

The `retrieve` process runs this for every new observation and proposes recalled memories as thought candidates. Recall therefore competes for attention like everything else; admitted recalls are reinforced and produce `memory_retrieved` events containing the component scores.

## Embeddings

`memory_embeddings` exists only when pgvector is available. Rows carry the model name and dimensions, so switching embedding models never mixes vector spaces; a background job embeds memories that lack a vector for the current model. Without an embedder, retrieval is text plus graph plus recency and the system stays fully functional.

## Working memory

Working memory is the small, decaying set of items the mind is actively holding (default nine). It is part of the workspace snapshot, persists across restarts, and feeds the context-relevance feature of attention scoring. See `docs/adr/0001-global-workspace.md`.

## Operator corrections

"This memory is inaccurate" sets `status=retracted`, writes an `operator_action` event with the reason and an audit entry. The memory remains visible with a retracted badge; nothing is rewritten.
