-- +goose Up
-- 引导部署恰好一行；模型密钥进凭据槽，不明文进普通列。

CREATE TABLE IF NOT EXISTS deployment_onboarding (
    id smallint PRIMARY KEY CHECK (id = 1),
    state text NOT NULL DEFAULT 'ONBOARDING_STATE_PENDING',
    local_asset_id text NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE deployment_onboarding ALTER COLUMN state SET DEFAULT 'ONBOARDING_STATE_PENDING';
ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS base_url text NOT NULL DEFAULT '';
ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS model text NOT NULL DEFAULT '';
ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS credential_slots (
    slot_id text PRIMARY KEY,
    kind text NOT NULL,
    secret_hash text NOT NULL DEFAULT '',
    secret_ciphertext bytea,
    secret_hint text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO deployment_onboarding (id, state)
VALUES (1, 'ONBOARDING_STATE_PENDING')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS credential_slots;
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS last_error;
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS model;
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS base_url;
DROP TABLE IF EXISTS deployment_onboarding;
