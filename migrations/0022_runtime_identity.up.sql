-- Runtime identity for reproducibility (review pass §7.2 item 9).
ALTER TABLE snapshots ADD COLUMN IF NOT EXISTS identity jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE replays
  ADD COLUMN IF NOT EXISTS identity jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS compatibility text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS identity_diff text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS divergence jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE replays DROP CONSTRAINT IF EXISTS replays_status_check;
ALTER TABLE replays ADD CONSTRAINT replays_status_check CHECK (status IN ('running','done','failed','incompatible'));
