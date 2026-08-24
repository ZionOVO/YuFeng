-- +goose Up
DELETE FROM model_inferences
WHERE model_group = 'fake'
  AND model_type = 'http'
  AND model_version = 'v1';

-- +goose Down
-- 删除的是固定分数生成的非业务记录，不允许在回滚时重新伪造。
