-- +goose Up
CREATE SCHEMA IF NOT EXISTS traffic;

CREATE TABLE IF NOT EXISTS traffic.traffic_windows (
    window_id               text NOT NULL,
    unit_id                 text NOT NULL,
    asset_id                text NOT NULL,
    window_start            timestamptz NOT NULL,
    window_end              timestamptz NOT NULL,
    generation_id           text NOT NULL DEFAULT '',
    generation_seq          bigint NOT NULL DEFAULT 0,
    policy_digest           text NOT NULL,
    request_count           bigint NOT NULL,
    critical_count          bigint NOT NULL,
    blocked_count           bigint NOT NULL,
    observed_count          bigint NOT NULL,
    incomplete_count        bigint NOT NULL,
    route_cells             jsonb NOT NULL DEFAULT '[]',
    other_cell              jsonb NOT NULL DEFAULT '{}',
    evidence_dropped_count  bigint NOT NULL DEFAULT 0,
    evidence_drop_reasons   jsonb NOT NULL DEFAULT '{}',
    review_mode             integer NOT NULL DEFAULT 0,
    ingested_at             timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (window_id, window_start)
) PARTITION BY RANGE (window_start);

CREATE TABLE IF NOT EXISTS traffic.traffic_windows_default
    PARTITION OF traffic.traffic_windows DEFAULT;
CREATE INDEX IF NOT EXISTS traffic_windows_asset_time_idx
    ON traffic.traffic_windows (asset_id, window_start DESC);

CREATE TABLE IF NOT EXISTS traffic.traffic_window_receipts (
    window_id       text PRIMARY KEY,
    window_start    timestamptz NOT NULL,
    payload_digest  text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS traffic.review_candidates (
    candidate_id         text NOT NULL,
    window_id            text NOT NULL,
    unit_id              text NOT NULL,
    asset_id             text NOT NULL,
    occurred_at          timestamptz NOT NULL,
    request_id           text NOT NULL DEFAULT '',
    method               text NOT NULL,
    route_template       text NOT NULL,
    risk_score           double precision NOT NULL,
    risk_reasons         jsonb NOT NULL DEFAULT '[]',
    evidence_projection  jsonb NOT NULL DEFAULT '{}',
    evidence_handle      text NOT NULL,
    evidence_digest      text NOT NULL,
    evidence_expires_at  timestamptz NOT NULL,
    generation_id        text NOT NULL DEFAULT '',
    generation_seq       bigint NOT NULL DEFAULT 0,
    baseline             boolean NOT NULL DEFAULT false,
    review_mode          integer NOT NULL DEFAULT 0,
    ingested_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (candidate_id, occurred_at)
) PARTITION BY RANGE (occurred_at);

CREATE TABLE IF NOT EXISTS traffic.review_candidates_default
    PARTITION OF traffic.review_candidates DEFAULT;
CREATE INDEX IF NOT EXISTS review_candidates_asset_risk_idx
    ON traffic.review_candidates (asset_id, risk_score DESC, occurred_at DESC);
CREATE INDEX IF NOT EXISTS review_candidates_window_idx
    ON traffic.review_candidates (window_id, occurred_at DESC);

CREATE TABLE IF NOT EXISTS traffic.review_case_outbox (
    candidate_id       text PRIMARY KEY,
    candidate_json     jsonb NOT NULL,
    state              text NOT NULL DEFAULT 'pending',
    attempts           integer NOT NULL DEFAULT 0,
    next_attempt_at    timestamptz NOT NULL DEFAULT now(),
    processed_at       timestamptz,
    last_error         text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('pending', 'processed'))
);
CREATE INDEX IF NOT EXISTS review_case_outbox_pending_idx
    ON traffic.review_case_outbox (state, next_attempt_at, created_at);

CREATE TABLE IF NOT EXISTS traffic.traffic_hourly_metrics (
    asset_id       text NOT NULL,
    bucket_start   timestamptz NOT NULL,
    metrics        jsonb NOT NULL,
    PRIMARY KEY (asset_id, bucket_start)
);

CREATE TABLE IF NOT EXISTS traffic.traffic_daily_metrics (
    asset_id       text NOT NULL,
    bucket_start   date NOT NULL,
    metrics        jsonb NOT NULL,
    PRIMARY KEY (asset_id, bucket_start)
);

CREATE TABLE investigation_cases (
    case_id            text PRIMARY KEY,
    module_id          text NOT NULL,
    asset_id           text NOT NULL REFERENCES assets(asset_id),
    cluster_id         text NOT NULL DEFAULT '',
    state              text NOT NULL,
    priority           integer NOT NULL CHECK (priority BETWEEN 0 AND 100),
    title              text NOT NULL,
    summary            text NOT NULL DEFAULT '',
    representatives    jsonb NOT NULL DEFAULT '[]',
    finding            jsonb NOT NULL DEFAULT '{}',
    shadow_release_id  text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    resolved_at        timestamptz,
    CHECK (state IN ('open', 'waiting_evidence_approval', 'queued', 'investigating', 'finding_ready', 'shadow_observing', 'resolved', 'failed', 'evidence_expired'))
);
CREATE INDEX investigation_cases_asset_state_idx
    ON investigation_cases (asset_id, state, priority DESC, updated_at DESC);
