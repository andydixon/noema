-- Consolidation jobs, reflections, narratives and notifications.

CREATE TABLE consolidation_jobs (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  status      text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','failed','rejected')),
  trigger     text NOT NULL DEFAULT 'schedule',   -- schedule | operator
  started_at  timestamptz,
  finished_at timestamptz,
  clusters    integer NOT NULL DEFAULT 0,
  produced    integer NOT NULL DEFAULT 0,
  inputs      jsonb NOT NULL DEFAULT '[]'::jsonb, -- episode ids considered
  outputs     jsonb NOT NULL DEFAULT '[]'::jsonb, -- {memory_id, belief_id, proposition, confidence, episodes}
  error       text NOT NULL DEFAULT '',
  created_by  text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX consolidation_jobs_mind_idx ON consolidation_jobs (mind_id, created_at DESC);

CREATE TABLE reflections (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  period_start timestamptz NOT NULL,
  period_end   timestamptz NOT NULL,
  findings    jsonb NOT NULL,   -- structured review
  prose       text NOT NULL DEFAULT '',
  event_id    uuid,
  llm_call_id uuid,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reflections_mind_idx ON reflections (mind_id, created_at DESC);

CREATE TABLE narratives (
  mind_id    uuid PRIMARY KEY REFERENCES minds(id),
  text       text NOT NULL,
  structured jsonb NOT NULL DEFAULT '{}'::jsonb,
  event_id   uuid,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  level         text NOT NULL CHECK (level IN ('info','warn','alert')),
  title         text NOT NULL,
  body          text NOT NULL DEFAULT '',
  rule          text NOT NULL,
  reason        jsonb NOT NULL DEFAULT '{}'::jsonb,  -- thresholds and values that fired the rule
  aggregate_key text NOT NULL DEFAULT '',
  count         integer NOT NULL DEFAULT 1,
  status        text NOT NULL DEFAULT 'new' CHECK (status IN ('new','read','suppressed')),
  source_event  uuid,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  read_at       timestamptz
);
CREATE INDEX notifications_mind_status_idx ON notifications (mind_id, status, created_at DESC);
CREATE INDEX notifications_aggregate_idx ON notifications (mind_id, aggregate_key, created_at DESC);
