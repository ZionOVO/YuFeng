-- +goose Up
CREATE TABLE asset_generation_settings (
    asset_id       text NOT NULL REFERENCES assets(asset_id) ON DELETE CASCADE,
    kind           text NOT NULL,
    payload        jsonb NOT NULL,
    payload_digest text NOT NULL DEFAULT '',
    updated_by     text NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (asset_id, kind)
);

ALTER TABLE investigation_cases
    ADD COLUMN resolution text NOT NULL DEFAULT '',
    ADD COLUMN automation_suppressed_reason text NOT NULL DEFAULT '',
    ADD COLUMN assigned_run_id text NOT NULL DEFAULT '',
    ADD COLUMN assigned_agent_config_digest text NOT NULL DEFAULT '';

CREATE TABLE case_feedback (
    feedback_id text PRIMARY KEY,
    case_id     text NOT NULL REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    asset_id    text NOT NULL REFERENCES assets(asset_id) ON DELETE CASCADE,
    resolution  text NOT NULL,
    note         text NOT NULL DEFAULT '',
    created_by   text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX case_feedback_asset_time_idx ON case_feedback(asset_id, created_at DESC);

CREATE TABLE shadow_candidate_jobs (
    case_id         text PRIMARY KEY REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    state           text NOT NULL DEFAULT 'pending',
    release_id      text NOT NULL DEFAULT '',
    finding_digest  text NOT NULL,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('pending','proposed','gated','shadow','failed'))
);
CREATE INDEX shadow_candidate_jobs_pending_idx
    ON shadow_candidate_jobs(state, next_attempt_at, created_at);

ALTER TABLE managed_agent_profiles
    ADD COLUMN execution_mode text NOT NULL DEFAULT 'ephemeral_run',
    ADD COLUMN config_digest text NOT NULL DEFAULT '',
    ADD COLUMN tombstoned_at timestamptz;
UPDATE managed_agent_profiles SET state='disabled';
ALTER TABLE managed_agent_profiles DROP CONSTRAINT managed_agent_profiles_state_check;
ALTER TABLE managed_agent_profiles
    ADD CHECK (state IN ('enabled','disabled','tombstoned'));

ALTER TABLE runs
    ADD COLUMN agent_config_digest text NOT NULL DEFAULT '',
    ADD COLUMN case_id text NOT NULL DEFAULT '';
ALTER TABLE work_items
    ADD COLUMN agent_id text NOT NULL DEFAULT '',
    ADD COLUMN agent_config_digest text NOT NULL DEFAULT '';

ALTER TABLE evidence_requests DROP CONSTRAINT evidence_requests_approval_id_key;
ALTER TABLE evidence_requests
    ADD COLUMN lease_id text NOT NULL DEFAULT '',
    ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0,
    ADD COLUMN lease_deadline timestamptz,
    ADD COLUMN bundle_group_id text NOT NULL DEFAULT '',
    ADD COLUMN bundle_digest text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX evidence_requests_approval_unit_idx
    ON evidence_requests(approval_id, unit_id);

ALTER TABLE worker_enrollments
    ADD COLUMN activation_public_key text NOT NULL DEFAULT '',
    ADD COLUMN activation_public_key_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN approved_manifest_digest text NOT NULL DEFAULT '',
    ADD COLUMN version text NOT NULL DEFAULT '',
    ADD COLUMN memory_capacity_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN logical_cpu_capacity integer NOT NULL DEFAULT 0,
    ADD COLUMN sandbox_challenge_id text NOT NULL DEFAULT '';
ALTER TABLE workers
    ADD COLUMN approved_manifest_digest text NOT NULL DEFAULT '',
    ADD COLUMN sandbox_attested_at timestamptz;

CREATE TABLE worker_activation_bundles (
    bundle_ref       text PRIMARY KEY,
    enrollment_id    text NOT NULL REFERENCES worker_enrollments(enrollment_id) ON DELETE CASCADE,
    ciphertext       bytea NOT NULL,
    manifest_digest  text NOT NULL,
    expires_at       timestamptz NOT NULL,
    acknowledged_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (enrollment_id)
);

