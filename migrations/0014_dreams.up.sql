-- Dreaming: replay, association and speculative hypotheses (spec §19).
CREATE TABLE dreams (
  id           uuid PRIMARY KEY,
  mind_id      uuid NOT NULL REFERENCES minds(id),
  status       text NOT NULL DEFAULT 'running' CHECK (status IN ('running','done','failed','rejected')),
  trigger      text NOT NULL DEFAULT 'schedule',
  started_at   timestamptz,
  finished_at  timestamptz,
  sampled      integer NOT NULL DEFAULT 0,
  associations integer NOT NULL DEFAULT 0,
  hypotheses   integer NOT NULL DEFAULT 0,
  llm_calls    integer NOT NULL DEFAULT 0,
  inputs       jsonb NOT NULL DEFAULT '[]'::jsonb,  -- sampled memory ids
  error        text NOT NULL DEFAULT '',
  created_by   text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dreams_mind_idx ON dreams (mind_id, created_at DESC);

CREATE TABLE dream_hypotheses (
  id           uuid PRIMARY KEY,
  dream_id     uuid NOT NULL REFERENCES dreams(id),
  mind_id      uuid NOT NULL REFERENCES minds(id),
  belief_id    uuid NOT NULL,
  memory_ids   uuid[] NOT NULL,
  proposition  text NOT NULL,
  confidence   double precision NOT NULL,
  basis        text NOT NULL DEFAULT '',
  wording      text NOT NULL DEFAULT 'deterministic',
  status       text NOT NULL DEFAULT 'proposed' CHECK (status IN ('proposed','promoted','rejected')),
  reviewed_by  text NOT NULL DEFAULT '',
  review_note  text NOT NULL DEFAULT '',
  reviewed_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dream_hypotheses_mind_idx ON dream_hypotheses (mind_id, status, created_at DESC);
