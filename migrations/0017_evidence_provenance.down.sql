DROP INDEX IF EXISTS belief_evidence_family_idx;
ALTER TABLE belief_evidence
  DROP COLUMN IF EXISTS provenance_status,
  DROP COLUMN IF EXISTS root_ids,
  DROP COLUMN IF EXISTS family,
  DROP COLUMN IF EXISTS derivation;
