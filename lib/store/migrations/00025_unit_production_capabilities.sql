-- +goose Up
ALTER TABLE units
    ADD COLUMN producer_capabilities jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN producer_health jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN posture text NOT NULL DEFAULT '',
    ADD COLUMN traffic_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE units
    DROP COLUMN traffic_key,
    DROP COLUMN posture,
    DROP COLUMN producer_health,
    DROP COLUMN producer_capabilities;
