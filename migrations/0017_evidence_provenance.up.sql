-- Evidence provenance (review pass §7.2 item 2).
-- family: the independent-source key evidence is grouped by when confidence
--         is computed (an actor, an integration, an operator, a prediction).
-- derivation: how the row relates to its roots (direct, assertion, operator,
--         external, prediction, consolidation, inference, reflection, dream,
--         retrieval). Derived rows never add weight to a family that already
--         has a primary member.
-- root_ids: the originating event or memory ids this row ultimately rests on.
-- provenance_status: 'recorded' for rows written with ancestry; existing rows
--         are marked 'legacy_unknown' with family = source, never invented.
ALTER TABLE belief_evidence
  ADD COLUMN IF NOT EXISTS derivation text NOT NULL DEFAULT 'legacy',
  ADD COLUMN IF NOT EXISTS family text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS root_ids uuid[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS provenance_status text NOT NULL DEFAULT 'legacy_unknown'
    CHECK (provenance_status IN ('recorded','legacy_unknown'));
UPDATE belief_evidence SET family = COALESCE(NULLIF(source, ''), kind || ':' || ref_id) WHERE family = '';
CREATE INDEX IF NOT EXISTS belief_evidence_family_idx ON belief_evidence (belief_id, family);
