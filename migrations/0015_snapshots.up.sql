-- Snapshots, forks, replays and experiments (spec §41, §42, §43, §57).
CREATE TABLE snapshots (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  name        text NOT NULL,
  note        text NOT NULL DEFAULT '',
  event_seq   bigint NOT NULL,            -- history position at capture
  checksum    text NOT NULL,              -- sha256 of the canonical archive
  size_bytes  integer NOT NULL,
  counts      jsonb NOT NULL DEFAULT '{}'::jsonb,
  archive     jsonb NOT NULL,
  created_by  text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX snapshots_mind_idx ON snapshots (mind_id, created_at DESC);

CREATE TABLE replays (
  id             uuid PRIMARY KEY,
  mind_id        uuid NOT NULL REFERENCES minds(id),      -- source of the history
  snapshot_id    uuid NOT NULL REFERENCES snapshots(id),
  replay_mind_id uuid REFERENCES minds(id),               -- sandbox mind the replay ran in
  from_seq       bigint NOT NULL,
  to_seq         bigint NOT NULL,
  status         text NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed')),
  summary        jsonb NOT NULL DEFAULT '{}'::jsonb,       -- {steps, exact, approximate, diverged, llm_live, llm_replayed}
  steps          jsonb NOT NULL DEFAULT '[]'::jsonb,
  error          text NOT NULL DEFAULT '',
  created_by     text NOT NULL DEFAULT '',
  created_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz
);
CREATE INDEX replays_mind_idx ON replays (mind_id, created_at DESC);

CREATE TABLE experiments (
  id           uuid PRIMARY KEY,
  name         text NOT NULL,
  hypothesis   text NOT NULL DEFAULT '',
  snapshot_id  uuid NOT NULL REFERENCES snapshots(id),
  mind_id      uuid REFERENCES minds(id),                 -- subject mind created by the run
  definition   jsonb NOT NULL,                            -- config/personality overrides, stimuli, expected, seed, duration
  status       text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','running','done','failed')),
  results      jsonb NOT NULL DEFAULT '{}'::jsonb,
  error        text NOT NULL DEFAULT '',
  created_by   text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now(),
  started_at   timestamptz,
  finished_at  timestamptz
);
CREATE INDEX experiments_created_idx ON experiments (created_at DESC);
