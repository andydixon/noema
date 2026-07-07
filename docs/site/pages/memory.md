Memory is where a mind keeps what it has experienced and learned. Noema separates *what happened* from *what is generally true* from *how to do things* from *what happened to me*, keeps the provenance of every item, and makes retrieval a scored, inspectable operation rather than a black box.

## Encoding

Memories are created by the executive when a thought is important enough to keep (attention-gated encoding), by consolidation when episodes generalise, by operators through the interface or API, and autobiographically when the mind itself is changed. Every memory records its kind, epistemic kind, content, source and trust, credibility, confidence, importance, salience, the time it refers to, the event that created it and any memory it supersedes. Nothing is ever overwritten: corrections retract or supersede.

## Retrieval

Retrieval fuses several signals into one score and keeps the parts visible:

| Component | Source | Notes |
|---|---|---|
| lexical | PostgreSQL full-text search over content, any-term matching, ranked | always available |
| semantic | cosine similarity over embeddings in pgvector | only when the extension and an embedding model are present |
| recency | time since the memory occurred | half-life scaled by importance |
| importance | stored importance | set at encoding, raised by reinforcement |
| activation | decaying activation, boosted on each retrieval | models spreading availability |
| relations | typed links to memories already in the workspace | structural relevance |

A retrieval is a `memory_retrieved` event that carries the breakdown, and the memory's activation and retrieval count are reinforced. The interface shows the breakdown on every recall, so "why did it remember that" is always answerable.

## Decay

A scheduled job lowers activation for memories not retrieved recently, with importance slowing the decay. Decay never deletes: a memory with activation near zero is still there, still searchable and still cited by the beliefs that rest on it; it simply competes less strongly for attention.

## Relationships

Memories link to each other with typed, weighted relationships. Consolidation writes `derived_from` edges from a generalisation to its episodes; dreaming writes `similar_to` edges; perception writes `caused_by` and `contradicts` when it can justify them; operators can add any. Edges are shown on the memory page and used as a retrieval signal.

## Consolidation

Consolidation groups recent episodes that share distinctive terms into clusters (deterministically), writes one semantic memory and one evidenced belief per cluster, links the generalisation back to every episode, and never touches the originals. A model may propose better wording within a per-run budget; the cluster, the evidence and the confidence come from code. Confidence requires diversity of sources: ten repetitions from one source do not make something true. Operators review generalisations and can reject them, which retracts the memory and the belief while leaving the episodes alone.

## Operator corrections

An operator can retract a memory, mark it inaccurate, or supersede it with a corrected one. The correction is recorded, the beliefs whose evidence cited the memory are re-evaluated, and a "do not infer this again" rule can be attached so that consolidation and association do not quietly rebuild the same conclusion.

{{include docs/memory.md}}
