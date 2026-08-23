-- Privacy redaction (review pass §7.2 item 8): the audited maintenance path
-- ADR-0002 promised. Redaction replaces the sensitive content of a row with a
-- fixed marker while keeping the row, its identifiers, its links and a hash
-- of what was removed. It is the only write ever permitted on append-only
-- tables, it is confined to content columns, and it runs through
-- noema_redact() so the application cannot reach the bypass by accident.

CREATE TABLE redactions (
  id           uuid PRIMARY KEY,
  mind_id      uuid NOT NULL REFERENCES minds(id),
  target_kind  text NOT NULL CHECK (target_kind IN ('event','memory','message','llm_call','belief','assertion')),
  target_id    uuid NOT NULL,
  reason       text NOT NULL,
  policy       text NOT NULL DEFAULT '',   -- retention or privacy policy invoked
  content_hash text NOT NULL,              -- sha256 of the removed content, for later attestation
  requested_by text NOT NULL,
  cascade_of   uuid REFERENCES redactions(id),
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (target_kind, target_id)
);
CREATE INDEX redactions_mind_idx ON redactions (mind_id, created_at DESC);
CREATE TRIGGER redactions_append_only BEFORE UPDATE OR DELETE ON redactions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

ALTER TABLE cognitive_events ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;
ALTER TABLE memories        ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;
ALTER TABLE messages        ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;
ALTER TABLE llm_calls       ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;
ALTER TABLE beliefs         ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;
ALTER TABLE assertions      ADD COLUMN IF NOT EXISTS redacted_at timestamptz, ADD COLUMN IF NOT EXISTS redaction_id uuid;

-- The append-only trigger admits exactly one kind of UPDATE: a redaction
-- running under the session flag that only noema_redact() sets. DELETE
-- stays forbidden in every case.
CREATE OR REPLACE FUNCTION noema_forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND current_setting('noema.redaction', true) = 'on' THEN
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'table % is append-only', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
END $$;

-- noema_redact replaces content columns for one row. It sets the bypass
-- flag for the current transaction only, touches nothing but content and
-- the redaction bookkeeping, and returns the number of rows changed.
CREATE OR REPLACE FUNCTION noema_redact(kind text, target uuid, redaction uuid) RETURNS integer LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE n integer := 0;
BEGIN
  PERFORM set_config('noema.redaction', 'on', true);
  IF kind = 'event' THEN
    UPDATE cognitive_events SET summary = '[redacted]', payload = '{"redacted":true}'::jsonb,
      metadata = (SELECT coalesce(jsonb_object_agg(key, value), '{}'::jsonb) FROM jsonb_each(metadata)
                  WHERE key IN ('conversation_id','message_id','cycle','belief_id','memory_id','integration_id','integration','kind','trust','method','replay_of','replay_id'))
                 || jsonb_build_object('redacted', true),
      redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
  ELSIF kind = 'memory' THEN
    UPDATE memories SET content = '[redacted]', detail = '{"redacted":true}'::jsonb, redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
  ELSIF kind = 'message' THEN
    UPDATE messages SET content = '[redacted]', intent = NULL, redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
  ELSIF kind = 'llm_call' THEN
    UPDATE llm_calls SET request = '{"redacted":true}'::jsonb, response = NULL, error = CASE WHEN error = '' THEN '' ELSE '[redacted]' END, redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
  ELSIF kind = 'belief' THEN
    UPDATE beliefs SET proposition = '[redacted]', detail = '{"redacted":true}'::jsonb, redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
    GET DIAGNOSTICS n = ROW_COUNT;
    IF n > 0 THEN
      UPDATE belief_versions SET proposition = '[redacted]', reason = '[redacted]' WHERE belief_id = target;
      UPDATE belief_evidence SET note = '[redacted]' WHERE belief_id = target;
      UPDATE belief_alternatives SET proposition = '[redacted]', note = '[redacted]' WHERE belief_id = target;
    END IF;
    PERFORM set_config('noema.redaction', 'off', true);
    RETURN n;
  ELSIF kind = 'assertion' THEN
    UPDATE assertions SET proposition = '[redacted]', context = '[redacted]', interpretation_notes = '{}', redacted_at = now(), redaction_id = redaction WHERE id = target AND redacted_at IS NULL;
  ELSE
    RAISE EXCEPTION 'unknown redaction kind %', kind;
  END IF;
  GET DIAGNOSTICS n = ROW_COUNT;
  PERFORM set_config('noema.redaction', 'off', true);
  RETURN n;
END $$;
REVOKE ALL ON FUNCTION noema_redact(text, uuid, uuid) FROM PUBLIC;
