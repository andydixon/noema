-- Research measurements (docs/cognitive-research.md): every value a
-- cognitive measure produces, with its subject and provenance. Append-only.
CREATE TABLE research_measurements (
  id            uuid PRIMARY KEY,
  mind_id       uuid NOT NULL REFERENCES minds(id),
  measure       text NOT NULL,            -- name in research.Measures
  value         double precision,
  label         text NOT NULL DEFAULT '', -- categorical result, e.g. supported
  subject_kind  text NOT NULL DEFAULT '',
  subject_id    text NOT NULL DEFAULT '',
  experiment_id uuid,
  event_id      uuid,
  detail        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX research_measurements_mind_idx ON research_measurements (mind_id, measure, created_at DESC);
CREATE INDEX research_measurements_experiment_idx ON research_measurements (experiment_id) WHERE experiment_id IS NOT NULL;
CREATE TRIGGER research_measurements_append_only BEFORE UPDATE OR DELETE ON research_measurements
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
