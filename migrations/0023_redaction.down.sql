-- Redacted content cannot be restored; this only removes the machinery.
DROP FUNCTION IF EXISTS noema_redact(text, uuid, uuid);
CREATE OR REPLACE FUNCTION noema_forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'table % is append-only', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
END $$;
ALTER TABLE assertions      DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
ALTER TABLE beliefs         DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
ALTER TABLE llm_calls       DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
ALTER TABLE messages        DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
ALTER TABLE memories        DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
ALTER TABLE cognitive_events DROP COLUMN IF EXISTS redaction_id, DROP COLUMN IF EXISTS redacted_at;
DROP TABLE IF EXISTS redactions;
