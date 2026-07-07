The relationship graph is a mind's structured picture of the people, systems, projects, places and things it has encountered, with typed, evidenced connections between them. It is built from what the mind perceives and corrected by operators; it is never populated from a model's imagination without an event behind each edge.

## Entities

An entity has a kind (person, system, service, host, project, organisation, place, document, concept and a few more), a canonical name, aliases, attributes, mention counts and first and last seen times. Deterministic extraction finds candidate entities in observations and conversation (capitalised names, hostnames, repository names, quoted titles) and links each mention to the event it came from. Entities link to the memories, beliefs and events that mention them.

## Relationships

Edges are typed (works_on, owns, runs_on, part_of, depends_on, talks_about, located_in and others), weighted by how often and how credibly they were observed, and each carries evidence references to events. Co-occurrence within one observation creates weak edges; repeated co-occurrence strengthens them; an explicit statement ("db-primary is the new name for server-3") creates a strong one.

## Corrections

Operators can **merge** two entities that turned out to be one (the merged entity becomes an alias, its edges and links move, and the merge is an event the mind remembers), or mark two as **distinct** so extraction does not merge them again. Both are audited.

## The explorer

The graph page renders a bounded neighbourhood around a chosen entity on a canvas with a force layout, with edge thickness for weight and colour for kind. It is deliberately capped in size; the detail page for an entity lists everything without the cap.

{{include docs/graph.md}}
