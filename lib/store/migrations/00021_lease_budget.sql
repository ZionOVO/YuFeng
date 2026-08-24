-- +goose Up
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS budget_id text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS lease_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE agent_instructions ADD COLUMN IF NOT EXISTS budget_id text NOT NULL DEFAULT '';
ALTER TABLE agent_instructions ADD COLUMN IF NOT EXISTS lease_epoch bigint NOT NULL DEFAULT 0;

ALTER TABLE capability_budget ADD COLUMN IF NOT EXISTS budget_id text;
UPDATE capability_budget SET budget_id=jti WHERE budget_id IS NULL OR budget_id='';
ALTER TABLE capability_budget ALTER COLUMN budget_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS capability_budget_budget_id_idx ON capability_budget(budget_id);

CREATE TABLE capability_token_instances (
    jti          text PRIMARY KEY,
    budget_id    text NOT NULL,
    lease_id     text NOT NULL DEFAULT '',
    lease_epoch  bigint NOT NULL DEFAULT 0,
    expires_at   timestamptz NOT NULL,
    revoked      boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX capability_token_instances_budget_idx ON capability_token_instances(budget_id, lease_epoch);

-- +goose Down
DROP TABLE IF EXISTS capability_token_instances;
DROP INDEX IF EXISTS capability_budget_budget_id_idx;
ALTER TABLE capability_budget DROP COLUMN IF EXISTS budget_id;
ALTER TABLE agent_instructions DROP COLUMN IF EXISTS lease_epoch;
ALTER TABLE agent_instructions DROP COLUMN IF EXISTS budget_id;
ALTER TABLE work_items DROP COLUMN IF EXISTS lease_epoch;
ALTER TABLE work_items DROP COLUMN IF EXISTS budget_id;
