package brain

import (
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"

	"yufeng/lib/kernel"
)

// CapabilityHeader 是能力令牌请求头。
const CapabilityHeader = "X-Yufeng-Capability"

// DualTokens 是解析后的双令牌。
type DualTokens struct {
	Access     string
	Capability string
}

// ParseDualTokens 从请求头取出访问令牌与能力令牌。
// 任一缺失或 Bearer 格式错误返回 unauthenticated。
func ParseDualTokens(h http.Header) (DualTokens, error) {
	var zero DualTokens
	access := bearerToken(h.Get("Authorization"))
	cap := bearerToken(h.Get(CapabilityHeader))
	if access == "" || cap == "" {
		return zero, connect.NewError(connect.CodeUnauthenticated, errors.New("access token and capability token are required"))
	}
	if looksLikeSingleTokenMisplaced(h) {
		return zero, connect.NewError(connect.CodeUnauthenticated, errors.New("access token and capability token are required"))
	}
	return DualTokens{Access: access, Capability: cap}, nil
}

func looksLikeSingleTokenMisplaced(h http.Header) bool {
	return strings.TrimSpace(h.Get(CapabilityHeader)) == "" && strings.TrimSpace(h.Get("Authorization")) != ""
}

// BindDualTokens 校验能力令牌 azp 必须等于访问主体。
func BindDualTokens(accessSub string, claims kernel.Claims) error {
	if strings.TrimSpace(accessSub) == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("access token subject is empty"))
	}
	if strings.TrimSpace(claims.AuthorizedParty) == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("capability token azp is required"))
	}
	if accessSub != claims.AuthorizedParty {
		return connect.NewError(connect.CodePermissionDenied, errors.New("access token subject does not match capability azp"))
	}
	return nil
}
