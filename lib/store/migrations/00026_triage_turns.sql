-- +goose Up
ALTER TABLE triage_clusters
    ADD COLUMN IF NOT EXISTS version bigint NOT NULL DEFAULT 1;

CREATE TABLE agent_threads (
    thread_id   text PRIMARY KEY,
    source_kind text NOT NULL,
    source_ref  text NOT NULL,
    agent_id    text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_kind, source_ref, agent_id)
);

CREATE TABLE agent_turns (
    turn_id        text PRIMARY KEY,
    thread_id      text NOT NULL REFERENCES agent_threads(thread_id) ON DELETE CASCADE,
    source_version bigint NOT NULL,
    input_snapshot jsonb NOT NULL,
    state          text NOT NULL DEFAULT 'pending',
    output_ref     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),
    completed_at   timestamptz,
    UNIQUE (thread_id, source_version)
);

CREATE TABLE triage_decisions (
    decision_id     text PRIMARY KEY,
    turn_id         text NOT NULL UNIQUE REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    agent_id        text NOT NULL,
    decision_digest text NOT NULL,
    decision        jsonb NOT NULL,
    release_id      text NOT NULL DEFAULT '',
    result          jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    completed_at    timestamptz
);

ALTER TABLE agent_instructions
    ADD COLUMN IF NOT EXISTS turn_id text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_instructions DROP COLUMN IF EXISTS turn_id;
DROP TABLE IF EXISTS triage_decisions;
DROP TABLE IF EXISTS agent_turns;
DROP TABLE IF EXISTS agent_threads;
ALTER TABLE triage_clusters DROP COLUMN IF EXISTS version;
