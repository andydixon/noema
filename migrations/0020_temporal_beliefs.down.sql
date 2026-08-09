DROP INDEX IF EXISTS beliefs_mind_subject_validity_idx;
ALTER TABLE beliefs
  DROP COLUMN IF EXISTS valid_until,
  DROP COLUMN IF EXISTS valid_from,
  DROP COLUMN IF EXISTS observed_at,
  DROP COLUMN IF EXISTS temporal_scope;
