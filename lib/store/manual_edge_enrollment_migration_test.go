package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"
)

func TestManualEdgeEnrollmentMigrationPreservesDeploymentSpecification(t *testing.T) {
	dsn := os.Getenv("YUFENG_TEST_DSN")
	if dsn == "" {
		t.Skip("YUFENG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := "manual_edge_migration_" + migrationTestSuffix(t)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Errorf("drop migration test schema: %v", err)
		}
	})
	schemaDSN, err := migrationDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", schemaDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close migration database: %v", err)
		}
	}()
	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 45); err != nil {
		t.Fatal(err)
	}

	const (
		unitID      = "dataset-edge"
		assetID     = "dataset-asset"
		generation  = "dataset-generation"
		digest      = "sha256:dataset-specification"
		listen      = ":18080"
		upstream    = "http://dataset-upstream:8080"
		trafficKey  = "dataset-http"
		profileID   = "http-threat/PVM/gpvm-e9eceef3"
		trustedCIDR = "10.20.0.0/16"
	)
	if _, err := db.ExecContext(ctx, `INSERT INTO units(unit_id,kind,contract_version) VALUES($1,'edge','v1')`, unitID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO assets(asset_id,display_name) VALUES($1,'Dataset API')`, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO unit_assets(unit_id,asset_id,relation,is_primary) VALUES($1,$2,'protects',true)`, unitID, assetID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO asset_generations(generation_id,asset_id,generation_seq,envelope,signed)
		VALUES($1,$2,7,'{}'::jsonb,true)`, generation, assetID); err != nil {
		t.Fatal(err)
	}
	specification := `{
		"unitId":"dataset-edge",
		"assetId":"dataset-asset",
		"posture":"INGRESS_POSTURE_REVERSE_PROXY",
		"trafficKey":"dataset-http",
		"reverseProxy":{"listenAddress":":18080","upstreamUrl":"http://dataset-upstream:8080"},
		"trustedProxyCidrs":["10.20.0.0/16"],
		"modelProfile":{"profileId":"http-threat/PVM/gpvm-e9eceef3","modelGroup":"http-threat","modelType":"PVM","modelVersion":"gpvm-e9eceef3"},
		"modelIngressWindow":{"maxItems":1024,"maxRetainedBytes":"67108864","maxQueueAge":"30s"}
	}`
	if _, err := db.ExecContext(ctx, `UPDATE deployment_onboarding SET state='ONBOARDING_STATE_EDGE_LIVE',
		deployment_spec=$1::jsonb,deployment_spec_digest=$2,local_unit_id=$3,local_asset_id=$4,
		expected_listen_plan_version=5,expected_generation_id=$5,expected_generation_seq=7 WHERE id=1`,
		specification, digest, unitID, assetID, generation); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, "migrations", 46); err != nil {
		t.Fatal(err)
	}

	var gotAsset, gotPosture, gotListen, gotUpstream, gotTrafficKey, gotDigest, gotModelSideID string
	var gotTrustedCIDR, gotProfileID string
	var gotListenVersion, gotGenerationSequence int64
	if err := db.QueryRowContext(ctx, `SELECT asset_id,posture,listen_address,upstream_url,traffic_key,
		trusted_proxy_cidrs->>0,model_profile->>'profileId',modelside_id,specification_digest,
		expected_listen_plan_version,expected_generation_seq FROM edge_enrollments WHERE unit_id=$1`, unitID).Scan(
		&gotAsset, &gotPosture, &gotListen, &gotUpstream, &gotTrafficKey, &gotTrustedCIDR, &gotProfileID,
		&gotModelSideID, &gotDigest, &gotListenVersion, &gotGenerationSequence); err != nil {
		t.Fatal(err)
	}
	if gotAsset != assetID || gotPosture != "INGRESS_POSTURE_REVERSE_PROXY" || gotListen != listen ||
		gotUpstream != upstream || gotTrafficKey != trafficKey || gotTrustedCIDR != trustedCIDR ||
		gotProfileID != profileID || gotModelSideID != unitID+"-modelside" || gotDigest != digest ||
		gotListenVersion != 5 || gotGenerationSequence != 7 {
		t.Fatalf("migrated enrollment asset=%q posture=%q listen=%q upstream=%q traffic=%q cidr=%q profile=%q modelside=%q digest=%q listenVersion=%d generationSequence=%d",
			gotAsset, gotPosture, gotListen, gotUpstream, gotTrafficKey, gotTrustedCIDR, gotProfileID,
			gotModelSideID, gotDigest, gotListenVersion, gotGenerationSequence)
	}
	var state string
	if err := db.QueryRowContext(ctx, `SELECT state FROM deployment_onboarding WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "ONBOARDING_STATE_MODEL_LIVE" {
		t.Fatalf("retired Edge onboarding state=%q", state)
	}
}

func migrationDSNWithSearchPath(dsn, schema string) (string, error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func migrationTestSuffix(t *testing.T) string {
	t.Helper()
	var value [6]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value[:])
}
