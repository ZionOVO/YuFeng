-- +goose Up
CREATE TABLE release_counters (
    unit_id               text NOT NULL,
    release_id            text NOT NULL,
    generation            bigint NOT NULL,
    mode                  text NOT NULL,
    requests_total        bigint NOT NULL DEFAULT 0,
    blocks_total          bigint NOT NULL DEFAULT 0,
    observe_total         bigint NOT NULL DEFAULT 0,
    canary_selected_total bigint NOT NULL DEFAULT 0,
    upstream_5xx_total    bigint NOT NULL DEFAULT 0,
    latency_micros_total  bigint NOT NULL DEFAULT 0,
    latency_samples       bigint NOT NULL DEFAULT 0,
    updated_at            timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (unit_id, release_id)
);

-- +goose Down
DROP TABLE IF EXISTS release_counters;
