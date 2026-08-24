-- +goose Up
ALTER TABLE audit_entries ADD COLUMN schema_version text NOT NULL DEFAULT 'audit/v1';
ALTER TABLE audit_entries ADD COLUMN run_id text NOT NULL DEFAULT '';
ALTER TABLE audit_entries ADD COLUMN turn_id text NOT NULL DEFAULT '';
ALTER TABLE audit_entries ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0;
ALTER TABLE audit_entries ADD COLUMN budget_id text NOT NULL DEFAULT '';
ALTER TABLE audit_entries ADD COLUMN payload_digest text NOT NULL DEFAULT '';

CREATE INDEX audit_run_idx ON audit_entries(run_id, sequence) WHERE run_id <> '';
CREATE INDEX audit_turn_idx ON audit_entries(turn_id, sequence) WHERE turn_id <> '';

CREATE TABLE tool_invocations (
    invocation_id         text PRIMARY KEY,
    budget_id             text NOT NULL,
    request_key           text NOT NULL,
    run_id                text NOT NULL DEFAULT '',
    turn_id               text NOT NULL DEFAULT '',
    lease_epoch           bigint NOT NULL DEFAULT 0,
    tool_name             text NOT NULL,
    arguments_digest      text NOT NULL,
    result_digest         text NOT NULL DEFAULT '',
    error_digest          text NOT NULL DEFAULT '',
    budget_reservation_id text NOT NULL DEFAULT '',
    state                  text NOT NULL,
    outcome                text NOT NULL DEFAULT '',
    created_at             timestamptz NOT NULL DEFAULT now(),
    effect_started_at      timestamptz,
    settled_at             timestamptz,
    UNIQUE (budget_id, request_key),
    CHECK (state IN ('intent_recorded', 'effect_started', 'settled', 'outcome_unknown')),
    CHECK (outcome IN ('', 'succeeded', 'failed', 'denied', 'cancelled', 'outcome_unknown'))
);
CREATE INDEX tool_invocations_run_idx ON tool_invocations(run_id, created_at) WHERE run_id <> '';
CREATE INDEX tool_invocations_turn_idx ON tool_invocations(turn_id, created_at) WHERE turn_id <> '';

ALTER TABLE run_events RENAME TO legacy_run_events;

-- +goose Down
ALTER TABLE legacy_run_events RENAME TO run_events;
DROP TABLE IF EXISTS tool_invocations;
DROP INDEX IF EXISTS audit_turn_idx;
DROP INDEX IF EXISTS audit_run_idx;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS payload_digest;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS budget_id;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS lease_epoch;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS turn_id;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS run_id;
ALTER TABLE audit_entries DROP COLUMN IF EXISTS schema_version;
