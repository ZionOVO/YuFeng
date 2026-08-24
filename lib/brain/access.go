package brain

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	grantv1 "yufeng/proto/gen/grantv1"
)

// loadEffectiveAccess 按授予表即时展开；引导未完成时管理员额外展开账户工具。
func loadEffectiveAccess(ctx context.Context, pool *pgxpool.Pool, user *authv1.User) (*grantv1.EffectiveAccess, error) {
	out := &grantv1.EffectiveAccess{Tools: []string{}, Bindings: []*grantv1.BindingRef{}}
	if user == nil {
		return out, nil
	}
	snap, err := loadOnboarding(ctx, pool)
	if err != nil {
		return nil, err
	}
	toolSet := map[string]struct{}{}
	bindSet := map[string]*grantv1.BindingRef{}
	if !snap.completed() && user.Role == commonv1.UserRole_USER_ROLE_ADMIN {
		for _, t := range []string{"user.admin", "grant.write", "agent.manage", "console.read", "case.read", "case.manage", "evidence.approve", "worker.enroll", "worker.capacity.approve", "asset.create", "asset.update", "asset.delete", "asset.attach", "asset.detach"} {
			toolSet[t] = struct{}{}
		}
	} else {
		rows, err := pool.Query(ctx, `SELECT tools, bindings FROM grants
			WHERE subject_kind='user' AND subject_id=$1 AND (expires_at IS NULL OR expires_at > now())`, user.UserId)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var toolsRaw, bindsRaw []byte
			if err := rows.Scan(&toolsRaw, &bindsRaw); err != nil {
				return nil, err
			}
			var tools []string
			var binds []*grantv1.BindingRef
			if err := json.Unmarshal(toolsRaw, &tools); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(bindsRaw, &binds); err != nil {
				return nil, err
			}
			for _, t := range tools {
				if t != "" {
					toolSet[t] = struct{}{}
				}
			}
			for _, b := range binds {
				if b == nil || b.Id == "" || b.Id == "*" {
					continue
				}
				bindSet[b.Kind+"/"+b.Id] = &grantv1.BindingRef{Kind: b.Kind, Id: b.Id}
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	for t := range toolSet {
		out.Tools = append(out.Tools, t)
	}
	sort.Strings(out.Tools)
	for _, b := range bindSet {
		out.Bindings = append(out.Bindings, b)
	}
	sort.Slice(out.Bindings, func(i, j int) bool {
		if out.Bindings[i].Kind != out.Bindings[j].Kind {
			return out.Bindings[i].Kind < out.Bindings[j].Kind
		}
		return out.Bindings[i].Id < out.Bindings[j].Id
	})
	return out, nil
}

// accessScope 是 EffectiveAccess 的查找结构。
type accessScope struct {
	tools    map[string]bool
	assets   map[string]bool
	units    map[string]bool
	releases map[string]bool
}

func scopeFromAccess(a *grantv1.EffectiveAccess) accessScope {
	s := accessScope{
		tools:    map[string]bool{},
		assets:   map[string]bool{},
		units:    map[string]bool{},
		releases: map[string]bool{},
	}
	if a == nil {
		return s
	}
	for _, t := range a.Tools {
		s.tools[t] = true
	}
	for _, b := range a.Bindings {
		if b == nil || b.Id == "" {
			continue
		}
		switch b.Kind {
		case "unit":
			s.units[b.Id] = true
		case "release":
			s.releases[b.Id] = true
		default:
			s.assets[b.Id] = true
		}
	}
	return s
}

func (s accessScope) hasTool(name string) bool { return s.tools[name] }

func (s accessScope) assetIDs() []string {
	out := make([]string, 0, len(s.assets))
	for id := range s.assets {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s accessScope) coversAsset(id string) bool { return id != "" && s.assets[id] }

func (s accessScope) coversAllAssets(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		if !s.coversAsset(id) {
			return false
		}
	}
	return true
}

func (s accessScope) coversRelease(id string, assetIDs []string) bool {
	if id != "" && s.releases[id] {
		return true
	}
	return s.coversAllAssets(assetIDs)
}

func (s accessScope) emptyObjects() bool {
	return len(s.assets)+len(s.units)+len(s.releases) == 0
}

func objectDenied() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("permission_denied"))
}
