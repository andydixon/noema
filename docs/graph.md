# Relationship graph

Each mind keeps its own entity graph (`entities`, `entity_relationships`, `entity_links`), mind-scoped like everything else.

## Entities

Kinds: person, system, project, document, concept, repository, organisation, place, other. Names are canonicalised (lower-cased, trimmed) for matching; aliases accumulate through merges. Every mention bumps `mention_count` and `last_seen`.

Extraction is deterministic and conservative: hostnames (`atlas.local`), hyphenated identifiers (`server-09`, `web-api-prod`), letter-digit names and capitalised words become entities; plain lower-case nouns do not. The `perceive` prompt may add entities with kinds when a model is configured. Operators can correct the kind and description of any entity.

## Relationships

Edges carry a relation from the shared vocabulary (`related_to`, `discussed_with`, `caused_by`, `supports`, `contradicts`, `depends_on`, `belongs_to`, `observed_during`, `derived_from`, `similar_to`, `follows`), a confidence, a source, a count of reinforcements and the event that produced them. Co-mention in one message yields `discussed_with` (speaker → entity) and `related_to` (entity ↔ entity) at low confidence; repeats raise the count. Operators can add typed edges with higher confidence.

`entity_links` record which memories, beliefs, events, goals and predictions involve an entity, so the entity page answers "where has this come up".

## Corrections

- **These are the same**: `Merge` re-points edges and links to the survivor, adds the loser's name as an alias, and keeps the loser row with `merged_into` so old references still resolve. An `operator_action` event and an `entities_same` correction are recorded.
- **These are different**: an `entities_different` correction and event are recorded; it documents the operator's judgement and is visible to later extraction.

## Explorer

`/minds/{mind}/graph` renders a bounded subgraph (node cap, kind filter, optional focus entity with its neighbourhood) on a canvas with a small force-directed layout written in plain JavaScript. Clicking a node loads its panel (edges, links, corrections); double-clicking focuses on it. The API endpoint `GET /api/v1/minds/{mind}/graph` returns the same nodes and edges as JSON.
