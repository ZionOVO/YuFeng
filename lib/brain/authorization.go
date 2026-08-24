package brain

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// RoleDefaultTools 是角色默认 Tools 模板。只能再收窄，不能靠改角色名放宽。
func RoleDefaultTools(role commonv1.UserRole) []string {
	switch role {
	case commonv1.UserRole_USER_ROLE_VIEWER:
		return []string{"console.read", "case.read"}
	case commonv1.UserRole_USER_ROLE_OPERATOR:
		return []string{"console.read", "case.read", "case.manage", "evidence.approve", "govern.propose", "govern.gate", "govern.start_shadow", "run.create"}
	case commonv1.UserRole_USER_ROLE_ADMIN:
		return []string{"console.read", "case.read", "case.manage", "evidence.approve", "worker.enroll", "worker.capacity.approve", "agent.manage", "user.admin", "grant.write", "asset.create", "asset.update", "asset.delete", "asset.attach", "asset.detach"}
	default:
		return nil
	}
}

func isAccountTool(tool string) bool {
	return tool == "user.admin" || tool == "grant.write" || tool == "catalog.manage" || tool == "worker.enroll"
}

func isAssetAdminTool(tool string) bool {
	switch tool {
	case "asset.create", "asset.update", "asset.delete", "asset.attach", "asset.detach":
		return true
	default:
		return false
	}
}

// requireAssetAdmin 资产增删改（含绑定单元）只许 USER_ROLE_ADMIN。
func requireAssetAdmin(user *authv1.User) error {
	if user == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing user"))
	}
	if user.Role != commonv1.UserRole_USER_ROLE_ADMIN {
		return grantMissingError()
	}
	return nil
}

// authorizeWrite 以授予表为准；角色模板不得挡住已授工具。
func authorizeWrite(ctx context.Context, pool *pgxpool.Pool, user *authv1.User, tool, bindKind, bindID string, demo bool) error {
	if user == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing user"))
	}
	if err := requireUserGrant(ctx, pool, user.UserId, tool, bindKind, bindID); err == nil {
		return nil
	} else if connect.CodeOf(err) != connect.CodePermissionDenied {
		return err
	}
	snap, err := loadOnboarding(ctx, pool)
	if err != nil {
		return err
	}
	if !snap.completed() && user.Role == commonv1.UserRole_USER_ROLE_ADMIN && (isAccountTool(tool) || isAssetAdminTool(tool)) {
		return nil
	}
	if demo && claimsAllows(RoleDefaultTools(user.Role), tool) {
		return nil
	}
	return grantMissingError()
}

// authorizeWriteAssets 要求授予覆盖工具以及完整资产列表；缺任一资产即拒绝。
func authorizeWriteAssets(ctx context.Context, pool *pgxpool.Pool, user *authv1.User, tool string, assetIDs []string, demo bool) error {
	if len(assetIDs) == 0 {
		return grantMissingError()
	}
	for _, id := range assetIDs {
		if err := authorizeWrite(ctx, pool, user, tool, "asset", id, demo); err != nil {
			return err
		}
	}
	return nil
}
