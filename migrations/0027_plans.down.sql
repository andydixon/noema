ALTER TABLE approval_requests DROP CONSTRAINT approval_requests_status_check;
ALTER TABLE approval_requests ADD CONSTRAINT approval_requests_status_check CHECK (status IN ('pending','approved','denied','expired','executed','failed'));
DROP INDEX IF EXISTS approval_requests_origin_idx;
DROP INDEX IF EXISTS tool_executions_origin_idx;
ALTER TABLE approval_requests DROP COLUMN origin;
ALTER TABLE tool_executions DROP COLUMN origin;
DROP TABLE IF EXISTS plan_steps;
DROP TABLE IF EXISTS plans;
