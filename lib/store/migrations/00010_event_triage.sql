-- +goose Up
ALTER TABLE agent_instructions ADD COLUMN IF NOT EXISTS ack_error text NOT NULL DEFAULT '';

-- 同一事件只允许入队一条研判指令（谓词 5）。
CREATE UNIQUE INDEX IF NOT EXISTS agent_instructions_triage_event_idx
    ON agent_instructions (payload_ref)
    WHERE kind = 'EVENT_TRIAGE';

-- +goose Down
DROP INDEX IF EXISTS agent_instructions_triage_event_idx;
ALTER TABLE agent_instructions DROP COLUMN IF EXISTS ack_error;
