-- +goose Up
CREATE TABLE worker_bootstrap (
    token_hash          text PRIMARY KEY,
    worker_id           text NOT NULL UNIQUE,
    worker_kind         text NOT NULL CHECK (worker_kind IN ('RUN_SUPERVISOR', 'ANALYSIS_SCORER')),
    public_key          text NOT NULL,
    client_cert_sha256  text NOT NULL,
    expires_at          timestamptz NOT NULL,
    used_at             timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE worker_identities (
    worker_id           text PRIMARY KEY,
    worker_kind         text NOT NULL CHECK (worker_kind IN ('RUN_SUPERVISOR', 'ANALYSIS_SCORER')),
    public_key          text NOT NULL,
    client_cert_sha256  text NOT NULL,
    refresh_token_hash  text NOT NULL UNIQUE,
    refresh_expires_at  timestamptz NOT NULL,
    revoked_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE worker_access_tokens (
    token_hash          text PRIMARY KEY,
    worker_id           text NOT NULL REFERENCES worker_identities(worker_id) ON DELETE CASCADE,
    expires_at          timestamptz NOT NULL,
    public_key_hash     text NOT NULL,
    client_cert_sha256  text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE workers ADD COLUMN worker_kind text NOT NULL DEFAULT 'RUN_SUPERVISOR';
ALTER TABLE workers ADD COLUMN identity_domain text NOT NULL DEFAULT 'agent_compat';
ALTER TABLE workers ADD COLUMN analyzer_profiles jsonb NOT NULL DEFAULT '[]';
ALTER TABLE workers ADD COLUMN max_concurrency integer NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 64);

CREATE TABLE analysis_work_items (
    analysis_work_id        text PRIMARY KEY,
    event_id                text NOT NULL UNIQUE REFERENCES check_tickets(event_id),
    ticket_digest           text NOT NULL,
    status                  text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'leased', 'succeeded', 'failed')),
    worker_id               text NOT NULL DEFAULT '',
    analyzer_profile_ref    text NOT NULL DEFAULT '',
    analyzer_profile_digest text NOT NULL DEFAULT '',
    model_group             text NOT NULL DEFAULT '',
    model_type              text NOT NULL DEFAULT '',
    model_version           text NOT NULL DEFAULT '',
    bindings                jsonb NOT NULL,
    deadline                timestamptz NOT NULL DEFAULT now() + interval '1 hour',
    lease_id                text NOT NULL DEFAULT '',
    lease_epoch             bigint NOT NULL DEFAULT 0,
    lease_deadline          timestamptz,
    attempts                integer NOT NULL DEFAULT 0,
    result_inference_id     text NOT NULL DEFAULT '',
    last_error_code         text NOT NULL DEFAULT '',
    last_error              text NOT NULL DEFAULT '',
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (analyzer_profile_ref = '' AND analyzer_profile_digest = '' AND model_group = '' AND model_type = '' AND model_version = '') OR
        (analyzer_profile_ref <> '' AND analyzer_profile_digest <> '' AND model_group <> '' AND model_type <> '' AND model_version <> '')
    )
);
CREATE INDEX analysis_work_pending_idx ON analysis_work_items(status, created_at);
CREATE INDEX analysis_work_worker_idx ON analysis_work_items(worker_id, status, lease_deadline);
CREATE INDEX analysis_work_deadline_idx ON analysis_work_items(deadline) WHERE status IN ('pending', 'leased');

CREATE TABLE analysis_attempts (
    attempt_id        bigserial PRIMARY KEY,
    analysis_work_id  text NOT NULL REFERENCES analysis_work_items(analysis_work_id),
    worker_id         text NOT NULL,
    lease_id          text NOT NULL,
    lease_epoch       bigint NOT NULL,
    outcome           text NOT NULL DEFAULT 'leased' CHECK (outcome IN ('leased', 'succeeded', 'retryable_failure', 'terminal_failure', 'expired')),
    error_code        text NOT NULL DEFAULT '',
    error_message     text NOT NULL DEFAULT '',
    started_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,
    UNIQUE (analysis_work_id, lease_epoch)
);

-- +goose Down
DROP TABLE IF EXISTS analysis_attempts;
DROP TABLE IF EXISTS analysis_work_items;
ALTER TABLE workers DROP COLUMN IF EXISTS max_concurrency;
ALTER TABLE workers DROP COLUMN IF EXISTS analyzer_profiles;
ALTER TABLE workers DROP COLUMN IF EXISTS identity_domain;
ALTER TABLE workers DROP COLUMN IF EXISTS worker_kind;
DROP TABLE IF EXISTS worker_access_tokens;
DROP TABLE IF EXISTS worker_identities;
DROP TABLE IF EXISTS worker_bootstrap;
