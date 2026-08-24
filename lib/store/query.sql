-- name: CountAssets :one
SELECT count(*) FROM assets;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: GetUserByUsername :one
SELECT user_id, username, display_name, role, state, created_at, updated_at, last_login_at, password_hash
FROM users WHERE username = $1;

-- name: ListRecentEvents :many
SELECT event_id, unit_id, asset_id, request_id, occurred_at, source, kind, verdict, payload
FROM events ORDER BY occurred_at DESC LIMIT $1;

-- name: ListActiveReleasesForUnit :many
SELECT r.release_id, r.artifact, r.state, r.canary_percent, ua.asset_id, r.updated_at
FROM releases r
JOIN release_assets ra ON ra.release_id = r.release_id
JOIN unit_assets ua ON ua.asset_id = ra.asset_id
WHERE ua.unit_id = $1 AND r.state IN ('shadow','canary','enforce')
ORDER BY r.updated_at;
