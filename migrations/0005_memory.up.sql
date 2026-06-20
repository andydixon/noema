-- Long-term memory: one table, four kinds, explicit epistemic status, decay
-- without deletion, text search, relationships, and optional pgvector
-- embeddings (ADR-0003).

CREATE TABLE memories (
  id                uuid PRIMARY KEY,
  mind_id           uuid NOT NULL REFERENCES minds(id),
  kind              text NOT NULL CHECK (kind IN ('episodic','semantic','procedural','autobiographical')),
  epistemic         text NOT NULL,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active','retracted','superseded','archived')),
  content           text NOT NULL,
  detail            jsonb NOT NULL DEFAULT '{}'::jsonb,
  source            text NOT NULL DEFAULT '',
  trust             text NOT NULL DEFAULT 'low',
  credibility       double precision NOT NULL DEFAULT 0.5,
  confidence        double precision NOT NULL DEFAULT 0.5 CHECK (confidence >= 0 AND confidence <= 1),
  importance        double precision NOT NULL DEFAULT 0.5 CHECK (importance >= 0 AND importance <= 1),
  salience          double precision NOT NULL DEFAULT 0.5,
  activation        double precision NOT NULL DEFAULT 1,
  valence           double precision,
  arousal           double precision,
  occurred_at       timestamptz NOT NULL DEFAULT now(),
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  retrieval_count   integer NOT NULL DEFAULT 0,
  last_retrieved_at timestamptz,
  origin_event      uuid,
  supersedes        uuid REFERENCES memories(id),
  search            tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);
CREATE INDEX memories_mind_kind_idx ON memories (mind_id, kind, occurred_at DESC);
CREATE INDEX memories_mind_status_idx ON memories (mind_id, status);
CREATE INDEX memories_search_idx ON memories USING gin (search);
CREATE INDEX memories_origin_idx ON memories (origin_event) WHERE origin_event IS NOT NULL;

CREATE TABLE memory_relationships (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  from_memory uuid NOT NULL REFERENCES memories(id),
  to_memory   uuid NOT NULL REFERENCES memories(id),
  relation    text NOT NULL,
  confidence  double precision NOT NULL DEFAULT 0.5,
  source      text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (from_memory, to_memory, relation)
);
CREATE INDEX memory_relationships_to_idx ON memory_relationships (to_memory);

-- Embeddings only when pgvector is present. The vector column is untyped so
-- several models with different dimensions can coexist; rows carry the model
-- name and dimension so representations are never mixed.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
    CREATE EXTENSION IF NOT EXISTS vector;
    CREATE TABLE memory_embeddings (
      memory_id  uuid NOT NULL REFERENCES memories(id),
      model      text NOT NULL,
      dims       integer NOT NULL,
      embedding  vector NOT NULL,
      created_at timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (memory_id, model)
    );
    CREATE INDEX memory_embeddings_model_idx ON memory_embeddings (model);
  END IF;
END $$;
