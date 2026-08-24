-- +goose Up
CREATE TABLE release_feed (
    seq            bigserial PRIMARY KEY,
    unit_id        text NOT NULL,
    asset_id       text NOT NULL,
    release_id     text NOT NULL,
    mode           text NOT NULL DEFAULT 'shadow',
    canary_percent int NOT NULL DEFAULT 0,
    retired        boolean NOT NULL DEFAULT false,
    retire_reason  text NOT NULL DEFAULT '',
    artifact       jsonb NOT NULL,
    changed_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX release_feed_unit_seq_idx ON release_feed(unit_id, seq);

CREATE TABLE release_timeline (
    sequence        bigserial,
    release_id      text NOT NULL,
    from_state      text NOT NULL,
    to_state        text NOT NULL,
    actor           text NOT NULL,
    reason          text NOT NULL DEFAULT '',
    gate_report_ref text NOT NULL DEFAULT '',
    occurred_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, sequence)
);

CREATE TABLE deny_feedback (
    release_id text NOT NULL,
    event_id   text NOT NULL,
    actor      text NOT NULL DEFAULT '',
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (release_id, event_id)
);

-- +goose Down
DROP TABLE IF EXISTS deny_feedback;
DROP TABLE IF EXISTS release_timeline;
DROP TABLE IF EXISTS release_feed;
