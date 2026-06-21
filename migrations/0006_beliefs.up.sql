-- Beliefs with provenance: evidence for/against, alternatives, append-only
-- version history, contradiction links and operator corrections.

CREATE TABLE beliefs (
  id              uuid PRIMARY KEY,
  mind_id         uuid NOT NULL REFERENCES minds(id),
  proposition     text NOT NULL,
  subject         text NOT NULL DEFAULT '',
  epistemic       text NOT NULL,
  status          text NOT NULL CHECK (status IN ('hypothesis','tentative','likely','accepted','contested','rejected','superseded')),
  confidence      double precision NOT NULL CHECK (confidence >= 0 AND confidence <= 1),
  prior           double precision NOT NULL DEFAULT 0.5,
  version         integer NOT NULL DEFAULT 1,
  source          text NOT NULL DEFAULT '',
  detail          jsonb NOT NULL DEFAULT '{}'::jsonb,
  origin_event    uuid,
  superseded_by   uuid REFERENCES beliefs(id),
  pinned_status   text, -- operator override (e.g. rejected); NULL = derived
  first_created   timestamptz NOT NULL DEFAULT now(),
  last_evaluated  timestamptz NOT NULL DEFAULT now(),
  evaluations     integer NOT NULL DEFAULT 1,
  search          tsvector GENERATED ALWAYS AS (to_tsvector('english', proposition)) STORED
);
CREATE INDEX beliefs_mind_status_idx ON beliefs (mind_id, status, confidence DESC);
CREATE INDEX beliefs_mind_subject_idx ON beliefs (mind_id, subject);
CREATE INDEX beliefs_search_idx ON beliefs USING gin (search);

CREATE TABLE belief_versions (
  id          uuid PRIMARY KEY,
  belief_id   uuid NOT NULL REFERENCES beliefs(id),
  mind_id     uuid NOT NULL REFERENCES minds(id),
  version     integer NOT NULL,
  status      text NOT NULL,
  confidence  double precision NOT NULL,
  proposition text NOT NULL,
  reason      text NOT NULL DEFAULT '',
  breakdown   jsonb NOT NULL DEFAULT '{}'::jsonb,
  event_id    uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (belief_id, version)
);
CREATE TRIGGER belief_versions_append_only BEFORE UPDATE OR DELETE ON belief_versions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

CREATE TABLE belief_evidence (
  id          uuid PRIMARY KEY,
  belief_id   uuid NOT NULL REFERENCES beliefs(id),
  mind_id     uuid NOT NULL REFERENCES minds(id),
  direction   text NOT NULL CHECK (direction IN ('for','against')),
  kind        text NOT NULL,   -- event | memory | belief | prediction | operator | note
  ref_id      text NOT NULL DEFAULT '',
  source      text NOT NULL DEFAULT '',
  weight      double precision NOT NULL CHECK (weight > 0 AND weight <= 1),
  credibility double precision NOT NULL CHECK (credibility >= 0 AND credibility <= 1),
  note        text NOT NULL DEFAULT '',
  event_id    uuid,
  retracted   boolean NOT NULL DEFAULT false,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX belief_evidence_belief_idx ON belief_evidence (belief_id);
CREATE INDEX belief_evidence_ref_idx ON belief_evidence (kind, ref_id);

CREATE TABLE belief_alternatives (
  id          uuid PRIMARY KEY,
  belief_id   uuid NOT NULL REFERENCES beliefs(id),
  mind_id     uuid NOT NULL REFERENCES minds(id),
  proposition text NOT NULL,
  confidence  double precision NOT NULL DEFAULT 0.2,
  note        text NOT NULL DEFAULT '',
  status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open','dismissed','promoted')),
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX belief_alternatives_belief_idx ON belief_alternatives (belief_id);

CREATE TABLE belief_links (
  id         uuid PRIMARY KEY,
  mind_id    uuid NOT NULL REFERENCES minds(id),
  from_id    uuid NOT NULL REFERENCES beliefs(id),
  to_id      uuid NOT NULL REFERENCES beliefs(id),
  relation   text NOT NULL, -- contradicts | supports | alternative_to | derived_from
  confidence double precision NOT NULL DEFAULT 0.5,
  event_id   uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (from_id, to_id, relation)
);
CREATE INDEX belief_links_to_idx ON belief_links (to_id);

-- Operator corrections. Append-only: a correction can be superseded by another
-- correction but never erased (spec §40).
CREATE TABLE corrections (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  kind        text NOT NULL CHECK (kind IN ('belief_wrong','memory_inaccurate','entities_same','entities_different','do_not_infer')),
  target_kind text NOT NULL DEFAULT '',
  target_id   text NOT NULL DEFAULT '',
  statement   text NOT NULL DEFAULT '',
  reason      text NOT NULL DEFAULT '',
  created_by  text NOT NULL DEFAULT '',
  event_id    uuid,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX corrections_mind_kind_idx ON corrections (mind_id, kind);
CREATE TRIGGER corrections_append_only BEFORE UPDATE OR DELETE ON corrections
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
