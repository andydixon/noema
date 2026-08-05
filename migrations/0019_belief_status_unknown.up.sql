-- Explicit unknown (review pass §7.2 item 13): a belief with no live evidence
-- on either side is 'unknown', not tentatively held at its prior. Absence
-- of evidence is not evidence of falsity.
ALTER TABLE beliefs DROP CONSTRAINT IF EXISTS beliefs_status_check;
ALTER TABLE beliefs ADD CONSTRAINT beliefs_status_check
  CHECK (status IN ('hypothesis','tentative','likely','accepted','contested','rejected','superseded','unknown'));
