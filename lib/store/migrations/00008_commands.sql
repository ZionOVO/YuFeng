-- +goose Up
CREATE TABLE commands (
    command_id      text PRIMARY KEY,
    unit_id         text NOT NULL,
    run_id          text NOT NULL DEFAULT '',
    procedure_ref   text NOT NULL DEFAULT '',
    artifact_ref    text NOT NULL DEFAULT '',
    target_asset_id text NOT NULL DEFAULT '',
    steps           jsonb NOT NULL DEFAULT '[]',
    deadline        timestamptz,
    status          text NOT NULL DEFAULT 'pending',
    lease_id        text NOT NULL DEFAULT '',
    lease_deadline  timestamptz,
    result_json     text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX commands_pending_idx ON commands(unit_id, status, created_at);

-- +goose Down
DROP TABLE IF EXISTS commands;
