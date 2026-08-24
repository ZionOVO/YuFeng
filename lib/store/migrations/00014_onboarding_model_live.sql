-- +goose Up
-- 探测成功且密钥未改的标记，FAILED 后仍能区分「模型已通」与「探测失败」。
ALTER TABLE deployment_onboarding ADD COLUMN IF NOT EXISTS model_live boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE deployment_onboarding DROP COLUMN IF EXISTS model_live;
