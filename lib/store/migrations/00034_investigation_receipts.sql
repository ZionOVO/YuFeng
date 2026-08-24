-- +goose Up
ALTER TABLE work_items ADD COLUMN investigation_event_id text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN investigation_ticket_digest text NOT NULL DEFAULT '';
ALTER TABLE work_items ADD COLUMN investigation_cluster_id text NOT NULL DEFAULT '';

CREATE TABLE investigation_receipts (
    run_id        text PRIMARY KEY REFERENCES runs(run_id) ON DELETE CASCADE,
    work_id       text NOT NULL UNIQUE REFERENCES work_items(work_id) ON DELETE CASCADE,
    event_id      text NOT NULL REFERENCES check_tickets(event_id),
    ticket_digest text NOT NULL,
    status        text NOT NULL,
    receipt       jsonb NOT NULL,
    error_code    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (status IN ('succeeded', 'failed', 'cancelled', 'timeout'))
);

-- +goose Down
DROP TABLE IF EXISTS investigation_receipts;
ALTER TABLE work_items DROP COLUMN IF EXISTS investigation_cluster_id;
ALTER TABLE work_items DROP COLUMN IF EXISTS investigation_ticket_digest;
ALTER TABLE work_items DROP COLUMN IF EXISTS investigation_event_id;
