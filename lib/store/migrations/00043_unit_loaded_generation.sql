-- +goose Up
ALTER TABLE units
    ADD COLUMN current_generation_id text NOT NULL DEFAULT '',
    ADD COLUMN current_generation_seq bigint NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE units
    DROP COLUMN IF EXISTS current_generation_seq,
    DROP COLUMN IF EXISTS current_generation_id;
