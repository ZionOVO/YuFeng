-- +goose Up
CREATE TABLE edge_enrollments (
    unit_id                       text PRIMARY KEY REFERENCES units(unit_id) ON DELETE RESTRICT,
    asset_id                      text NOT NULL REFERENCES assets(asset_id) ON DELETE RESTRICT,
    posture                       text NOT NULL,
    listen_address                text NOT NULL,
    upstream_url                  text NOT NULL DEFAULT '',
    traffic_key                   text NOT NULL,
    trusted_proxy_cidrs           jsonb NOT NULL DEFAULT '[]',
    model_profile                 jsonb NOT NULL,
    model_ingress_window          jsonb NOT NULL,
    modelside_id                  text NOT NULL UNIQUE,
    specification_digest          text NOT NULL,
    expected_listen_plan_version  bigint NOT NULL CHECK (expected_listen_plan_version > 0),
    expected_generation_id        text NOT NULL REFERENCES asset_generations(generation_id),
    expected_generation_seq       bigint NOT NULL CHECK (expected_generation_seq > 0),
    created_at                    timestamptz NOT NULL DEFAULT now(),
    updated_at                    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (asset_id, unit_id)
);
CREATE INDEX edge_enrollments_asset_idx ON edge_enrollments(asset_id, unit_id);

-- 已有单例部署规格必须在切换注册权威前完整进入资产域。
INSERT INTO edge_enrollments(
    unit_id, asset_id, posture, listen_address, upstream_url, traffic_key,
    trusted_proxy_cidrs, model_profile, model_ingress_window, modelside_id,
    specification_digest, expected_listen_plan_version, expected_generation_id,
    expected_generation_seq, created_at, updated_at)
SELECT
    d.local_unit_id,
    d.local_asset_id,
    d.deployment_spec->>'posture',
    COALESCE(
        d.deployment_spec #>> '{reverseProxy,listenAddress}',
        d.deployment_spec #>> '{reverse_proxy,listen_address}',
        d.deployment_spec #>> '{extAuthz,listenAddress}',
        d.deployment_spec #>> '{ext_authz,listen_address}',
        ''
    ),
    COALESCE(
        d.deployment_spec #>> '{reverseProxy,upstreamUrl}',
        d.deployment_spec #>> '{reverse_proxy,upstream_url}',
        ''
    ),
    COALESCE(d.deployment_spec->>'trafficKey', d.deployment_spec->>'traffic_key', ''),
    COALESCE(d.deployment_spec->'trustedProxyCidrs', d.deployment_spec->'trusted_proxy_cidrs', '[]'::jsonb),
    COALESCE(d.deployment_spec->'modelProfile', d.deployment_spec->'model_profile', '{}'::jsonb),
    COALESCE(d.deployment_spec->'modelIngressWindow', d.deployment_spec->'model_ingress_window', '{}'::jsonb),
    d.local_unit_id || '-modelside',
    d.deployment_spec_digest,
    d.expected_listen_plan_version,
    d.expected_generation_id,
    d.expected_generation_seq,
    d.updated_at,
    d.updated_at
FROM deployment_onboarding d
JOIN units u ON u.unit_id=d.local_unit_id
JOIN assets a ON a.asset_id=d.local_asset_id
JOIN asset_generations g ON g.generation_id=d.expected_generation_id AND g.asset_id=d.local_asset_id
WHERE d.id=1
  AND d.deployment_spec_digest<>''
  AND d.local_unit_id<>''
  AND d.local_asset_id<>''
  AND d.expected_listen_plan_version>0
  AND d.expected_generation_seq>0
ON CONFLICT (unit_id) DO NOTHING;

-- 旧引导状态不再由 Edge 心跳推进；已有状态降级为模型已探测。
UPDATE deployment_onboarding
SET state='ONBOARDING_STATE_MODEL_LIVE', updated_at=now()
WHERE state='ONBOARDING_STATE_EDGE_LIVE';

-- +goose Down
DROP TABLE IF EXISTS edge_enrollments;
