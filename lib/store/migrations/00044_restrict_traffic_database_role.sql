-- +goose Up
-- +goose StatementBegin
DO $restrict_traffic_role$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yufeng_traffic') THEN
        ALTER ROLE yufeng_traffic WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
        REVOKE CREATE ON SCHEMA traffic FROM yufeng_traffic;
        REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA traffic FROM yufeng_traffic;
        REVOKE ALL PRIVILEGES ON users, releases, grants, audit_entries FROM yufeng_traffic;
        GRANT USAGE ON SCHEMA traffic TO yufeng_traffic;
        GRANT SELECT, INSERT ON traffic.traffic_windows, traffic.traffic_window_receipts,
            traffic.review_candidates, traffic.review_case_outbox TO yufeng_traffic;
    END IF;
END
$restrict_traffic_role$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $restore_traffic_role$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'yufeng_traffic') THEN
        ALTER ROLE yufeng_traffic WITH INHERIT;
        GRANT USAGE ON SCHEMA traffic TO yufeng_traffic;
        GRANT SELECT, INSERT ON traffic.traffic_windows, traffic.traffic_window_receipts,
            traffic.review_candidates, traffic.review_case_outbox TO yufeng_traffic;
    END IF;
END
$restore_traffic_role$;
-- +goose StatementEnd
