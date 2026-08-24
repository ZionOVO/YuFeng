package brain

import (
	"context"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"yufeng/lib/store"
)

var brainPostgresTests struct {
	mu        sync.Mutex
	once      sync.Once
	dsn       string
	schemaDSN string
	schema    string
	admin     *pgxpool.Pool
	err       error
}

func TestMain(m *testing.M) {
	code := m.Run()
	cleanupBrainTestSchema()
	cleanupBrainYufengRunBuild()
	os.Exit(code)
}

// openTestStore 为数据库测试打开隔离后的干净连接池。
// 同一测试进程只迁移一次专用 schema；每个用例串行截断业务表，保留完整的独立状态语义。
func openTestStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("YUFENG_TEST_DSN")
	if dsn == "" {
		t.Skip("YUFENG_TEST_DSN 未设置，跳过 PostgreSQL 集成测试")
	}
	brainPostgresTests.mu.Lock()
	t.Cleanup(brainPostgresTests.mu.Unlock)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	brainPostgresTests.once.Do(func() {
		brainPostgresTests.err = initializeBrainTestSchema(ctx, dsn)
	})
	if brainPostgresTests.err != nil {
		t.Fatal(brainPostgresTests.err)
	}
	if brainPostgresTests.dsn != dsn {
		t.Fatal("YUFENG_TEST_DSN changed during lib/brain test process")
	}
	st, err := store.Open(ctx, store.Config{
		DSN:        brainPostgresTests.schemaDSN,
		TrafficDSN: os.Getenv("YUFENG_TRAFFIC_TEST_DSN"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := resetBrainTestSchema(ctx, st.Pool()); err != nil {
		st.Close()
		t.Fatal(err)
	}
	return st, ctx
}

func initializeBrainTestSchema(ctx context.Context, dsn string) error {
	schema := "brain_test_" + newTestSuffix()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		return err
	}
	schemaDSN, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		admin.Close()
		return err
	}
	bootstrap, err := store.Open(ctx, store.Config{
		DSN:        schemaDSN,
		TrafficDSN: os.Getenv("YUFENG_TRAFFIC_TEST_DSN"),
	})
	if err != nil {
		admin.Close()
		return err
	}
	if err := bootstrap.Migrate(ctx); err != nil {
		bootstrap.Close()
		admin.Close()
		return err
	}
	bootstrap.Close()
	brainPostgresTests.dsn = dsn
	brainPostgresTests.schemaDSN = schemaDSN
	brainPostgresTests.schema = schema
	brainPostgresTests.admin = admin
	return nil
}

func postgresDSNWithSearchPath(dsn, schema string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func resetBrainTestSchema(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 只截断上个用例实际写过的表，避免为近百张空表反复获取排他锁。
	if _, err := tx.Exec(ctx, `DO $reset$
	DECLARE
		table_name text;
		table_has_rows boolean;
		targets text := '';
	BEGIN
		FOR table_name IN
			SELECT c.relname
			FROM pg_class c
			JOIN pg_namespace n ON n.oid=c.relnamespace
			WHERE n.nspname=current_schema() AND c.relkind IN ('r','p')
			  AND c.relname<>'goose_db_version'
			  AND NOT EXISTS(SELECT 1 FROM pg_inherits i WHERE i.inhrelid=c.oid)
			ORDER BY c.relname
		LOOP
			EXECUTE format('SELECT EXISTS(SELECT 1 FROM %I.%I LIMIT 1)', current_schema(), table_name)
				INTO table_has_rows;
			IF table_has_rows THEN
				targets := targets || CASE WHEN targets='' THEN '' ELSE ',' END || format('%I.%I', current_schema(), table_name);
			END IF;
		END LOOP;
		IF targets<>'' THEN
			EXECUTE 'TRUNCATE TABLE ' || targets || ' RESTART IDENTITY CASCADE';
		END IF;
	END
	$reset$`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_onboarding(id,state) VALUES(1,$1)`, OnboardingStatePending); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_ledger_epochs(epoch_id,genesis_reason)
		VALUES('audit-epoch-review-closure','event and audit retention migration')`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cleanupBrainTestSchema() {
	admin := brainPostgresTests.admin
	schema := brainPostgresTests.schema
	if admin == nil || schema == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = admin.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
	admin.Close()
}

func cleanupBrainYufengRunBuild() {
	if brainYufengRunBuild.dir != "" {
		_ = os.RemoveAll(brainYufengRunBuild.dir)
	}
}
