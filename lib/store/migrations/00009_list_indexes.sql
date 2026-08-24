-- +goose Up
-- 列表与反向查询索引：
--   releases(proposed_at) 支撑治理列表的按提案时间倒序（否则全表扫描）；
--   unit_assets(asset_id) 支撑发布 fan-out 与制品快照按资产反查单元
--   （主键 (unit_id, asset_id) 只覆盖 unit→asset 方向）。
CREATE INDEX releases_proposed_idx ON releases(proposed_at DESC);
CREATE INDEX unit_assets_asset_idx ON unit_assets(asset_id);

-- +goose Down
DROP INDEX unit_assets_asset_idx;
DROP INDEX releases_proposed_idx;