CREATE TABLE worker_sandbox_attestations (
    worker_id       text PRIMARY KEY REFERENCES workers(worker_id) ON DELETE CASCADE,
    challenge_id    text NOT NULL,
    manifest_digest text NOT NULL,
    passed_probes   jsonb NOT NULL DEFAULT '[]',
    signature       text NOT NULL,
    verified_at     timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE commands ADD COLUMN lease_epoch bigint NOT NULL DEFAULT 0;
CREATE TABLE command_step_receipts (
    command_id               text NOT NULL REFERENCES commands(command_id) ON DELETE CASCADE,
    step_index               integer NOT NULL,
    phase                    text NOT NULL,
    guard_digest             text NOT NULL DEFAULT '',
    receipt_ref              text NOT NULL DEFAULT '',
    compensation_receipt_ref text NOT NULL DEFAULT '',
    output_json              jsonb NOT NULL DEFAULT '{}',
    error                    text NOT NULL DEFAULT '',
    updated_at               timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (command_id, step_index)
);

-- 本次升级按产品决定开启新账本世代，不保留既有事件、票据、推理与审计正文。
TRUNCATE TABLE analysis_attempts, analysis_work_items, investigation_receipts, check_tickets, model_inferences;
ALTER TABLE check_tickets DROP CONSTRAINT check_tickets_event_id_fkey;
ALTER TABLE model_inferences DROP CONSTRAINT model_inferences_event_id_fkey;
DROP TABLE events;

CREATE TABLE event_receipts (
    event_id       text PRIMARY KEY,
    payload_digest text NOT NULL DEFAULT '',
    occurred_at    timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE events (
    event_id       text NOT NULL,
    unit_id        text NOT NULL DEFAULT '',
    asset_id       text NOT NULL DEFAULT '',
    request_id     text NOT NULL DEFAULT '',
    occurred_at    timestamptz NOT NULL,
    ingested_at    timestamptz NOT NULL DEFAULT now(),
    source         text NOT NULL DEFAULT '',
    kind           text NOT NULL,
    verdict        text NOT NULL,
    payload        jsonb NOT NULL,
    release_traces jsonb NOT NULL DEFAULT '[]',
    observation    text NOT NULL DEFAULT '',
    triage_reason  text NOT NULL DEFAULT '',
    cluster_id     text NOT NULL DEFAULT '',
    generation_seq bigint NOT NULL DEFAULT 0,
    payload_digest text NOT NULL DEFAULT '',
    PRIMARY KEY (event_id, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE TABLE events_default PARTITION OF events DEFAULT;
CREATE INDEX events_asset_time_idx ON events(asset_id, occurred_at DESC);
CREATE INDEX events_verdict_time_idx ON events(verdict, occurred_at DESC);
CREATE INDEX events_release_time_idx ON events((release_traces::text), occurred_at DESC);
ALTER TABLE check_tickets ADD CONSTRAINT check_tickets_event_receipt_fkey
    FOREIGN KEY (event_id) REFERENCES event_receipts(event_id);
ALTER TABLE model_inferences ADD CONSTRAINT model_inferences_event_receipt_fkey
    FOREIGN KEY (event_id) REFERENCES event_receipts(event_id);

DROP TABLE audit_entries;
CREATE TABLE audit_ledger_epochs (
    epoch_id       text PRIMARY KEY,
    started_at     timestamptz NOT NULL DEFAULT now(),
    genesis_reason text NOT NULL
);
INSERT INTO audit_ledger_epochs(epoch_id, genesis_reason)
VALUES ('audit-epoch-review-closure', 'event and audit retention migration');

CREATE TABLE audit_entries (
    sequence      bigserial,
    occurred_at   timestamptz NOT NULL DEFAULT now(),
    actor_type    text NOT NULL,
    actor_id      text NOT NULL,
    action        text NOT NULL,
    object_type   text NOT NULL,
    object_id     text NOT NULL DEFAULT '',
    details       jsonb NOT NULL DEFAULT '{}',
    previous_hash text NOT NULL,
    entry_hash    text NOT NULL,
    schema_version text NOT NULL DEFAULT 'audit/v1',
    run_id         text NOT NULL DEFAULT '',
    turn_id        text NOT NULL DEFAULT '',
    lease_epoch    bigint NOT NULL DEFAULT 0,
    budget_id      text NOT NULL DEFAULT '',
    payload_digest text NOT NULL DEFAULT '',
    PRIMARY KEY (sequence, occurred_at)
) PARTITION BY RANGE (occurred_at);
CREATE TABLE audit_entries_default PARTITION OF audit_entries DEFAULT;
CREATE INDEX audit_object_idx ON audit_entries(object_type, object_id, sequence DESC);
CREATE INDEX audit_run_idx ON audit_entries(run_id, sequence) WHERE run_id <> '';
CREATE INDEX audit_turn_idx ON audit_entries(turn_id, sequence) WHERE turn_id <> '';

CREATE TABLE audit_partition_anchors (
    partition_name text PRIMARY KEY,
    first_sequence bigint NOT NULL,
    last_sequence  bigint NOT NULL,
    previous_hash  text NOT NULL,
    last_hash      text NOT NULL,
    checkpoint_ref text NOT NULL,
    checkpoint_signature text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS audit_partition_anchors;
DROP TABLE IF EXISTS audit_entries;
DROP TABLE IF EXISTS audit_ledger_epochs;
CREATE TABLE audit_entries (
    sequence bigserial PRIMARY KEY, occurred_at timestamptz NOT NULL DEFAULT now(),
    actor_type text NOT NULL, actor_id text NOT NULL, action text NOT NULL,
    object_type text NOT NULL, object_id text NOT NULL DEFAULT '', details jsonb NOT NULL DEFAULT '{}',
    previous_hash text NOT NULL, entry_hash text NOT NULL, schema_version text NOT NULL DEFAULT 'audit/v1',
    run_id text NOT NULL DEFAULT '', turn_id text NOT NULL DEFAULT '', lease_epoch bigint NOT NULL DEFAULT 0,
    budget_id text NOT NULL DEFAULT '', payload_digest text NOT NULL DEFAULT ''
);
CREATE INDEX audit_object_idx ON audit_entries(object_type, object_id, sequence DESC);

ALTER TABLE check_tickets DROP CONSTRAINT IF EXISTS check_tickets_event_receipt_fkey;
ALTER TABLE model_inferences DROP CONSTRAINT IF EXISTS model_inferences_event_receipt_fkey;
DROP TABLE IF EXISTS events;
DROP TABLE IF EXISTS event_receipts;
CREATE TABLE events (
    event_id text PRIMARY KEY, unit_id text NOT NULL DEFAULT '', asset_id text NOT NULL DEFAULT '',
    request_id text NOT NULL DEFAULT '', occurred_at timestamptz NOT NULL, ingested_at timestamptz NOT NULL DEFAULT now(),
    source text NOT NULL DEFAULT '', kind text NOT NULL, verdict text NOT NULL, payload jsonb NOT NULL,
    release_traces jsonb NOT NULL DEFAULT '[]', observation text NOT NULL DEFAULT '', triage_reason text NOT NULL DEFAULT '',
    cluster_id text NOT NULL DEFAULT '', generation_seq bigint NOT NULL DEFAULT 0, payload_digest text NOT NULL DEFAULT ''
);
ALTER TABLE check_tickets ADD FOREIGN KEY (event_id) REFERENCES events(event_id);
ALTER TABLE model_inferences ADD FOREIGN KEY (event_id) REFERENCES events(event_id);

DROP TABLE IF EXISTS command_step_receipts;
ALTER TABLE commands DROP COLUMN IF EXISTS lease_epoch;
DROP TABLE IF EXISTS worker_sandbox_attestations;
DROP TABLE IF EXISTS worker_activation_bundles;
ALTER TABLE workers DROP COLUMN IF EXISTS sandbox_attested_at;
ALTER TABLE workers DROP COLUMN IF EXISTS approved_manifest_digest;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS approved_manifest_digest;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS sandbox_challenge_id;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS logical_cpu_capacity;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS memory_capacity_bytes;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS version;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS activation_public_key_fingerprint;
ALTER TABLE worker_enrollments DROP COLUMN IF EXISTS activation_public_key;
DROP INDEX IF EXISTS evidence_requests_approval_unit_idx;
ALTER TABLE evidence_requests DROP COLUMN IF EXISTS bundle_digest;
ALTER TABLE evidence_requests DROP COLUMN IF EXISTS bundle_group_id;
ALTER TABLE evidence_requests DROP COLUMN IF EXISTS lease_deadline;
ALTER TABLE evidence_requests DROP COLUMN IF EXISTS lease_epoch;
ALTER TABLE evidence_requests DROP COLUMN IF EXISTS lease_id;
ALTER TABLE evidence_requests ADD UNIQUE (approval_id);
ALTER TABLE work_items DROP COLUMN IF EXISTS agent_config_digest;
ALTER TABLE work_items DROP COLUMN IF EXISTS agent_id;
ALTER TABLE runs DROP COLUMN IF EXISTS case_id;
ALTER TABLE runs DROP COLUMN IF EXISTS agent_config_digest;
ALTER TABLE managed_agent_profiles DROP COLUMN IF EXISTS tombstoned_at;
ALTER TABLE managed_agent_profiles DROP COLUMN IF EXISTS config_digest;
ALTER TABLE managed_agent_profiles DROP COLUMN IF EXISTS execution_mode;
DROP TABLE IF EXISTS shadow_candidate_jobs;
DROP TABLE IF EXISTS case_feedback;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS assigned_agent_config_digest;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS assigned_run_id;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS automation_suppressed_reason;
ALTER TABLE investigation_cases DROP COLUMN IF EXISTS resolution;
DROP TABLE IF EXISTS asset_generation_settings;
