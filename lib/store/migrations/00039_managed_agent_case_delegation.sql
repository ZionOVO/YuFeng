-- +goose Up
ALTER TABLE investigation_cases
    ADD COLUMN assigned_agent_id text NOT NULL DEFAULT '',
    ADD COLUMN assigned_agent_display_name text NOT NULL DEFAULT '',
    ADD COLUMN agent_profile_snapshot jsonb NOT NULL DEFAULT '{}';

CREATE INDEX investigation_cases_assigned_agent_idx
    ON investigation_cases (assigned_agent_id, state, updated_at DESC)
    WHERE assigned_agent_id <> '';

CREATE TABLE case_delegation_outbox (
    case_id          text PRIMARY KEY REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    state            text NOT NULL DEFAULT 'pending',
    attempts         integer NOT NULL DEFAULT 0,
    next_attempt_at  timestamptz NOT NULL DEFAULT now(),
    dispatched_at    timestamptz,
    last_error       text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('pending', 'dispatched'))
);
CREATE INDEX case_delegation_outbox_pending_idx
    ON case_delegation_outbox (state, next_attempt_at, created_at);

ALTER TABLE runs
    ADD COLUMN agent_profile_id text NOT NULL DEFAULT '',
    ADD COLUMN agent_profile_snapshot jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE runs DROP COLUMN IF EXISTS agent_profile_snapshot;
ALTER TABLE runs DROP COLUMN IF EXISTS agent_profile_id;
DROP TABLE IF EXISTS case_delegation_outbox;
DROP INDEX IF EXISTS investigation_cases_assigned_agent_idx;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS agent_profile_snapshot;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS assigned_agent_display_name;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS assigned_agent_id;
