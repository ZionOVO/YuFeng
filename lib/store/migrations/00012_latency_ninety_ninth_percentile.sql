-- +goose Up
ALTER TABLE release_counters ADD COLUMN IF NOT EXISTS latency_p99_micros bigint NOT NULL DEFAULT 0;
ALTER TABLE release_guards ADD COLUMN IF NOT EXISTS last_p99_micros bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE release_counters DROP COLUMN IF EXISTS latency_p99_micros;
