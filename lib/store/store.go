package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"yufeng/lib/store/sqlc"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Config 是数据源配置。
type Config struct {
	DSN             string
	TrafficDSN      string
	MaxConns        int32
	TrafficMaxConns int32
	ConnectTimeout  time.Duration
}

// Store 是数据访问入口。查询与写路径统一从池取连接，禁止包级全局状态。
type Store struct {
	pool        *pgxpool.Pool
	trafficPool *pgxpool.Pool
	dsn         string
}

// Open 建立连接池并验证连通性。
func Open(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("store: DSN is empty")
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 8
	}
	if cfg.TrafficMaxConns <= 0 {
		cfg.TrafficMaxConns = 4
	}
	if cfg.TrafficDSN == "" {
		cfg.TrafficDSN = cfg.DSN
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = 5 * time.Second
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("store: parse DSN: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	trafficCfg, err := pgxpool.ParseConfig(cfg.TrafficDSN)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: parse traffic DSN: %w", err)
	}
	trafficCfg.MaxConns = cfg.TrafficMaxConns
	trafficCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	if trafficCfg.ConnConfig.RuntimeParams == nil {
		trafficCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	trafficCfg.ConnConfig.RuntimeParams["application_name"] = "yufeng-traffic"
	trafficPool, err := pgxpool.NewWithConfig(ctx, trafficCfg)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: create traffic pool: %w", err)
	}
	trafficPingCtx, trafficCancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer trafficCancel()
	if err := trafficPool.Ping(trafficPingCtx); err != nil {
		trafficPool.Close()
		pool.Close()
		return nil, fmt.Errorf("store: ping traffic pool: %w", err)
	}
	return &Store{pool: pool, trafficPool: trafficPool, dsn: cfg.DSN}, nil
}

// Migrate 应用内嵌 goose 迁移。goose v3 的入口是 database/sql，
// 因此迁移连接与业务连接池分离；迁移完成后立即关闭。
func (s *Store) Migrate(ctx context.Context) (err error) {
	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		return fmt.Errorf("store: open migrate db: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("store: close migrate db: %w", closeErr))
		}
	}()
	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: goose dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// Pool 暴露底层连接池，供中台服务直接执行结构化查询语言语句。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// TrafficPool 返回有独立连接上限的流量入账池；生产可通过独立 DSN 使用受限数据库角色。
func (s *Store) TrafficPool() *pgxpool.Pool { return s.trafficPool }

// SQLC 返回 sqlc 生成的查询集合。
// 新查询只进 query.sql，停止新增内联结构化查询语言；存量逐步迁移。
func (s *Store) SQLC() *sqlc.Queries { return sqlc.New(s.pool) }

// Close 关闭连接池。
func (s *Store) Close() {
	s.trafficPool.Close()
	s.pool.Close()
}
