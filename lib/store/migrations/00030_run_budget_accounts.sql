-- +goose Up
CREATE TABLE run_budget_accounts (
    budget_id                    text PRIMARY KEY,
    run_id                       text NOT NULL UNIQUE REFERENCES runs(run_id) ON DELETE CASCADE,
    state                        text NOT NULL DEFAULT 'active',
    max_steps                    bigint NOT NULL CHECK (max_steps > 0),
    max_model_calls              bigint NOT NULL CHECK (max_model_calls > 0),
    max_input_tokens             bigint NOT NULL CHECK (max_input_tokens > 0),
    max_output_tokens            bigint NOT NULL CHECK (max_output_tokens > 0),
    max_tool_calls               bigint NOT NULL CHECK (max_tool_calls > 0),
    max_tool_result_bytes        bigint NOT NULL CHECK (max_tool_result_bytes > 0),
    max_cost_microunits          bigint NOT NULL CHECK (max_cost_microunits > 0),
    max_active_milliseconds      bigint NOT NULL CHECK (max_active_milliseconds > 0),
    steps_used                   bigint NOT NULL DEFAULT 0,
    steps_reserved               bigint NOT NULL DEFAULT 0,
    model_calls_used             bigint NOT NULL DEFAULT 0,
    model_calls_reserved         bigint NOT NULL DEFAULT 0,
    input_tokens_used            bigint NOT NULL DEFAULT 0,
    input_tokens_reserved        bigint NOT NULL DEFAULT 0,
    output_tokens_used           bigint NOT NULL DEFAULT 0,
    output_tokens_reserved       bigint NOT NULL DEFAULT 0,
    tool_calls_used              bigint NOT NULL DEFAULT 0,
    tool_calls_reserved          bigint NOT NULL DEFAULT 0,
    tool_result_bytes_used       bigint NOT NULL DEFAULT 0,
    tool_result_bytes_reserved   bigint NOT NULL DEFAULT 0,
    cost_microunits_used         bigint NOT NULL DEFAULT 0,
    cost_microunits_reserved     bigint NOT NULL DEFAULT 0,
    active_milliseconds_used     bigint NOT NULL DEFAULT 0,
    active_started_at            timestamptz,
    execution_deadline           timestamptz NOT NULL,
    created_at                   timestamptz NOT NULL DEFAULT now(),
    updated_at                   timestamptz NOT NULL DEFAULT now(),
    CHECK (state IN ('active', 'completed', 'failed', 'cancelled', 'expired', 'outcome_unknown')),
    CHECK (steps_used >= 0 AND steps_reserved >= 0 AND model_calls_used >= 0 AND model_calls_reserved >= 0
        AND input_tokens_used >= 0 AND input_tokens_reserved >= 0 AND output_tokens_used >= 0 AND output_tokens_reserved >= 0
        AND tool_calls_used >= 0 AND tool_calls_reserved >= 0 AND tool_result_bytes_used >= 0 AND tool_result_bytes_reserved >= 0
        AND cost_microunits_used >= 0 AND cost_microunits_reserved >= 0 AND active_milliseconds_used >= 0),
    CHECK (steps_used + steps_reserved <= max_steps
        AND model_calls_used + model_calls_reserved <= max_model_calls
        AND input_tokens_used + input_tokens_reserved <= max_input_tokens
        AND output_tokens_used + output_tokens_reserved <= max_output_tokens
        AND tool_calls_used + tool_calls_reserved <= max_tool_calls
        AND tool_result_bytes_used + tool_result_bytes_reserved <= max_tool_result_bytes
        AND cost_microunits_used + cost_microunits_reserved <= max_cost_microunits)
);

CREATE TABLE run_budget_reservations (
    reservation_id          text PRIMARY KEY,
    budget_id               text NOT NULL REFERENCES run_budget_accounts(budget_id) ON DELETE CASCADE,
    kind                    text NOT NULL,
    request_key             text NOT NULL,
    state                   text NOT NULL DEFAULT 'reserved',
    steps                   bigint NOT NULL DEFAULT 0,
    model_calls             bigint NOT NULL DEFAULT 0,
    input_tokens            bigint NOT NULL DEFAULT 0,
    output_tokens           bigint NOT NULL DEFAULT 0,
    tool_calls              bigint NOT NULL DEFAULT 0,
    tool_result_bytes       bigint NOT NULL DEFAULT 0,
    cost_microunits         bigint NOT NULL DEFAULT 0,
    actual_steps            bigint NOT NULL DEFAULT 0,
    actual_model_calls      bigint NOT NULL DEFAULT 0,
    actual_input_tokens     bigint NOT NULL DEFAULT 0,
    actual_output_tokens    bigint NOT NULL DEFAULT 0,
    actual_tool_calls       bigint NOT NULL DEFAULT 0,
    actual_tool_result_bytes bigint NOT NULL DEFAULT 0,
    actual_cost_microunits  bigint NOT NULL DEFAULT 0,
    created_at              timestamptz NOT NULL DEFAULT now(),
    settled_at              timestamptz,
    CHECK (kind IN ('step', 'model', 'tool')),
    CHECK (state IN ('reserved', 'settled', 'outcome_unknown')),
    CHECK (steps >= 0 AND model_calls >= 0 AND input_tokens >= 0 AND output_tokens >= 0
        AND tool_calls >= 0 AND tool_result_bytes >= 0 AND cost_microunits >= 0
        AND actual_steps >= 0 AND actual_model_calls >= 0 AND actual_input_tokens >= 0
        AND actual_output_tokens >= 0 AND actual_tool_calls >= 0
        AND actual_tool_result_bytes >= 0 AND actual_cost_microunits >= 0),
    CHECK (actual_steps <= steps AND actual_model_calls <= model_calls
        AND actual_input_tokens <= input_tokens AND actual_output_tokens <= output_tokens
        AND actual_tool_calls <= tool_calls AND actual_tool_result_bytes <= tool_result_bytes
        AND actual_cost_microunits <= cost_microunits),
    UNIQUE (budget_id, kind, request_key)
);
CREATE INDEX run_budget_reservations_budget_idx ON run_budget_reservations(budget_id, state, created_at);

ALTER TABLE model_attempts ADD COLUMN IF NOT EXISTS budget_reservation_id text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE model_attempts DROP COLUMN IF EXISTS budget_reservation_id;
DROP TABLE IF EXISTS run_budget_reservations;
DROP TABLE IF EXISTS run_budget_accounts;
