package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ValidateRestrictedTrafficRole 验证流量池使用独立非特权角色且只有规定表的读写权限。
// 生产中台完成迁移后必须调用；任何额外治理权限都应阻止启动。
func ValidateRestrictedTrafficRole(ctx context.Context, governancePool, trafficPool *pgxpool.Pool) error {
	if governancePool == nil || trafficPool == nil {
		return fmt.Errorf("store: database pool is nil")
	}
	var governanceRole, trafficRole string
	if err := governancePool.QueryRow(ctx, `SELECT current_user`).Scan(&governanceRole); err != nil {
		return fmt.Errorf("store: read governance database role: %w", err)
	}
	if err := trafficPool.QueryRow(ctx, `SELECT current_user`).Scan(&trafficRole); err != nil {
		return fmt.Errorf("store: read traffic database role: %w", err)
	}
	if trafficRole == "" || trafficRole == governanceRole {
		return fmt.Errorf("store: traffic database role must differ from governance role")
	}

	var canLogin, inherits, superuser, createRole, createDatabase, replication, bypassRowSecurity bool
	if err := governancePool.QueryRow(ctx, `SELECT rolcanlogin, rolinherit, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls
		FROM pg_roles WHERE rolname=$1`, trafficRole).Scan(
		&canLogin, &inherits, &superuser, &createRole, &createDatabase, &replication, &bypassRowSecurity,
	); err != nil {
		return fmt.Errorf("store: read traffic database role attributes: %w", err)
	}
	if !canLogin || inherits || superuser || createRole || createDatabase || replication || bypassRowSecurity {
		return fmt.Errorf("store: traffic database role has privileged attributes")
	}

	var schemaUsage, schemaCreate bool
	if err := governancePool.QueryRow(ctx, `SELECT
		has_schema_privilege($1, 'traffic', 'USAGE'),
		has_schema_privilege($1, 'traffic', 'CREATE')`, trafficRole).Scan(&schemaUsage, &schemaCreate); err != nil {
		return fmt.Errorf("store: read traffic schema privileges: %w", err)
	}
	if !schemaUsage || schemaCreate {
		return fmt.Errorf("store: traffic database role has invalid traffic schema privileges")
	}

	for _, table := range []string{
		"traffic.traffic_windows",
		"traffic.traffic_window_receipts",
		"traffic.review_candidates",
		"traffic.review_case_outbox",
	} {
		privileges, err := readTablePrivileges(ctx, governancePool, trafficRole, table)
		if err != nil {
			return err
		}
		if !privileges.selectRows || !privileges.insertRows || privileges.updateRows || privileges.deleteRows || privileges.truncateRows || privileges.references || privileges.trigger {
			return fmt.Errorf("store: traffic database role has invalid privileges on %s", table)
		}
	}

	var governanceSchema string
	if err := governancePool.QueryRow(ctx, `SELECT current_schema()`).Scan(&governanceSchema); err != nil {
		return fmt.Errorf("store: read governance schema: %w", err)
	}
	for _, table := range []string{"users", "releases", "grants", "audit_entries"} {
		relation := pgx.Identifier{governanceSchema, table}.Sanitize()
		privileges, err := readTablePrivileges(ctx, governancePool, trafficRole, relation)
		if err != nil {
			return err
		}
		if privileges.any() {
			return fmt.Errorf("store: traffic database role can access governance table %s", relation)
		}
	}
	return nil
}

type tablePrivileges struct {
	selectRows   bool
	insertRows   bool
	updateRows   bool
	deleteRows   bool
	truncateRows bool
	references   bool
	trigger      bool
}

func (p tablePrivileges) any() bool {
	return p.selectRows || p.insertRows || p.updateRows || p.deleteRows || p.truncateRows || p.references || p.trigger
}

func readTablePrivileges(ctx context.Context, pool *pgxpool.Pool, role, relation string) (tablePrivileges, error) {
	var privileges tablePrivileges
	err := pool.QueryRow(ctx, `SELECT
		has_table_privilege($1, $2, 'SELECT'),
		has_table_privilege($1, $2, 'INSERT'),
		has_table_privilege($1, $2, 'UPDATE'),
		has_table_privilege($1, $2, 'DELETE'),
		has_table_privilege($1, $2, 'TRUNCATE'),
		has_table_privilege($1, $2, 'REFERENCES'),
		has_table_privilege($1, $2, 'TRIGGER')`, role, relation).Scan(
		&privileges.selectRows,
		&privileges.insertRows,
		&privileges.updateRows,
		&privileges.deleteRows,
		&privileges.truncateRows,
		&privileges.references,
		&privileges.trigger,
	)
	if err != nil {
		return tablePrivileges{}, fmt.Errorf("store: read table privileges for %s: %w", relation, err)
	}
	return privileges, nil
}
