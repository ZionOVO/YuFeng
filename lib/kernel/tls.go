package kernel

import (
	"errors"
	"strings"
)

// 生产拒绝的字面量默认凭证（architecture.md §13）。
const (
	DefaultAgentBootstrapToken = "dev-agent-bootstrap-token"
	DefaultUnitBootstrapToken  = "dev-unit-bootstrap-token"
)

// ValidateProductionTLS 在生产配置缺少证书或未显式开发开关时拒绝启动。
func ValidateProductionTLS(devInsecure bool, certFile, keyFile string) error {
	if devInsecure {
		return nil
	}
	if certFile == "" || keyFile == "" {
		return errors.New("production tls requires certificate and key or -dev-insecure")
	}
	return nil
}

// ValidateProductionMTLS 要求生产智能代理写路径启用相互传输层安全协议认证。
func ValidateProductionMTLS(devInsecure bool, clientCA string) error {
	if devInsecure {
		return nil
	}
	if strings.TrimSpace(clientCA) == "" {
		return errors.New("production agent write path requires mutual tls")
	}
	return nil
}

// ValidateProductionAgentBootstrap 生产拒绝未绑定 agent_id 的部署级共享引导令牌。
func ValidateProductionAgentBootstrap(devInsecure bool, token, boundAgentID string) error {
	if devInsecure {
		return nil
	}
	if strings.TrimSpace(boundAgentID) == "" {
		return errors.New("unbound agent bootstrap token is not allowed")
	}
	if strings.TrimSpace(token) == "" || token == DefaultAgentBootstrapToken {
		return errors.New("default agent bootstrap token is not allowed")
	}
	return nil
}

// ValidateProductionSecrets 生产拒绝空密码、字面量默认密码与未改的引导令牌。
func ValidateProductionSecrets(devInsecure bool, adminPass, agentBoot, unitBoot string) error {
	if devInsecure {
		return nil
	}
	if err := RejectDefaultPassword(adminPass); err != nil {
		return err
	}
	if strings.TrimSpace(agentBoot) == "" || agentBoot == DefaultAgentBootstrapToken {
		return errors.New("default agent bootstrap token is not allowed")
	}
	if strings.TrimSpace(unitBoot) == "" || unitBoot == DefaultUnitBootstrapToken {
		return errors.New("default unit bootstrap token is not allowed")
	}
	return nil
}

// RejectDefaultPassword 拒绝空口令与 architecture §13 列出的字面量。
func RejectDefaultPassword(password string) error {
	p := strings.ToLower(strings.TrimSpace(password))
	if p == "" {
		return errors.New("bootstrap admin password is required")
	}
	switch p {
	case "admin", "password", "changeme":
		return errors.New("default bootstrap password is not allowed")
	}
	return nil
}
