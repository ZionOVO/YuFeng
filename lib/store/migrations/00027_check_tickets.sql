-- +goose Up
CREATE TABLE check_tickets (
    event_id          text PRIMARY KEY REFERENCES events(event_id),
    generation_id     text NOT NULL DEFAULT '',
    generation_seq    bigint NOT NULL DEFAULT 0,
    status            text NOT NULL,
    ticket            jsonb NOT NULL DEFAULT '{}',
    ticket_digest     text NOT NULL DEFAULT '',
    forward_policy    text NOT NULL DEFAULT '',
    quarantine_reason text NOT NULL DEFAULT '',
    created_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (status IN ('ready', 'quarantined')),
    CHECK (
        (status = 'ready' AND ticket_digest <> '' AND quarantine_reason = '') OR
        (status = 'quarantined' AND ticket_digest = '' AND quarantine_reason <> '')
    )
);

-- +goose StatementBegin
CREATE FUNCTION reject_check_ticket_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'check tickets are immutable';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER check_tickets_immutable
    BEFORE UPDATE OR DELETE ON check_tickets
    FOR EACH ROW EXECUTE FUNCTION reject_check_ticket_mutation();

-- +goose Down
DROP TRIGGER IF EXISTS check_tickets_immutable ON check_tickets;
DROP FUNCTION IF EXISTS reject_check_ticket_mutation();
DROP TABLE IF EXISTS check_tickets;
