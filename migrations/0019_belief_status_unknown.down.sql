UPDATE beliefs SET status='tentative' WHERE status='unknown';
ALTER TABLE beliefs DROP CONSTRAINT IF EXISTS beliefs_status_check;
ALTER TABLE beliefs ADD CONSTRAINT beliefs_status_check
  CHECK (status IN ('hypothesis','tentative','likely','accepted','contested','rejected','superseded'));
