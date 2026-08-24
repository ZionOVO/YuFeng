-- +goose Up
CREATE TABLE agent_turn_tool_schemas (
    turn_id       text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    tool_name     text NOT NULL,
    version       text NOT NULL,
    artifact_id   text NOT NULL,
    schema_digest text NOT NULL,
    pinned_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (turn_id, tool_name)
);

CREATE TABLE agent_turn_skills (
    turn_id        text NOT NULL REFERENCES agent_turns(turn_id) ON DELETE CASCADE,
    skill_id       text NOT NULL,
    version        text NOT NULL,
    artifact_id    text NOT NULL,
    content_digest text NOT NULL,
    skill_ref      text NOT NULL,
    loaded_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (turn_id, skill_id)
);

-- +goose Down
DROP TABLE IF EXISTS agent_turn_skills;
DROP TABLE IF EXISTS agent_turn_tool_schemas;
