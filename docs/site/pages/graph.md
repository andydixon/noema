The relationship graph is a mind's structured picture of the people, systems, projects, places and things it has encountered, with typed, evidenced connections between them. It is built from what the mind perceives and corrected by operators; it is never populated from a model's imagination without an event behind each edge.

## Entities

An entity has a kind (person, system, service, host, project, organisation, place, document, concept and a few more), a canonical name, aliases, attributes, mention counts and first and last seen times. Deterministic extraction finds candidate entities in observations and conversation (capitalised names, hostnames, repository names, quoted titles) and links each mention to the event it came from. Entities link to the memories, beliefs and events that mention them.

## Relationships

Edges carry a relation from one shared vocabulary, a confidence and evidence references to events, and are weighted by how often and how credibly they were observed. The vocabulary keeps association and causation apart. `co_occurs_with` and `temporally_precedes` record what was seen together or in order; co-mention within one observation writes nothing stronger. `possibly_caused_by` is a hypothesis: the association process proposes it for notable events close in time, always as a hypothesis belief with the alternatives "coincidence" and "a third factor caused both", and a prediction that the effect should recur. `caused_by` is reserved for established causes stated by an operator or recorded as a correction; nothing automatic writes it. `asserted_by` links a proposition to the person who put it forward. An explicit statement ("db-primary is the new name for server-3") still creates a strong edge of the kind it states.

## Corrections

Operators can **merge** two entities that turned out to be one (the merged entity becomes an alias, its edges and links move, and the merge is an event the mind remembers), or mark two as **distinct** so extraction does not merge them again. Both are audited.

## The explorer

The graph page renders a bounded neighbourhood around a chosen entity on a canvas with a force layout, with edge thickness for weight and colour for kind. It is deliberately capped in size; the detail page for an entity lists everything without the cap.

{{include docs/graph.md}}
