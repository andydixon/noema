-- Entities and typed relationships (spec §24). Entities are mind-scoped;
-- merges keep the loser row with merged_into so history never dangles.

CREATE TABLE entities (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  kind          text NOT NULL CHECK (kind IN ('person','system','project','document','concept','repository','organisation','place','other')),
  name          text NOT NULL,
  canonical     text NOT NULL,
  aliases       text[] NOT NULL DEFAULT '{}',
  description   text NOT NULL DEFAULT '',
  attributes    jsonb NOT NULL DEFAULT '{}'::jsonb,
  mention_count integer NOT NULL DEFAULT 0,
  first_seen    timestamptz NOT NULL DEFAULT now(),
  last_seen     timestamptz NOT NULL DEFAULT now(),
  merged_into   uuid REFERENCES entities(id),
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX entities_canonical_idx ON entities (mind_id, canonical) WHERE merged_into IS NULL;
CREATE INDEX entities_mind_kind_idx ON entities (mind_id, kind, mention_count DESC);

CREATE TABLE entity_relationships (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  from_entity uuid NOT NULL REFERENCES entities(id),
  to_entity   uuid NOT NULL REFERENCES entities(id),
  relation    text NOT NULL,
  confidence  double precision NOT NULL DEFAULT 0.3,
  source      text NOT NULL DEFAULT '',
  event_id    uuid,
  count       integer NOT NULL DEFAULT 1,
  first_seen  timestamptz NOT NULL DEFAULT now(),
  last_seen   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (from_entity, to_entity, relation)
);
CREATE INDEX entity_relationships_to_idx ON entity_relationships (to_entity);

-- Mentions: which memories, beliefs, events, goals and predictions involve an entity.
CREATE TABLE entity_links (
  id         uuid PRIMARY KEY,
  mind_id    uuid NOT NULL REFERENCES minds(id),
  entity_id  uuid NOT NULL REFERENCES entities(id),
  kind       text NOT NULL,
  ref_id     uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entity_id, kind, ref_id)
);
CREATE INDEX entity_links_ref_idx ON entity_links (kind, ref_id);
