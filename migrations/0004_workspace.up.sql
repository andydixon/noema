-- Latest workspace / working-memory snapshot per mind. History lives in
-- workspace_broadcast events; this row only makes restarts seamless.
CREATE TABLE workspace_states (
  mind_id    uuid PRIMARY KEY REFERENCES minds(id),
  state      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
