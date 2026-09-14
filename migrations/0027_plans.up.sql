-- Plans and plan steps (PLAN §8.2 items 7–8, docs/planning.md). A plan is a
-- revisable DAG serving one intention; replans are new revisions that keep
-- the old plan as history.

CREATE TABLE plans (
  id                  uuid PRIMARY KEY,
  mind_id             uuid NOT NULL REFERENCES minds(id),
  intention_id        uuid NOT NULL REFERENCES intentions(id),
  revision            integer NOT NULL DEFAULT 1,
  supersedes          uuid REFERENCES plans(id),
  superseded_by       uuid,
  objective           text NOT NULL,
  origin              text NOT NULL CHECK (origin IN ('template','model','operator','dream','replan')),
  strategy            text NOT NULL DEFAULT '',
  status              text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','validated','waiting','executing','blocked','awaiting_approval','replanning','completed','failed','abandoned','superseded')),
  assumptions         jsonb NOT NULL DEFAULT '[]'::jsonb,
  completion_criteria text NOT NULL DEFAULT '',
  abort_conditions    jsonb NOT NULL DEFAULT '[]'::jsonb,
  alternatives        jsonb NOT NULL DEFAULT '[]'::jsonb,
  risk                text NOT NULL DEFAULT '',
  estimate            jsonb NOT NULL DEFAULT '{}'::jsonb,
  actual              jsonb NOT NULL DEFAULT '{}'::jsonb,
  budget              jsonb NOT NULL DEFAULT '{}'::jsonb,
  validation          jsonb NOT NULL DEFAULT '{}'::jsonb,
  require_approval    boolean NOT NULL DEFAULT false,
  max_replans         integer NOT NULL DEFAULT 3,
  llm_call            uuid,
  status_reason       text NOT NULL DEFAULT '',
  closed_at           timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX plans_mind_idx ON plans (mind_id, status, created_at DESC);
CREATE INDEX plans_intention_idx ON plans (intention_id, revision);

CREATE TABLE plan_steps (
  id               uuid PRIMARY KEY,
  plan_id          uuid NOT NULL REFERENCES plans(id),
  mind_id          uuid NOT NULL REFERENCES minds(id),
  key              text NOT NULL,
  position         integer NOT NULL,
  title            text NOT NULL,
  kind             text NOT NULL CHECK (kind IN ('capability','request_capability','verify','wait','monitor','internal')),
  depends_on       text[] NOT NULL DEFAULT '{}',
  capability       text NOT NULL DEFAULT '',
  target           text NOT NULL DEFAULT '',
  params           jsonb NOT NULL DEFAULT '{}'::jsonb,
  require_approval boolean NOT NULL DEFAULT false,
  expectation      jsonb,
  precondition     jsonb,
  spec             jsonb NOT NULL DEFAULT '{}'::jsonb,     -- kind-specific: request, check, wait, monitor, internal action
  status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','ready','executing','awaiting_approval','awaiting_grant','waiting','completed','failed','skipped','blocked','cancelled')),
  attempts         integer NOT NULL DEFAULT 0,
  max_attempts     integer NOT NULL DEFAULT 1,
  lease_until      timestamptz,
  next_run_at      timestamptz,
  approval_id      uuid,
  request_id       uuid,
  execution_id     uuid,
  prediction_id    uuid,
  intention_id     uuid,                                    -- subordinate intention this step formed
  result           jsonb,
  observation      jsonb,
  error            text NOT NULL DEFAULT '',
  status_reason    text NOT NULL DEFAULT '',
  started_at       timestamptz,
  finished_at      timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (plan_id, key)
);
CREATE INDEX plan_steps_due_idx ON plan_steps (status, next_run_at) WHERE status IN ('ready','executing','awaiting_approval','awaiting_grant','waiting');
CREATE INDEX plan_steps_plan_idx ON plan_steps (plan_id, position);

-- Provenance for recovery: which cognitive object asked for an execution
-- or approval, so an interrupted step can find what already happened.
ALTER TABLE tool_executions ADD COLUMN origin text NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN origin text NOT NULL DEFAULT '';
CREATE INDEX tool_executions_origin_idx ON tool_executions (origin) WHERE origin <> '';
CREATE INDEX approval_requests_origin_idx ON approval_requests (origin) WHERE origin <> '';

-- A mind may withdraw an approval request it no longer needs.
ALTER TABLE approval_requests DROP CONSTRAINT approval_requests_status_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_status_check CHECK (status IN ('pending','approved','denied','expired','executed','failed','withdrawn'));
