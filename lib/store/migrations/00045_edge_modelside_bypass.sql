-- +goose Up
ALTER TABLE deployment_onboarding
    ADD COLUMN local_unit_id text NOT NULL DEFAULT '',
    ADD COLUMN expected_generation_id text NOT NULL DEFAULT '',
    ADD COLUMN expected_generation_seq bigint NOT NULL DEFAULT 0,
    ADD COLUMN expected_listen_plan_version bigint NOT NULL DEFAULT 0;

ALTER TABLE units
    ADD COLUMN current_listen_plan_version bigint NOT NULL DEFAULT 0;

ALTER TABLE model_inferences
    ADD COLUMN model_profile_digest text NOT NULL DEFAULT '',
    ADD COLUMN request_id text NOT NULL DEFAULT '',
    ADD COLUMN result_kind text NOT NULL DEFAULT '';

CREATE TABLE model_result_receipts (
    result_id             text PRIMARY KEY,
    payload_digest        text NOT NULL,
    modelside_id          text NOT NULL,
    request_id            text NOT NULL,
    unit_id               text NOT NULL REFERENCES units(unit_id),
    asset_id              text NOT NULL REFERENCES assets(asset_id),
    generation_id         text NOT NULL REFERENCES asset_generations(generation_id),
    generation_seq        bigint NOT NULL,
    model_profile_digest  text NOT NULL,
    result_kind           text NOT NULL CHECK (result_kind IN ('MODEL_ALERT', 'REVIEW_SAMPLE')),
    score                 double precision NOT NULL CHECK (score >= 0 AND score <= 1),
    method                text NOT NULL,
    route                 text NOT NULL,
    window_start          timestamptz,
    event_id              text NOT NULL REFERENCES event_receipts(event_id),
    case_id               text NOT NULL DEFAULT '',
    occurred_at           timestamptz NOT NULL,
    created_at            timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE modelside_identities (
    modelside_id        text PRIMARY KEY,
    unit_id             text NOT NULL REFERENCES units(unit_id),
    asset_id            text NOT NULL REFERENCES assets(asset_id),
    client_cert_sha256  text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    certificate_bound_at timestamptz,
    UNIQUE (unit_id, asset_id)
);
CREATE INDEX model_result_receipts_unit_window_idx
    ON model_result_receipts(unit_id, model_profile_digest, window_start)
    WHERE result_kind='REVIEW_SAMPLE';
CREATE TABLE model_review_representatives (
    unit_id              text NOT NULL REFERENCES units(unit_id),
    model_profile_digest text NOT NULL,
    window_start         timestamptz NOT NULL,
    method               text NOT NULL,
    route                text NOT NULL,
    result_id            text NOT NULL REFERENCES model_result_receipts(result_id),
    score                double precision NOT NULL CHECK (score >= 0 AND score <= 1),
    case_id              text NOT NULL DEFAULT '',
    updated_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (unit_id, model_profile_digest, window_start, method, route)
);

-- +goose Down
DROP TABLE IF EXISTS model_review_representatives;
DROP TABLE IF EXISTS model_result_receipts;
DROP TABLE IF EXISTS modelside_identities;
ALTER TABLE model_inferences
    DROP COLUMN IF EXISTS result_kind,
    DROP COLUMN IF EXISTS request_id,
    DROP COLUMN IF EXISTS model_profile_digest;
ALTER TABLE units DROP COLUMN IF EXISTS current_listen_plan_version;
ALTER TABLE deployment_onboarding
    DROP COLUMN IF EXISTS expected_listen_plan_version,
    DROP COLUMN IF EXISTS expected_generation_seq,
    DROP COLUMN IF EXISTS expected_generation_id,
    DROP COLUMN IF EXISTS local_unit_id;
