-- Scoped temporary grants and structured capability requests (PLAN §8.2 item 9).
-- Grants are operator decisions: expiring, optionally counted, restricted by
-- target, parameters and access. Requests are what a mind asks for; only an
-- operator route can decide them.

CREATE TABLE capability_grants (
  id                 uuid PRIMARY KEY,
  mind_id            uuid NOT NULL REFERENCES minds(id),
  capability         text NOT NULL,
  target_pattern     text NOT NULL DEFAULT '*',
  param_constraints  jsonb NOT NULL DEFAULT '{}'::jsonb,   -- parameter name → glob the value must match
  access             text NOT NULL DEFAULT 'read' CHECK (access IN ('read','write')),
  max_operations     integer CHECK (max_operations IS NULL OR max_operations > 0),
  used_operations    integer NOT NULL DEFAULT 0 CHECK (used_operations >= 0),
  expires_at         timestamptz NOT NULL,
  granted_by         text NOT NULL,
  request_id         uuid,
  note               text NOT NULL DEFAULT '',
  revoked_at         timestamptz,
  revoked_by         text NOT NULL DEFAULT '',
  expiry_notified_at timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  CHECK (max_operations IS NULL OR used_operations <= max_operations)
);
CREATE INDEX capability_grants_mind_idx ON capability_grants (mind_id, capability) WHERE revoked_at IS NULL;

CREATE TABLE capability_requests (
  id                uuid PRIMARY KEY,
  mind_id           uuid NOT NULL REFERENCES minds(id),
  capability        text NOT NULL,
  target_pattern    text NOT NULL DEFAULT '*',
  param_constraints jsonb NOT NULL DEFAULT '{}'::jsonb,
  access            text NOT NULL DEFAULT 'read' CHECK (access IN ('read','write')),
  duration_seconds  integer NOT NULL CHECK (duration_seconds > 0 AND duration_seconds <= 604800),
  max_operations    integer CHECK (max_operations IS NULL OR max_operations > 0),
  reason            text NOT NULL,
  evidence          jsonb NOT NULL DEFAULT '[]'::jsonb,
  risk              text NOT NULL DEFAULT '',
  mitigation        text NOT NULL DEFAULT '',
  fallback          text NOT NULL DEFAULT '',
  alternatives      jsonb NOT NULL DEFAULT '[]'::jsonb,
  history           jsonb NOT NULL DEFAULT '{}'::jsonb,    -- explicit past decisions on this capability, for the operator
  intention_id      uuid,
  plan_id           uuid,
  step_id           uuid,
  batch_key         text NOT NULL DEFAULT '',
  status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','granted','denied','expired','withdrawn')),
  decided_by        text NOT NULL DEFAULT '',
  decision_note     text NOT NULL DEFAULT '',
  decided_at        timestamptz,
  grant_id          uuid REFERENCES capability_grants(id),
  event_id          uuid,
  expires_at        timestamptz NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX capability_requests_status_idx ON capability_requests (status, created_at DESC);
CREATE INDEX capability_requests_mind_idx ON capability_requests (mind_id, created_at DESC);
