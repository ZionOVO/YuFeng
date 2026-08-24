-- +goose Up
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS source_cursor jsonb NOT NULL DEFAULT '{}';
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS budget_id text NOT NULL DEFAULT '';
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS checkpoint jsonb NOT NULL DEFAULT '{}';
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS next_item_sequence bigint NOT NULL DEFAULT 1;
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS next_input_sequence bigint NOT NULL DEFAULT 1;
ALTER TABLE agent_turns ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();
UPDATE agent_turns SET state='running' WHERE state='working';

ALTER TABLE work_items ADD COLUMN IF NOT EXISTS turn_id text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN IF NOT EXISTS plan_digest text NOT NULL DEFAULT '';

CREATE TABLE agent_steps (
    step_id        text PRIMARY KEY,
    turn_id        text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    step_sequence  bigint NOT NULL,
    state          text NOT NULL DEFAULT 'running',
    created_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz,
    UNIQUE (turn_id, step_sequence)
);

CREATE TABLE agent_items (
    item_id         text PRIMARY KEY,
    turn_id         text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    step_id         text NOT NULL DEFAULT '',
    item_sequence   bigint NOT NULL,
    kind            text NOT NULL,
    content_ref     text NOT NULL DEFAULT '',
    content_digest  text NOT NULL DEFAULT '',
    payload          jsonb NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (turn_id, item_sequence)
);
CREATE INDEX agent_items_turn_idx ON agent_items(turn_id, item_sequence);

CREATE TABLE agent_turn_inputs (
    turn_id         text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    input_sequence  bigint NOT NULL,
    kind            text NOT NULL,
    content_ref     text NOT NULL,
    received_at     timestamptz NOT NULL DEFAULT now(),
    consumed_at     timestamptz,
    PRIMARY KEY (turn_id, input_sequence)
);

CREATE TABLE model_generations (
    generation_id       text PRIMARY KEY,
    turn_id             text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    step_id             text NOT NULL REFERENCES agent_steps(step_id) ON DELETE CASCADE,
    request_digest      text NOT NULL,
    request_payload     jsonb NOT NULL,
    context_manifest    jsonb NOT NULL,
    generation_limits   jsonb NOT NULL,
    state               text NOT NULL DEFAULT 'pending',
    accepted_attempt_id text NOT NULL DEFAULT '',
    accepted_response   jsonb NOT NULL DEFAULT '{}',
    created_at          timestamptz NOT NULL DEFAULT now(),
    completed_at        timestamptz
);
CREATE INDEX model_generations_turn_idx ON model_generations(turn_id, created_at);

CREATE TABLE model_attempts (
    attempt_id          text PRIMARY KEY,
    generation_id       text NOT NULL REFERENCES model_generations(generation_id) ON DELETE CASCADE,
    attempt_sequence    bigint NOT NULL,
    lease_epoch         bigint NOT NULL,
    state               text NOT NULL,
    request_digest      text NOT NULL,
    response_candidate  jsonb NOT NULL DEFAULT '{}',
    usage               jsonb NOT NULL DEFAULT '{}',
    error_code          text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    effect_started_at   timestamptz,
    settled_at          timestamptz,
    UNIQUE (generation_id, attempt_sequence)
);
CREATE INDEX model_attempts_generation_idx ON model_attempts(generation_id, attempt_sequence);

-- +goose Down
DROP TABLE IF EXISTS model_attempts;
DROP TABLE IF EXISTS model_generations;
DROP TABLE IF EXISTS agent_turn_inputs;
DROP TABLE IF EXISTS agent_items;
DROP TABLE IF EXISTS agent_steps;
ALTER TABLE work_items DROP COLUMN IF EXISTS plan_digest;
ALTER TABLE work_items DROP COLUMN IF EXISTS turn_id;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS updated_at;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS next_input_sequence;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS next_item_sequence;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS checkpoint;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS budget_id;
ALTER TABLE agent_turns DROP COLUMN IF EXISTS source_cursor;
