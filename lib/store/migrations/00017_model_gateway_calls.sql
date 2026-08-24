-- +goose Up
CREATE TABLE IF NOT EXISTS model_gateway_calls (
    id          bigserial PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    kind        text NOT NULL,
    ok          boolean NOT NULL,
    host        text NOT NULL DEFAULT '',
    model       text NOT NULL DEFAULT '',
    latency_ms  integer NOT NULL DEFAULT 0,
    error       text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS model_gateway_calls_at_idx ON model_gateway_calls (occurred_at DESC);

-- +goose Down
DROP INDEX IF EXISTS model_gateway_calls_at_idx;
DROP TABLE IF EXISTS model_gateway_calls;
