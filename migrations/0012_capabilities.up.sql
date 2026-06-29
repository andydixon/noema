-- Capability policies, approval requests, session grants and tool executions (ADR-0005).

CREATE TABLE policies (
  id             uuid PRIMARY KEY,
  mind_id        uuid REFERENCES minds(id),   -- NULL = global
  capability     text NOT NULL DEFAULT '*',
  target_pattern text NOT NULL DEFAULT '*',
  action_kind    text NOT NULL DEFAULT '*',
  effect         text NOT NULL CHECK (effect IN ('allow','ask','deny')),
  hours_from     smallint,                    -- optional daily window (local hours), NULL = always
  hours_to       smallint,
  reason         text NOT NULL DEFAULT '',
  created_by     text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX policies_mind_idx ON policies (mind_id, capability);

CREATE TABLE approval_requests (
  id              uuid PRIMARY KEY,
  mind_id         uuid NOT NULL REFERENCES minds(id),
  capability      text NOT NULL,
  target          text NOT NULL DEFAULT '',
  params          jsonb NOT NULL DEFAULT '{}'::jsonb,
  request_hash    text NOT NULL,
  reason          text NOT NULL DEFAULT '',
  risk            text NOT NULL,
  evidence        jsonb NOT NULL DEFAULT '[]'::jsonb,
  status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','approved','denied','expired','executed','failed')),
  decision_by     text NOT NULL DEFAULT '',
  decision_note   text NOT NULL DEFAULT '',
  decided_at      timestamptz,
  session_grant   boolean NOT NULL DEFAULT false,
  modified_params jsonb,
  result          jsonb,
  error           text NOT NULL DEFAULT '',
  event_id        uuid,
  correlation_id  uuid,
  expires_at      timestamptz NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_requests_status_idx ON approval_requests (status, created_at DESC);
CREATE INDEX approval_requests_mind_idx ON approval_requests (mind_id, created_at DESC);

-- Append-only record of every decision.
CREATE TABLE approval_decisions (
  id         uuid PRIMARY KEY,
  request_id uuid NOT NULL REFERENCES approval_requests(id),
  decision   text NOT NULL,
  decided_by text NOT NULL,
  note       text NOT NULL DEFAULT '',
  details    jsonb NOT NULL DEFAULT '{}'::jsonb,
  decided_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER approval_decisions_append_only BEFORE UPDATE OR DELETE ON approval_decisions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

CREATE TABLE session_grants (
  id             uuid PRIMARY KEY,
  mind_id        uuid NOT NULL REFERENCES minds(id),
  capability     text NOT NULL,
  target_pattern text NOT NULL DEFAULT '*',
  granted_by     text NOT NULL,
  expires_at     timestamptz NOT NULL,
  revoked_at     timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX session_grants_mind_idx ON session_grants (mind_id, capability);

CREATE TABLE tool_executions (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  capability  text NOT NULL,
  target      text NOT NULL DEFAULT '',
  params      jsonb NOT NULL DEFAULT '{}'::jsonb,
  result      jsonb,
  error       text NOT NULL DEFAULT '',
  duration_ms integer NOT NULL DEFAULT 0,
  approval_id uuid REFERENCES approval_requests(id),
  event_id    uuid,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tool_executions_mind_idx ON tool_executions (mind_id, created_at DESC);
