UPDATE replays SET status='failed' WHERE status='incompatible';
ALTER TABLE replays DROP CONSTRAINT IF EXISTS replays_status_check;
ALTER TABLE replays ADD CONSTRAINT replays_status_check CHECK (status IN ('running','done','failed'));
ALTER TABLE replays DROP COLUMN IF EXISTS divergence, DROP COLUMN IF EXISTS identity_diff, DROP COLUMN IF EXISTS compatibility, DROP COLUMN IF EXISTS identity;
ALTER TABLE snapshots DROP COLUMN IF EXISTS identity;
