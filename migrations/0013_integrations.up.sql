-- Integrations: configured external sources, their runs and ingested documents.

CREATE TABLE integrations (
  id             uuid PRIMARY KEY,
  mind_id        uuid NOT NULL REFERENCES minds(id),
  kind           text NOT NULL CHECK (kind IN ('research_folder','git','prometheus','webhook')),
  name           text NOT NULL,
  config         jsonb NOT NULL DEFAULT '{}'::jsonb,   -- never contains secrets
  secret_ref     text NOT NULL DEFAULT '',             -- env:/file:/enc: reference
  trust          text NOT NULL DEFAULT 'source',
  enabled        boolean NOT NULL DEFAULT true,
  status         text NOT NULL DEFAULT 'new' CHECK (status IN ('new','ok','stale','error','disabled')),
  state          jsonb NOT NULL DEFAULT '{}'::jsonb,   -- poller bookkeeping (last commit, last values)
  last_run_at    timestamptz,
  last_ok_at     timestamptz,
  last_error     text NOT NULL DEFAULT '',
  ingested_count integer NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX integrations_mind_name_idx ON integrations (mind_id, lower(name));

CREATE TABLE integration_runs (
  id             uuid PRIMARY KEY,
  integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  items          integer NOT NULL DEFAULT 0,
  observations   integer NOT NULL DEFAULT 0,
  errors         integer NOT NULL DEFAULT 0,
  notes          jsonb NOT NULL DEFAULT '[]'::jsonb,
  error          text NOT NULL DEFAULT ''
);
CREATE INDEX integration_runs_idx ON integration_runs (integration_id, started_at DESC);

CREATE TABLE ingested_documents (
  id             uuid PRIMARY KEY,
  mind_id        uuid NOT NULL REFERENCES minds(id),
  integration_id uuid NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
  fingerprint    text NOT NULL,
  reference      text NOT NULL,
  title          text NOT NULL DEFAULT '',
  content_type   text NOT NULL DEFAULT '',
  size_bytes     bigint NOT NULL DEFAULT 0,
  extracted_chars integer NOT NULL DEFAULT 0,
  excerpt        text NOT NULL DEFAULT '',
  status         text NOT NULL CHECK (status IN ('ingested','duplicate','rejected','failed')),
  error          text NOT NULL DEFAULT '',
  event_id       uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (mind_id, fingerprint)
);
CREATE INDEX ingested_documents_integration_idx ON ingested_documents (integration_id, created_at DESC);
