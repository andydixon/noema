-- Performance pass (Phase 16): indexes matching the hot list orders found by
-- tests/perf. The planner chose sequential scans because the existing
-- (mind_id, kind|status) indexes could not serve the ORDER BY.
CREATE INDEX IF NOT EXISTS memories_mind_recent_idx ON memories (mind_id, occurred_at DESC, id DESC) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS beliefs_mind_confidence_idx ON beliefs (mind_id, confidence DESC) WHERE status <> 'superseded';
CREATE INDEX IF NOT EXISTS cognitive_events_mind_category_seq_idx ON cognitive_events (mind_id, category, seq);
CREATE INDEX IF NOT EXISTS belief_versions_mind_created_idx ON belief_versions (mind_id, created_at DESC);
