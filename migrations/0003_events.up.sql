-- Immutable cognitive events and durable consumer cursors.

CREATE TABLE cognitive_events (
  id             uuid PRIMARY KEY,
  seq            bigserial NOT NULL UNIQUE,
  mind_id        uuid NOT NULL REFERENCES minds(id),
  type           text NOT NULL,
  category       text NOT NULL,
  source         text NOT NULL,
  occurred_at    timestamptz NOT NULL,
  recorded_at    timestamptz NOT NULL DEFAULT now(),
  parent_ids     uuid[] NOT NULL DEFAULT '{}',
  causation_id   uuid,
  correlation_id uuid,
  salience       real NOT NULL DEFAULT 0 CHECK (salience >= 0 AND salience <= 1),
  confidence     real NOT NULL DEFAULT 0 CHECK (confidence >= 0 AND confidence <= 1),
  valence        real CHECK (valence IS NULL OR (valence >= -1 AND valence <= 1)),
  arousal        real CHECK (arousal IS NULL OR (arousal >= 0 AND arousal <= 1)),
  evidence       jsonb NOT NULL DEFAULT '[]'::jsonb,
  summary        text NOT NULL,
  metadata       jsonb NOT NULL DEFAULT '{}'::jsonb,
  payload        jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX cognitive_events_mind_seq_idx ON cognitive_events (mind_id, seq);
CREATE INDEX cognitive_events_mind_type_seq_idx ON cognitive_events (mind_id, type, seq);
CREATE INDEX cognitive_events_correlation_idx ON cognitive_events (correlation_id) WHERE correlation_id IS NOT NULL;
CREATE INDEX cognitive_events_causation_idx ON cognitive_events (causation_id) WHERE causation_id IS NOT NULL;
CREATE INDEX cognitive_events_parents_idx ON cognitive_events USING gin (parent_ids);
CREATE INDEX cognitive_events_evidence_idx ON cognitive_events USING gin (evidence jsonb_path_ops);
CREATE TRIGGER cognitive_events_append_only BEFORE UPDATE OR DELETE ON cognitive_events
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

-- Durable read positions for consumers (the mind runtime, consolidation, ...).
CREATE TABLE mind_cursors (
  mind_id    uuid NOT NULL REFERENCES minds(id),
  consumer   text NOT NULL,
  last_seq   bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (mind_id, consumer)
);
