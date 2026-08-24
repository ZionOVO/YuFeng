-- +goose Up
CREATE TABLE sessions (
    session_id text PRIMARY KEY,
    title      text NOT NULL DEFAULT '',
    owner      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE session_messages (
    sequence    bigserial,
    session_id  text NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
    sender      text NOT NULL DEFAULT '',
    content     text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, sequence)
);

-- +goose Down
DROP TABLE IF EXISTS session_messages;
DROP TABLE IF EXISTS sessions;
