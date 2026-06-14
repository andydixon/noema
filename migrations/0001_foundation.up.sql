-- Foundation: append-only guard, users, sessions, API tokens, audit log, settings.

-- Raise on any UPDATE/DELETE. Attached to every history table so immutability
-- is enforced by the database, not by application discipline (ADR-0002).
CREATE OR REPLACE FUNCTION noema_forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'table % is append-only', TG_TABLE_NAME USING ERRCODE = 'integrity_constraint_violation';
END $$;

CREATE TABLE users (
  id                  uuid PRIMARY KEY,
  username            text NOT NULL,
  password_hash       text NOT NULL,
  role                text NOT NULL CHECK (role IN ('administrator','operator','observer')),
  disabled            boolean NOT NULL DEFAULT false,
  failed_attempts     integer NOT NULL DEFAULT 0,
  locked_until        timestamptz,
  totp_secret_enc     text,
  totp_enabled        boolean NOT NULL DEFAULT false,
  password_changed_at timestamptz NOT NULL DEFAULT now(),
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_username_lower_idx ON users (lower(username));

CREATE TABLE recovery_codes (
  id        uuid PRIMARY KEY,
  user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash bytea NOT NULL,
  used_at   timestamptz
);
CREATE INDEX recovery_codes_user_idx ON recovery_codes (user_id);

CREATE TABLE sessions (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE,
  csrf_token   text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  ip           text NOT NULL DEFAULT '',
  user_agent   text NOT NULL DEFAULT '',
  revoked_at   timestamptz
);
CREATE INDEX sessions_user_idx ON sessions (user_id) WHERE revoked_at IS NULL;

CREATE TABLE api_tokens (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         text NOT NULL,
  token_hash   bytea NOT NULL UNIQUE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  expires_at   timestamptz,
  revoked_at   timestamptz
);
CREATE INDEX api_tokens_user_idx ON api_tokens (user_id);

-- Administrative actions. Append-only.
CREATE TABLE audit_log (
  id          uuid PRIMARY KEY,
  seq         bigserial NOT NULL,
  at          timestamptz NOT NULL DEFAULT now(),
  actor_kind  text NOT NULL CHECK (actor_kind IN ('user','system','mind')),
  actor_id    text NOT NULL DEFAULT '',
  actor_name  text NOT NULL DEFAULT '',
  action      text NOT NULL,
  target_kind text NOT NULL DEFAULT '',
  target_id   text NOT NULL DEFAULT '',
  details     jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip          text NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_seq_idx ON audit_log (seq);
CREATE INDEX audit_log_target_idx ON audit_log (target_kind, target_id);
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION noema_forbid_change();

-- Runtime-editable settings (tunables only, never secrets).
CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  updated_by text NOT NULL DEFAULT ''
);
