-- +goose Up
ALTER TABLE deployment_onboarding
    ADD COLUMN IF NOT EXISTS dialect text NOT NULL DEFAULT 'MODEL_DIALECT_OPENAI_CHAT';

-- +goose Down
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS dialect;
