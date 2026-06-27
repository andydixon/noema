-- Goals, drives, affect and predictions.

CREATE TABLE goals (
  id           uuid PRIMARY KEY,
  mind_id      uuid NOT NULL REFERENCES minds(id),
  parent_id    uuid REFERENCES goals(id),
  title        text NOT NULL,
  description  text NOT NULL DEFAULT '',
  kind         text NOT NULL DEFAULT 'task' CHECK (kind IN ('task','investigation','question','maintenance')),
  status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active','blocked','paused','completed','abandoned')),
  priority     double precision NOT NULL DEFAULT 0.5 CHECK (priority >= 0 AND priority <= 1),
  progress     double precision NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 1),
  owner        text NOT NULL DEFAULT 'mind',   -- mind | operator
  blockers     jsonb NOT NULL DEFAULT '[]'::jsonb,
  origin_event uuid,
  subject      text NOT NULL DEFAULT '',
  due_at       timestamptz,
  last_progress_at timestamptz NOT NULL DEFAULT now(),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
CREATE INDEX goals_mind_status_idx ON goals (mind_id, status, priority DESC);
CREATE INDEX goals_parent_idx ON goals (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE drives (
  mind_id    uuid NOT NULL REFERENCES minds(id),
  name       text NOT NULL,
  value      double precision NOT NULL,
  min        double precision NOT NULL DEFAULT 0,
  max        double precision NOT NULL DEFAULT 1,
  baseline   double precision NOT NULL DEFAULT 0.5,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (mind_id, name)
);

CREATE TABLE affect_states (
  mind_id    uuid PRIMARY KEY REFERENCES minds(id),
  state      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE predictions (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  statement     text NOT NULL,
  subject       text NOT NULL DEFAULT '',
  confidence    double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  window_start  timestamptz NOT NULL DEFAULT now(),
  window_end    timestamptz NOT NULL,
  criteria      jsonb NOT NULL,   -- {"kind":"event_match","expect":"occurs|absent","terms":[...],"types":[...]} | {"kind":"belief","belief_id":..,"min_confidence":..} | {"kind":"operator"}
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open','confirmed','refuted','expired','withdrawn')),
  source        text NOT NULL DEFAULT '',
  origin_event  uuid,
  belief_id     uuid REFERENCES beliefs(id),
  resolved_at   timestamptz,
  resolution    jsonb,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX predictions_mind_status_idx ON predictions (mind_id, status, window_end);

-- Append-only outcomes for calibration.
CREATE TABLE prediction_results (
  id            uuid PRIMARY KEY,
  prediction_id uuid NOT NULL REFERENCES predictions(id),
  mind_id       uuid NOT NULL REFERENCES minds(id),
  outcome       text NOT NULL CHECK (outcome IN ('confirmed','refuted','expired','withdrawn')),
  confidence    double precision NOT NULL,
  error         double precision NOT NULL,   -- |outcome(1/0) - confidence| for confirmed/refuted
  evidence      jsonb NOT NULL DEFAULT '[]'::jsonb,
  event_id      uuid,
  resolved_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX prediction_results_mind_idx ON prediction_results (mind_id, resolved_at DESC);
CREATE TRIGGER prediction_results_append_only BEFORE UPDATE OR DELETE ON prediction_results
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
