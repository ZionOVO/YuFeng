-- +goose Up
ALTER TABLE runs ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

-- +goose Down
ALTER TABLE runs DROP COLUMN IF EXISTS updated_at;
