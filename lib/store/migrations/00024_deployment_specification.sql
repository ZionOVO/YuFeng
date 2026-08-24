-- +goose Up
ALTER TABLE deployment_onboarding
  ADD COLUMN IF NOT EXISTS deployment_spec jsonb NOT NULL DEFAULT '{}';
ALTER TABLE deployment_onboarding
  ADD COLUMN IF NOT EXISTS deployment_spec_digest text NOT NULL DEFAULT '';

-- 监听计划先于首次单元注册签发；只有注册后的同身份单元能够通过服务读取。
ALTER TABLE unit_listen_plans
  DROP CONSTRAINT IF EXISTS unit_listen_plans_unit_id_fkey;

-- +goose Down
ALTER TABLE unit_listen_plans
  ADD CONSTRAINT unit_listen_plans_unit_id_fkey
  FOREIGN KEY (unit_id) REFERENCES units(unit_id) ON DELETE CASCADE;
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS deployment_spec_digest;
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS deployment_spec;
