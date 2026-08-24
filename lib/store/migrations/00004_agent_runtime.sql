-- +goose Up
CREATE TABLE agents (
    agent_id          text PRIMARY KEY,
    refresh_token_hash text NOT NULL DEFAULT '',
    role              text NOT NULL DEFAULT 'worker',
    public_key        text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agent_tokens (
    token_hash text PRIMARY KEY,
    agent_id   text NOT NULL REFERENCES agents(agent_id) ON DELETE CASCADE,
    kind       text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agent_instructions (
    instruction_id   text PRIMARY KEY,
    agent_id         text NOT NULL,
    kind             text NOT NULL,
    payload_ref      text NOT NULL DEFAULT '',
    status           text NOT NULL DEFAULT 'pending',
    lease_id         text NOT NULL DEFAULT '',
    lease_expires_at timestamptz,
    capability_token text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    deadline         timestamptz NOT NULL DEFAULT now() + interval '1 hour',
    acked_at         timestamptz
);
CREATE INDEX agent_instructions_pending_idx ON agent_instructions(agent_id, status, created_at);

CREATE TABLE runs (
    run_id      text PRIMARY KEY,
    state       text NOT NULL DEFAULT 'pending',
    role        text NOT NULL DEFAULT 'worker',
    plan_ref    text NOT NULL DEFAULT '',
    toolset     jsonb NOT NULL DEFAULT '[]',
    budget      text NOT NULL DEFAULT '',
    ttl         text NOT NULL DEFAULT '',
    bindings    jsonb NOT NULL DEFAULT '[]',
    created_by  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    deadline    timestamptz,
    error       text NOT NULL DEFAULT ''
);

CREATE TABLE work_items (
    work_id         text PRIMARY KEY,
    run_id          text NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    worker_id       text NOT NULL DEFAULT '',
    lease_id        text NOT NULL DEFAULT '',
    lease_deadline  timestamptz,
    capability_token text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'pending',
    result_ref      text NOT NULL DEFAULT '',
    receipt         text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX work_items_pending_idx ON work_items(status, created_at);

CREATE TABLE run_events (
    sequence    bigserial,
    run_id      text NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    kind        text NOT NULL,
    payload_ref text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, sequence)
);

-- +goose Down
DROP TABLE IF EXISTS run_events;
DROP TABLE IF EXISTS work_items;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS agent_instructions;
DROP TABLE IF EXISTS agent_tokens;
DROP TABLE IF EXISTS agents;
