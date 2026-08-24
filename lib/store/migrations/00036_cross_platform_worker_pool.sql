-- +goose Up
ALTER TABLE workers ADD COLUMN operating_system text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN architecture text NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN sandbox_capabilities jsonb NOT NULL DEFAULT '[]';
ALTER TABLE workers ADD COLUMN memory_capacity_bytes bigint NOT NULL DEFAULT 0;
ALTER TABLE workers ADD COLUMN logical_cpu_capacity integer NOT NULL DEFAULT 0;

CREATE TABLE worker_enrollments (
    enrollment_id          text PRIMARY KEY,
    worker_id              text NOT NULL,
    worker_kind            text NOT NULL CHECK (worker_kind IN ('RUN_SUPERVISOR', 'ANALYSIS_SCORER')),
    public_key             text NOT NULL,
    public_key_fingerprint text NOT NULL,
    hostname               text NOT NULL DEFAULT '',
    operating_system       text NOT NULL,
    architecture           text NOT NULL,
    sandbox_capabilities   jsonb NOT NULL DEFAULT '[]',
    certificate_request    text NOT NULL,
    state                  text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied', 'expired')),
    bindings               jsonb NOT NULL DEFAULT '[]',
    max_concurrency        integer NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 4),
    requested_at           timestamptz NOT NULL DEFAULT now(),
    decided_at             timestamptz,
    decided_by             text NOT NULL DEFAULT '',
    UNIQUE (worker_id, public_key_fingerprint)
);
CREATE INDEX worker_enrollments_state_idx ON worker_enrollments (state, requested_at);

CREATE TABLE worker_capacity_changes (
    change_id          text PRIMARY KEY,
    case_id            text NOT NULL REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    worker_id          text NOT NULL REFERENCES workers(worker_id) ON DELETE CASCADE,
    requested_by       text NOT NULL,
    requested_capacity integer NOT NULL CHECK (requested_capacity BETWEEN 1 AND 4),
    previous_capacity  integer NOT NULL CHECK (previous_capacity BETWEEN 1 AND 4),
    state              text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'approved', 'denied', 'expired')),
    expires_at         timestamptz NOT NULL,
    decided_by         text NOT NULL DEFAULT '',
    decided_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX worker_capacity_changes_worker_idx
    ON worker_capacity_changes (worker_id, state, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS worker_capacity_changes;
DROP TABLE IF EXISTS worker_enrollments;
ALTER TABLE workers DROP COLUMN IF EXISTS logical_cpu_capacity;
ALTER TABLE workers DROP COLUMN IF EXISTS memory_capacity_bytes;
ALTER TABLE workers DROP COLUMN IF EXISTS sandbox_capabilities;
ALTER TABLE workers DROP COLUMN IF EXISTS architecture;
ALTER TABLE workers DROP COLUMN IF EXISTS operating_system;
