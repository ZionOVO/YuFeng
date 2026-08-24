-- +goose Up
CREATE TABLE run_sagas (
    run_id            text PRIMARY KEY REFERENCES runs(run_id) ON DELETE CASCADE,
    work_id           text NOT NULL UNIQUE REFERENCES work_items(work_id) ON DELETE CASCADE,
    state             text NOT NULL DEFAULT 'pending',
    plan_digest       text NOT NULL DEFAULT '',
    cancel_requested  boolean NOT NULL DEFAULT false,
    cause             text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('pending', 'running', 'compensating', 'cancelling', 'ready', 'compensated', 'outcome_unknown'))
);

CREATE TABLE run_saga_steps (
    run_id                    text NOT NULL REFERENCES run_sagas(run_id) ON DELETE CASCADE,
    step_sequence             integer NOT NULL CHECK (step_sequence > 0),
    step_key                  text NOT NULL CHECK (step_key <> ''),
    action_replay             text NOT NULL,
    has_compensation          boolean NOT NULL DEFAULT false,
    compensation_replay       text NOT NULL,
    action_phase              text NOT NULL DEFAULT 'pending',
    action_effect_started     boolean NOT NULL DEFAULT false,
    compensation_phase        text NOT NULL DEFAULT 'pending',
    guard_digest              text NOT NULL DEFAULT '',
    action_receipt_ref        text NOT NULL DEFAULT '',
    compensation_receipt_ref  text NOT NULL DEFAULT '',
    error                     text NOT NULL DEFAULT '',
    updated_at                timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, step_sequence),
    UNIQUE (run_id, step_key),
    CHECK (action_replay IN ('safe', 'idempotent', 'never_replay')),
    CHECK (compensation_replay IN ('safe', 'idempotent', 'never_replay')),
    CHECK (action_phase IN ('pending', 'intent_recorded', 'effect_started', 'succeeded', 'failed', 'outcome_unknown')),
    CHECK (compensation_phase IN ('pending', 'intent_recorded', 'effect_started', 'compensated', 'failed', 'outcome_unknown'))
);

INSERT INTO run_sagas(run_id, work_id)
SELECT DISTINCT ON (run_id) run_id, work_id
FROM work_items
ORDER BY run_id, created_at, work_id
ON CONFLICT (run_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS run_saga_steps;
DROP TABLE IF EXISTS run_sagas;
