-- +goose Up
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS pubkey_hash text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_tokens DROP COLUMN IF EXISTS pubkey_hash;
