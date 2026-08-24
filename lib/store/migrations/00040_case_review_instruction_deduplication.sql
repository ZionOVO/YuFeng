-- +goose Up
ALTER TABLE agent_instructions
    ADD COLUMN dedupe_key text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX agent_instructions_case_review_dedupe_idx
    ON agent_instructions (dedupe_key)
    WHERE kind = 'CASE_REVIEW' AND dedupe_key <> '';

-- +goose Down
DROP INDEX IF EXISTS agent_instructions_case_review_dedupe_idx;
ALTER TABLE agent_instructions DROP COLUMN IF EXISTS dedupe_key;
