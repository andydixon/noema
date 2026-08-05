-- Assertions (review pass §7.2 item 1): the recorded fact that an actor
-- stated a proposition, kept apart from any belief about the proposition.
CREATE TABLE assertions (
  id                        uuid PRIMARY KEY,
  mind_id                   uuid NOT NULL REFERENCES minds(id),
  actor                     text NOT NULL,             -- who spoke to the mind
  actor_kind                text NOT NULL DEFAULT 'user',
  attributed_to             text NOT NULL,             -- whose stance this is (speaker, or the person they report)
  proposition               text NOT NULL,
  subject                   text NOT NULL DEFAULT '',
  stance                    text NOT NULL CHECK (stance IN ('asserts','denies','doubts','questions','supposes','reports')),
  certainty                 text NOT NULL CHECK (certainty IN ('asserted','hedged','uncertain','emphatic')),
  modality                  text NOT NULL CHECK (modality IN ('factual','hypothetical','reported','quoted','imperative','question')),
  negated                   boolean NOT NULL DEFAULT false,
  reported                  boolean NOT NULL DEFAULT false,
  reported_actor            text NOT NULL DEFAULT '',
  quoted                    boolean NOT NULL DEFAULT false,
  hypothetical              boolean NOT NULL DEFAULT false,
  interpretation_confidence double precision NOT NULL CHECK (interpretation_confidence >= 0 AND interpretation_confidence <= 1),
  interpretation_notes      text[] NOT NULL DEFAULT '{}',
  source_event              uuid,
  conversation_id           uuid,
  message_id                uuid,
  context                   text NOT NULL DEFAULT '',
  trust                     text NOT NULL DEFAULT 'user',
  belief_id                 uuid,                      -- the proposition belief this fed, if any
  created_at                timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assertions_mind_time_idx ON assertions (mind_id, created_at DESC);
CREATE INDEX assertions_mind_actor_idx ON assertions (mind_id, attributed_to);
CREATE INDEX assertions_belief_idx ON assertions (belief_id) WHERE belief_id IS NOT NULL;
CREATE INDEX assertions_source_idx ON assertions (source_event) WHERE source_event IS NOT NULL;
CREATE TRIGGER assertions_append_only BEFORE UPDATE OR DELETE ON assertions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
