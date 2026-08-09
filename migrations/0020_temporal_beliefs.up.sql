-- Temporal scope (review pass §7.2 item 12). A belief may describe a
-- persistent fact or a transient state. Transient states have a validity
-- interval: a later observation that the state changed closes the interval
-- and supersedes the belief instead of contesting it, so what was true at
-- 10:00 stays true of 10:00 when 10:05 differs.
ALTER TABLE beliefs
  ADD COLUMN IF NOT EXISTS temporal_scope text NOT NULL DEFAULT 'unknown' CHECK (temporal_scope IN ('persistent','transient','unknown')),
  ADD COLUMN IF NOT EXISTS observed_at timestamptz,
  ADD COLUMN IF NOT EXISTS valid_from timestamptz,
  ADD COLUMN IF NOT EXISTS valid_until timestamptz;
CREATE INDEX IF NOT EXISTS beliefs_mind_subject_validity_idx ON beliefs (mind_id, subject, valid_until);
