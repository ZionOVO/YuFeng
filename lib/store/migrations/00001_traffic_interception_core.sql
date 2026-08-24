-- +goose Up
CREATE TABLE units (
    unit_id          text PRIMARY KEY,
    kind             text NOT NULL,
    version          text NOT NULL DEFAULT '',
    contract_version text NOT NULL DEFAULT 'v1',
    pubkey_hint      text NOT NULL DEFAULT '',
    token_hash       text NOT NULL DEFAULT '',
    health           text NOT NULL DEFAULT 'healthy',
    generation       bigint NOT NULL DEFAULT 0,
    last_heartbeat_at timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE assets (
    asset_id       text PRIMARY KEY,
    display_name   text NOT NULL DEFAULT '',
    access_mode    text NOT NULL DEFAULT 'network',
    transports     jsonb NOT NULL DEFAULT '[]',
    capabilities   jsonb NOT NULL DEFAULT '{}',
    criticality    text NOT NULL DEFAULT 'p2',
    max_auto_tier  text NOT NULL DEFAULT 'L0',
    labels         jsonb NOT NULL DEFAULT '{}',
    last_probe_at  timestamptz,
    version        bigint NOT NULL DEFAULT 1,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE unit_assets (
    unit_id      text NOT NULL REFERENCES units(unit_id) ON DELETE CASCADE,
    asset_id     text NOT NULL REFERENCES assets(asset_id) ON DELETE CASCADE,
    relation     text NOT NULL DEFAULT 'protects',
    is_primary   boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (unit_id, asset_id)
);

CREATE TABLE releases (
    release_id        text PRIMARY KEY,
    state             text NOT NULL,
    artifact_id       text NOT NULL DEFAULT '',
    artifact          jsonb NOT NULL,
    supersedes        text NOT NULL DEFAULT '',
    ttl_seconds       bigint NOT NULL,
    canary_percent    int NOT NULL DEFAULT 0,
    created_by        text NOT NULL DEFAULT '',
    proposed_at       timestamptz NOT NULL DEFAULT now(),
    signed_at         timestamptz,
    shadow_started_at timestamptz,
    canary_started_at timestamptz,
    enforced_at       timestamptz,
    retired_at        timestamptz,
    retire_reason     text NOT NULL DEFAULT '',
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE release_assets (
    release_id text NOT NULL REFERENCES releases(release_id) ON DELETE CASCADE,
    asset_id   text NOT NULL REFERENCES assets(asset_id) ON DELETE CASCADE,
    PRIMARY KEY (release_id, asset_id)
);

CREATE TABLE events (
    event_id       text PRIMARY KEY,
    unit_id        text NOT NULL DEFAULT '',
    asset_id       text NOT NULL DEFAULT '',
    request_id     text NOT NULL DEFAULT '',
    occurred_at    timestamptz NOT NULL,
    ingested_at    timestamptz NOT NULL DEFAULT now(),
    source         text NOT NULL DEFAULT '',
    kind           text NOT NULL,
    verdict        text NOT NULL,
    payload        jsonb NOT NULL,
    release_traces jsonb NOT NULL DEFAULT '[]'
);
CREATE INDEX events_asset_time_idx ON events(asset_id, occurred_at DESC);
CREATE INDEX events_verdict_time_idx ON events(verdict, occurred_at DESC);
CREATE INDEX events_release_time_idx ON events((release_traces::text), occurred_at DESC);

CREATE TABLE audit_entries (
    sequence      bigserial PRIMARY KEY,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text NOT NULL,
    actor_id      text NOT NULL,
    action        text NOT NULL,
    object_type   text NOT NULL,
    object_id     text NOT NULL DEFAULT '',
    details       jsonb NOT NULL DEFAULT '{}',
    previous_hash text NOT NULL,
    entry_hash    text NOT NULL
);
CREATE INDEX audit_object_idx ON audit_entries(object_type, object_id, sequence DESC);

CREATE TABLE users (
    user_id       text PRIMARY KEY,
    username      text NOT NULL UNIQUE,
    display_name  text NOT NULL DEFAULT '',
    role          text NOT NULL,
    state         text NOT NULL,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz
);

CREATE TABLE user_sessions (
    token_hash text PRIMARY KEY,
    user_id    text NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS audit_entries;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS release_assets;
DROP TABLE IF EXISTS releases;
DROP TABLE IF EXISTS unit_assets;
DROP TABLE IF EXISTS assets;
DROP TABLE IF EXISTS units;
