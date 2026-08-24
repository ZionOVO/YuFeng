package kernel

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnimplementedPrimitive 表示初次配置引导拒绝运行时约束、冷补丁或未知执行原语。
var ErrUnimplementedPrimitive = errors.New("unimplemented primitive")

// RejectOnboardingRuntimeConstraints 拒绝防火墙、命令解释器和其它未交付的执行原语，不得伪造成功。
func RejectOnboardingRuntimeConstraints(values ...string) error {
	for _, raw := range values {
		v := strings.ToLower(strings.TrimSpace(raw))
		if v == "" {
			continue
		}
		if strings.Contains(v, "nftables") || strings.Contains(v, "iptables") ||
			strings.Contains(v, "shell") || strings.Contains(v, "seccomp") ||
			strings.Contains(v, "ebpf") || v == "unknown" || strings.HasPrefix(v, "sh ") ||
			strings.Contains(v, "sh -c") {
			return fmt.Errorf("%w: %s", ErrUnimplementedPrimitive, v)
		}
		return fmt.Errorf("%w: %s", ErrUnimplementedPrimitive, v)
	}
	return nil
}
