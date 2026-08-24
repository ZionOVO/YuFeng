-- +goose Up
CREATE TABLE managed_agent_profiles (
    agent_id      text PRIMARY KEY,
    display_name  text NOT NULL,
    kind          text NOT NULL DEFAULT 'traffic_review',
    state         text NOT NULL DEFAULT 'enabled',
    tools         jsonb NOT NULL DEFAULT '[]',
    bindings      jsonb NOT NULL DEFAULT '[]',
    created_by    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (kind = 'traffic_review'),
    CHECK (state IN ('enabled', 'disabled')),
    CHECK (jsonb_typeof(tools) = 'array'),
    CHECK (jsonb_typeof(bindings) = 'array')
);
CREATE INDEX managed_agent_profiles_state_updated_idx
    ON managed_agent_profiles (state, updated_at DESC, agent_id);

UPDATE grants
SET tools = tools || '["agent.manage"]'::jsonb
WHERE subject_kind = 'user'
  AND created_by = 'system'
  AND tools ? 'user.admin'
  AND NOT (tools ? 'agent.manage');

-- +goose Down
UPDATE grants
SET tools = (
    SELECT COALESCE(jsonb_agg(tool), '[]'::jsonb)
    FROM jsonb_array_elements(tools) tool
    WHERE tool <> '"agent.manage"'::jsonb
)
WHERE subject_kind = 'user' AND created_by = 'system' AND tools ? 'agent.manage';
DROP TABLE IF EXISTS managed_agent_profiles;
