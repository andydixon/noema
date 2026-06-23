-- LLM providers, models, role assignments, recorded calls and versioned prompts.

CREATE TABLE llm_providers (
  id            uuid PRIMARY KEY,
  name          text NOT NULL,
  kind          text NOT NULL CHECK (kind IN ('openai','anthropic','ollama')),
  endpoint      text NOT NULL,
  api_key_ref   text NOT NULL DEFAULT '',   -- env:, file: or enc: reference; never plaintext
  allow_private boolean NOT NULL DEFAULT false,
  headers       jsonb NOT NULL DEFAULT '{}'::jsonb,
  timeout_ms    integer NOT NULL DEFAULT 60000,
  enabled       boolean NOT NULL DEFAULT true,
  last_test_at  timestamptz,
  last_test_ok  boolean,
  last_test_msg text NOT NULL DEFAULT '',
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX llm_providers_name_idx ON llm_providers (lower(name));

CREATE TABLE llm_models (
  id                 uuid PRIMARY KEY,
  provider_id        uuid NOT NULL REFERENCES llm_providers(id) ON DELETE CASCADE,
  name               text NOT NULL,
  kind               text NOT NULL DEFAULT 'chat' CHECK (kind IN ('chat','embedding')),
  context_window     integer NOT NULL DEFAULT 0,
  dims               integer NOT NULL DEFAULT 0,
  input_cost_per_m   double precision NOT NULL DEFAULT 0,  -- USD per million tokens
  output_cost_per_m  double precision NOT NULL DEFAULT 0,
  params             jsonb NOT NULL DEFAULT '{}'::jsonb,   -- default temperature, top_p, max_tokens
  enabled            boolean NOT NULL DEFAULT true,
  created_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider_id, name)
);

CREATE TABLE llm_roles (
  role         text PRIMARY KEY,
  model_id     uuid REFERENCES llm_models(id) ON DELETE SET NULL,
  fallback_ids uuid[] NOT NULL DEFAULT '{}',
  temperature  double precision,
  max_tokens   integer,
  updated_at   timestamptz NOT NULL DEFAULT now(),
  updated_by   text NOT NULL DEFAULT ''
);

-- Every model call, for inspection, cost accounting and replay.
CREATE TABLE llm_calls (
  id             uuid PRIMARY KEY,
  mind_id        uuid REFERENCES minds(id),
  correlation_id uuid,
  role           text NOT NULL,
  purpose        text NOT NULL DEFAULT '',
  provider       text NOT NULL,
  model          text NOT NULL,
  prompt_name    text NOT NULL DEFAULT '',
  prompt_version integer NOT NULL DEFAULT 0,
  request_hash   text NOT NULL,
  request        jsonb NOT NULL,
  response       jsonb,
  error          text NOT NULL DEFAULT '',
  prompt_tokens  integer NOT NULL DEFAULT 0,
  output_tokens  integer NOT NULL DEFAULT 0,
  cost           double precision NOT NULL DEFAULT 0,
  latency_ms     integer NOT NULL DEFAULT 0,
  seed           bigint,
  replayed       boolean NOT NULL DEFAULT false,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX llm_calls_time_idx ON llm_calls (created_at DESC);
CREATE INDEX llm_calls_mind_idx ON llm_calls (mind_id, created_at DESC);
CREATE INDEX llm_calls_hash_idx ON llm_calls (request_hash);
CREATE INDEX llm_calls_corr_idx ON llm_calls (correlation_id) WHERE correlation_id IS NOT NULL;

CREATE TABLE prompts (
  name            text PRIMARY KEY,
  description     text NOT NULL DEFAULT '',
  role            text NOT NULL,
  current_version integer NOT NULL DEFAULT 1,
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE prompt_versions (
  id         uuid PRIMARY KEY,
  name       text NOT NULL REFERENCES prompts(name),
  version    integer NOT NULL,
  system     text NOT NULL DEFAULT '',
  template   text NOT NULL,
  schema     jsonb,
  note       text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name, version)
);
CREATE TRIGGER prompt_versions_append_only BEFORE UPDATE OR DELETE ON prompt_versions
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();
