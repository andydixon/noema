-- Volition (PLAN §8, docs/volition.md): wants, want conflicts, commitments,
-- intentions and the append-only ledger of volitional decisions.

CREATE TABLE wants (
  id                 uuid PRIMARY KEY,
  mind_id            uuid NOT NULL REFERENCES minds(id),
  subject            text NOT NULL,                 -- stable key; identical wants reinforce instead of duplicating
  description        text NOT NULL,
  origin_type        text NOT NULL,
  origin_event       uuid,
  origin_ref         text NOT NULL DEFAULT '',
  strength           double precision NOT NULL CHECK (strength >= 0 AND strength <= 1),
  valence            text NOT NULL DEFAULT 'approach' CHECK (valence IN ('approach','avoid')),
  urgency            double precision NOT NULL DEFAULT 0 CHECK (urgency >= 0 AND urgency <= 1),
  status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active','satisfied','expired','abandoned','superseded')),
  drives             text[] NOT NULL DEFAULT '{}',
  value_keys         text[] NOT NULL DEFAULT '{}',
  goal_id            uuid,
  commitment_id      uuid,
  proposal           jsonb,                          -- optional means: {kind, capability, target, params}
  terms              jsonb NOT NULL DEFAULT '{}'::jsonb,  -- how strength was computed
  reinforcements     integer NOT NULL DEFAULT 0,
  suppressions       integer NOT NULL DEFAULT 0,
  last_reinforced_at timestamptz NOT NULL DEFAULT now(),
  resolution         text NOT NULL DEFAULT '',
  resolved_at        timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX wants_active_subject_idx ON wants (mind_id, subject) WHERE status = 'active';
CREATE INDEX wants_mind_idx ON wants (mind_id, status, created_at DESC);

CREATE TABLE want_conflicts (
  id             uuid PRIMARY KEY,
  mind_id        uuid NOT NULL REFERENCES minds(id),
  want_a         uuid NOT NULL REFERENCES wants(id),
  want_b         uuid NOT NULL REFERENCES wants(id),
  kind           text NOT NULL,                     -- value | priority | commitment | exclusive
  status         text NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dissolved')),
  preferred      uuid,
  resolution     text NOT NULL DEFAULT '',          -- preferred | preferred_with_constraint | deferred | dissolved
  rationale      jsonb NOT NULL DEFAULT '{}'::jsonb,
  detected_event uuid,
  resolved_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX want_conflicts_open_pair_idx ON want_conflicts (mind_id, least(want_a, want_b), greatest(want_a, want_b)) WHERE status = 'open';

CREATE TABLE commitments (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  subject       text NOT NULL,
  description   text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('promise','operator_instruction','responsibility','value_rule','relationship','prior_decision','policy','scheduled')),
  polarity      text NOT NULL CHECK (polarity IN ('do','refrain')),
  matcher       jsonb NOT NULL DEFAULT '{}'::jsonb, -- {capabilities:[glob], targets:[glob], access, min_risk}
  unless        text NOT NULL DEFAULT '' CHECK (unless IN ('','approval')),
  beneficiary   text NOT NULL DEFAULT '',
  origin_type   text NOT NULL DEFAULT 'operator',
  origin_event  uuid,
  origin_ref    text NOT NULL DEFAULT '',
  strength      double precision NOT NULL DEFAULT 0.8 CHECK (strength >= 0 AND strength <= 1),
  priority      double precision NOT NULL DEFAULT 0.5 CHECK (priority >= 0 AND priority <= 1),
  conditions    text NOT NULL DEFAULT '',
  due_at        timestamptz,
  expires_at    timestamptz,
  revocable     boolean NOT NULL DEFAULT true,
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','fulfilled','broken','revoked','expired','superseded')),
  superseded_by uuid,
  goal_id       uuid,
  intention_id  uuid,
  evidence      jsonb NOT NULL DEFAULT '[]'::jsonb,
  due_notified_at timestamptz,
  created_by    text NOT NULL DEFAULT '',
  status_reason text NOT NULL DEFAULT '',
  closed_at     timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX commitments_mind_idx ON commitments (mind_id, status, due_at);

CREATE TABLE intentions (
  id                    uuid PRIMARY KEY,
  mind_id               uuid NOT NULL REFERENCES minds(id),
  parent_id             uuid REFERENCES intentions(id),
  description           text NOT NULL,
  desired_outcome       text NOT NULL DEFAULT '',
  kind                  text NOT NULL CHECK (kind IN ('act','investigate','request_approval','request_capability','fulfil_commitment','pursue','respond')),
  source_wants          uuid[] NOT NULL DEFAULT '{}',
  goal_id               uuid,
  commitment_id         uuid,
  belief_ids            uuid[] NOT NULL DEFAULT '{}',
  expected_result       text NOT NULL DEFAULT '',
  confidence            double precision NOT NULL DEFAULT 0.5 CHECK (confidence >= 0 AND confidence <= 1),
  priority              double precision NOT NULL DEFAULT 0.5 CHECK (priority >= 0 AND priority <= 1),
  status                text NOT NULL DEFAULT 'active' CHECK (status IN ('proposed','active','suspended','blocked','abandoned','fulfilled','superseded')),
  blocked_kind          text NOT NULL DEFAULT '',
  blocked_reason        text NOT NULL DEFAULT '',
  required_capabilities text[] NOT NULL DEFAULT '{}',
  require_approval      boolean NOT NULL DEFAULT false,
  proposal              jsonb,
  plan_id               uuid,
  superseded_by         uuid,
  formed_event          uuid,
  last_event            uuid,
  formation             jsonb NOT NULL DEFAULT '{}'::jsonb,
  reconsiderations      integer NOT NULL DEFAULT 0,
  status_reason         text NOT NULL DEFAULT '',
  closed_at             timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX intentions_mind_idx ON intentions (mind_id, status, priority DESC);

-- Append-only: why each want did or did not become an intention, how each
-- conflict was resolved, why each intention changed.
CREATE TABLE volition_decisions (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  kind          text NOT NULL,   -- formation | non_formation | conflict_resolution | reconsideration | suppression | commitment_check
  outcome       text NOT NULL,
  reason        text NOT NULL DEFAULT '',
  rationale     text NOT NULL DEFAULT '',
  want_id       uuid,
  intention_id  uuid,
  commitment_id uuid,
  conflict_id   uuid,
  terms         jsonb NOT NULL DEFAULT '{}'::jsonb,
  inputs        jsonb NOT NULL DEFAULT '{}'::jsonb,
  event_id      uuid,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX volition_decisions_mind_idx ON volition_decisions (mind_id, created_at DESC);
CREATE INDEX volition_decisions_want_idx ON volition_decisions (want_id) WHERE want_id IS NOT NULL;
CREATE INDEX volition_decisions_intention_idx ON volition_decisions (intention_id) WHERE intention_id IS NOT NULL;
CREATE TRIGGER volition_decisions_append_only BEFORE UPDATE OR DELETE ON volition_decisions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
