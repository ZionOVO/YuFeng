-- +goose Up
CREATE TABLE IF NOT EXISTS workers (
    worker_id          text PRIMARY KEY,
    capability_labels  jsonb NOT NULL DEFAULT '[]',
    version            text NOT NULL DEFAULT '',
    bindings           jsonb NOT NULL DEFAULT '[]',
    updated_at         timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS workers;
