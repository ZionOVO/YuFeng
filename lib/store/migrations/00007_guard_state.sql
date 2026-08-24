-- +goose Up
CREATE TABLE release_guards (
    release_id        text PRIMARY KEY,
    requests_total    bigint NOT NULL DEFAULT 0,
    blocks_total      bigint NOT NULL DEFAULT 0,
    deny_total        bigint NOT NULL DEFAULT 0,
    consecutive_bad   int NOT NULL DEFAULT 0,
    last_bad_at       timestamptz,
    last_bad_reasons  text NOT NULL DEFAULT '',
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS release_guards;
