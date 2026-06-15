-- Minds, personality versions (append-only), templates, self-models.

CREATE TABLE minds (
  id                   uuid PRIMARY KEY,
  slug                 text NOT NULL,
  display_name         text NOT NULL,
  status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled','archived')),
  config               jsonb NOT NULL DEFAULT '{}'::jsonb,
  notes                text NOT NULL DEFAULT '',
  personality_version  integer NOT NULL DEFAULT 0,
  forked_from_mind     uuid REFERENCES minds(id),
  forked_from_snapshot uuid,
  cloned_from_mind     uuid REFERENCES minds(id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX minds_slug_idx ON minds (lower(slug));

CREATE TABLE personality_templates (
  id          uuid PRIMARY KEY,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  personality jsonb NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX personality_templates_name_idx ON personality_templates (lower(name));

-- Every personality change is a new row; minds.personality_version points at
-- the current one. Rollback writes a new version copying an older one.
CREATE TABLE personality_versions (
  id          uuid PRIMARY KEY,
  mind_id     uuid NOT NULL REFERENCES minds(id),
  version     integer NOT NULL,
  personality jsonb NOT NULL,        -- effective personality
  template_id uuid REFERENCES personality_templates(id),
  overrides   jsonb NOT NULL DEFAULT '{}'::jsonb, -- mind-specific deviations from the template
  change_note text NOT NULL DEFAULT '',
  created_by  text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (mind_id, version)
);
CREATE TRIGGER personality_versions_append_only BEFORE UPDATE OR DELETE ON personality_versions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

CREATE TABLE self_models (
  mind_id    uuid PRIMARY KEY REFERENCES minds(id),
  model      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