CREATE INDEX investigation_cases_module_idx
    ON investigation_cases (module_id, updated_at DESC);

CREATE TABLE case_activities (
    sequence       bigserial PRIMARY KEY,
    case_id        text NOT NULL REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    kind           text NOT NULL,
    ref_id         text NOT NULL DEFAULT '',
    summary        text NOT NULL DEFAULT '',
    occurred_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX case_activities_case_sequence_idx
    ON case_activities (case_id, sequence);

CREATE TABLE evidence_approvals (
    approval_id         text PRIMARY KEY,
    case_id             text NOT NULL REFERENCES investigation_cases(case_id) ON DELETE CASCADE,
    asset_id            text NOT NULL,
    unit_id             text NOT NULL,
    evidence_handles    jsonb NOT NULL,
    allowed_fields      jsonb NOT NULL,
    max_bytes           bigint NOT NULL CHECK (max_bytes BETWEEN 1 AND 40960),
    model_config_digest text NOT NULL,
    state               text NOT NULL DEFAULT 'pending',
    requested_by        text NOT NULL,
    decided_by          text NOT NULL DEFAULT '',
    expires_at          timestamptz NOT NULL,
    decided_at          timestamptz,
    consumed_at         timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('pending', 'approved', 'denied', 'consumed', 'expired'))
);
CREATE INDEX evidence_approvals_case_idx
    ON evidence_approvals (case_id, created_at DESC);

CREATE TABLE evidence_requests (
    request_id           text PRIMARY KEY,
    approval_id          text NOT NULL UNIQUE REFERENCES evidence_approvals(approval_id) ON DELETE CASCADE,
    case_id              text NOT NULL,
    asset_id             text NOT NULL,
    unit_id              text NOT NULL,
    evidence_handles     jsonb NOT NULL,
    allowed_fields       jsonb NOT NULL,
    max_bytes            bigint NOT NULL,
    model_config_digest  text NOT NULL,
    state                text NOT NULL DEFAULT 'pending',
    expires_at           timestamptz NOT NULL,
    submitted_at         timestamptz,
    sensitive_content_ref text NOT NULL DEFAULT '',
    CHECK (state IN ('pending', 'leased', 'submitted', 'expired'))
);
CREATE INDEX evidence_requests_unit_pending_idx
    ON evidence_requests (unit_id, state, expires_at);

ALTER TABLE model_generations ADD COLUMN sensitive boolean NOT NULL DEFAULT false;
ALTER TABLE model_generations ADD COLUMN approval_id text NOT NULL DEFAULT '';
ALTER TABLE model_generations ADD COLUMN case_id text NOT NULL DEFAULT '';
ALTER TABLE model_generations ADD COLUMN sensitive_content_digest text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN investigation_case_id text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN review_candidate_id text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN sensitive_content_ref text NOT NULL DEFAULT '';
ALTER TABLE session_messages ADD COLUMN attachments jsonb NOT NULL DEFAULT '[]';

-- +goose StatementBegin
DO $traffic_role_grants$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yufeng_traffic') THEN
        GRANT USAGE ON SCHEMA traffic TO yufeng_traffic;
        GRANT SELECT, INSERT ON traffic.traffic_windows, traffic.traffic_window_receipts,
            traffic.review_candidates, traffic.review_case_outbox TO yufeng_traffic;
    END IF;
END
$traffic_role_grants$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE session_messages DROP COLUMN IF EXISTS attachments;
ALTER TABLE work_items DROP COLUMN IF EXISTS sensitive_content_ref;
ALTER TABLE work_items DROP COLUMN IF EXISTS review_candidate_id;
ALTER TABLE work_items DROP COLUMN IF EXISTS investigation_case_id;
ALTER TABLE model_generations DROP COLUMN IF EXISTS sensitive_content_digest;
ALTER TABLE model_generations DROP COLUMN IF EXISTS case_id;
ALTER TABLE model_generations DROP COLUMN IF EXISTS approval_id;
ALTER TABLE model_generations DROP COLUMN IF EXISTS sensitive;
DROP TABLE IF EXISTS evidence_requests;
DROP TABLE IF EXISTS evidence_approvals;
DROP TABLE IF EXISTS case_activities;
DROP TABLE IF EXISTS investigation_cases;
DROP TABLE IF EXISTS traffic.traffic_daily_metrics;
DROP TABLE IF EXISTS traffic.traffic_hourly_metrics;
DROP TABLE IF EXISTS traffic.review_case_outbox;
DROP TABLE IF EXISTS traffic.review_candidates;
DROP TABLE IF EXISTS traffic.traffic_window_receipts;
DROP TABLE IF EXISTS traffic.traffic_windows;
DROP SCHEMA IF EXISTS traffic;
