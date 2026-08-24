-- +goose Up
-- L1 生产冻结草案：授予、刷新、预算、幂等、发件箱、聚类、世代、模型追加。

ALTER TABLE units ADD COLUMN IF NOT EXISTS refresh_token_hash text NOT NULL DEFAULT '';
ALTER TABLE units ADD COLUMN IF NOT EXISTS refresh_expires_at timestamptz;
ALTER TABLE units ADD COLUMN IF NOT EXISTS token_expires_at timestamptz;

CREATE TABLE IF NOT EXISTS agent_bootstrap (
    token_hash     text PRIMARY KEY,
    agent_id       text NOT NULL UNIQUE,
    used_at        timestamptz,
    expires_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE agents ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS last_heartbeat_at timestamptz;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS refresh_expires_at timestamptz;

CREATE TABLE IF NOT EXISTS grants (
    grant_id         text PRIMARY KEY,
    subject_kind     text NOT NULL, -- user / agent
    subject_id       text NOT NULL,
    tools            jsonb NOT NULL DEFAULT '[]',
    bindings         jsonb NOT NULL DEFAULT '[]',
    created_by       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz
);
CREATE INDEX IF NOT EXISTS grants_subject_idx ON grants(subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS capability_budget (
    jti            text PRIMARY KEY,
    subject        text NOT NULL,
    azp            text NOT NULL DEFAULT '',
    max_calls      bigint NOT NULL,
    calls_used     bigint NOT NULL DEFAULT 0,
    revoked        boolean NOT NULL DEFAULT false,
    expires_at     timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
    scope          text NOT NULL,
    idem_key       text NOT NULL,
    request_digest text NOT NULL,
    status_code    text NOT NULL DEFAULT '',
    response_json  jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, idem_key)
);

CREATE TABLE IF NOT EXISTS outbox (
    outbox_id      bigserial PRIMARY KEY,
    topic          text NOT NULL,
    dedupe_key     text NOT NULL,
    payload        jsonb NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz,
    attempts       int NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS outbox_dedupe_idx ON outbox(topic, dedupe_key);

CREATE TABLE IF NOT EXISTS triage_clusters (
    cluster_id     text PRIMARY KEY,
    asset_id       text NOT NULL,
    route_template text NOT NULL,
    method         text NOT NULL,
    identity_key   text NOT NULL,
    reason         text NOT NULL,
    event_ids      jsonb NOT NULL DEFAULT '[]',
    representative text NOT NULL DEFAULT '',
    opened_at      timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now(),
    closed_at      timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS triage_clusters_open_idx
    ON triage_clusters(asset_id, identity_key)
    WHERE closed_at IS NULL;

CREATE TABLE IF NOT EXISTS asset_generations (
    generation_id  text PRIMARY KEY,
    asset_id       text NOT NULL,
    generation_seq bigint NOT NULL,
    envelope       jsonb NOT NULL,
    signed         boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (asset_id, generation_seq)
);

ALTER TABLE releases ADD COLUMN IF NOT EXISTS review_at timestamptz;
ALTER TABLE releases ADD COLUMN IF NOT EXISTS hard_expires_at timestamptz;
ALTER TABLE releases ADD COLUMN IF NOT EXISTS expiry_behavior text NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS scope_risk text NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS evidence_class text NOT NULL DEFAULT '';
ALTER TABLE releases ADD COLUMN IF NOT EXISTS generation_seq bigint NOT NULL DEFAULT 0;

ALTER TABLE events ADD COLUMN IF NOT EXISTS observation text NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS triage_reason text NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS cluster_id text NOT NULL DEFAULT '';
ALTER TABLE events ADD COLUMN IF NOT EXISTS generation_seq bigint NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN IF NOT EXISTS payload_digest text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS model_inferences (
    inference_id   text PRIMARY KEY,
    event_id       text NOT NULL REFERENCES events(event_id),
    model_group    text NOT NULL,
    model_type     text NOT NULL,
    model_version  text NOT NULL,
    threshold      double precision NOT NULL,
    score          double precision NOT NULL,
    attack_class   text NOT NULL DEFAULT '',
    taxonomy_version text NOT NULL DEFAULT '',
    recorded_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS model_inferences_event_model_idx
    ON model_inferences(event_id, model_group, model_type, model_version);

-- +goose Down
DROP TABLE IF EXISTS model_inferences;
ALTER TABLE events DROP COLUMN IF EXISTS payload_digest;
ALTER TABLE events DROP COLUMN IF EXISTS generation_seq;
ALTER TABLE events DROP COLUMN IF EXISTS cluster_id;
ALTER TABLE events DROP COLUMN IF EXISTS triage_reason;
ALTER TABLE events DROP COLUMN IF EXISTS observation;
ALTER TABLE releases DROP COLUMN IF EXISTS generation_seq;
ALTER TABLE releases DROP COLUMN IF EXISTS evidence_class;
ALTER TABLE releases DROP COLUMN IF EXISTS scope_risk;
ALTER TABLE releases DROP COLUMN IF EXISTS expiry_behavior;
ALTER TABLE releases DROP COLUMN IF EXISTS hard_expires_at;
ALTER TABLE releases DROP COLUMN IF EXISTS review_at;
DROP TABLE IF EXISTS asset_generations;
DROP TABLE IF EXISTS triage_clusters;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS idempotency_keys;
DROP TABLE IF EXISTS capability_budget;
DROP TABLE IF EXISTS grants;
ALTER TABLE agents DROP COLUMN IF EXISTS refresh_expires_at;
ALTER TABLE agents DROP COLUMN IF EXISTS last_heartbeat_at;
ALTER TABLE agents DROP COLUMN IF EXISTS revoked_at;
DROP TABLE IF EXISTS agent_bootstrap;
ALTER TABLE units DROP COLUMN IF EXISTS token_expires_at;
ALTER TABLE units DROP COLUMN IF EXISTS refresh_expires_at;
ALTER TABLE units DROP COLUMN IF EXISTS refresh_token_hash;
