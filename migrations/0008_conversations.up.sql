-- Conversations and messages. A user message is also an observation event;
-- a mind message is also a response_generated event. Rows here are the
-- conversational view; provenance lives in the events they reference.
CREATE TABLE conversations (
  id              uuid PRIMARY KEY,
  mind_id         uuid NOT NULL REFERENCES minds(id),
  title           text NOT NULL DEFAULT '',
  channel         text NOT NULL DEFAULT 'web',
  person          text NOT NULL DEFAULT '',
  project         text NOT NULL DEFAULT '',
  context         jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at      timestamptz NOT NULL DEFAULT now(),
  last_message_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversations_mind_idx ON conversations (mind_id, last_message_at DESC);

CREATE TABLE messages (
  id              uuid PRIMARY KEY,
  conversation_id uuid NOT NULL REFERENCES conversations(id),
  mind_id         uuid NOT NULL REFERENCES minds(id),
  role            text NOT NULL CHECK (role IN ('user','mind','system')),
  actor           text NOT NULL DEFAULT '',
  content         text NOT NULL,
  event_id        uuid,
  reply_to        uuid REFERENCES messages(id),
  intent          jsonb,
  llm_call_id     uuid,
  created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_conversation_idx ON messages (conversation_id, created_at);
CREATE INDEX messages_reply_idx ON messages (reply_to) WHERE reply_to IS NOT NULL;
