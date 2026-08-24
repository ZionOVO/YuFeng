-- +goose Up
-- 生产 payload_ref 是 cluster_id：已失败/已确认的研判不得挡住同聚类再入队。
DROP INDEX IF EXISTS agent_instructions_triage_event_idx;
CREATE UNIQUE INDEX IF NOT EXISTS agent_instructions_triage_open_idx
    ON agent_instructions (payload_ref)
    WHERE kind = 'EVENT_TRIAGE' AND status IN ('pending', 'leased');

-- +goose Down
DROP INDEX IF EXISTS agent_instructions_triage_open_idx;
CREATE UNIQUE INDEX IF NOT EXISTS agent_instructions_triage_event_idx
    ON agent_instructions (payload_ref)
    WHERE kind = 'EVENT_TRIAGE';
