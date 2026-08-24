-- +goose Up
ALTER TABLE agents ADD COLUMN IF NOT EXISTS client_cert_hash text NOT NULL DEFAULT '';
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS client_cert_hash text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS client_cert_hash;
ALTER TABLE agents DROP COLUMN IF EXISTS client_cert_hash;
