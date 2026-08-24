-- +goose Up
ALTER TABLE agent_instructions
    ADD COLUMN retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count BETWEEN 0 AND 4),
    ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now();

CREATE INDEX agent_instructions_retryable_pending_idx
    ON agent_instructions (agent_id, next_attempt_at, created_at)
    WHERE status = 'pending';

-- +goose Down
DROP INDEX IF EXISTS agent_instructions_retryable_pending_idx;
ALTER TABLE agent_instructions
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS retry_count;
