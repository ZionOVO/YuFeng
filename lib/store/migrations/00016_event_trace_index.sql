-- +goose Up
-- release_traces 整段进 btree 会在策略条数上来后撑破索引页，遥测整单回滚。
DROP INDEX IF EXISTS events_release_time_idx;

-- +goose Down
CREATE INDEX IF NOT EXISTS events_release_time_idx ON events((release_traces::text), occurred_at DESC);
