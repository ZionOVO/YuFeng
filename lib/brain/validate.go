package brain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	agentv1 "yufeng/proto/gen/agentv1"
	eventv1 "yufeng/proto/gen/eventv1"
	governv1 "yufeng/proto/gen/governv1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"

	"yufeng/lib/kernel"
	"yufeng/lib/observability"
)

// ValidateInterceptor 在业务逻辑前拒绝非法 Connect 请求。
// 手写覆盖：提案存活时间、金丝雀百分比、事件标识/时间/资产、注册令牌、工具参数。
func ValidateInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if msg, ok := req.Any().(proto.Message); ok {
				if err := validateIncoming(msg); err != nil {
					return nil, connect.NewError(connect.CodeInvalidArgument, err)
				}
			}
			return next(ctx, req)
		}
	}
}

func validateIncoming(msg proto.Message) error {
	switch m := msg.(type) {
	case *governv1.ProposeArtifactRequest:
		if m.Ttl != nil {
			d := m.Ttl.AsDuration()
			if d < kernel.TTLMin || d > kernel.TTLMax {
				return errors.New("ttl out of range")
			}
		}
	case *governv1.PromoteCanaryRequest:
		if m.CanaryPercent != 0 && (m.CanaryPercent < kernel.CanaryPercentMin || m.CanaryPercent > kernel.CanaryPercentMax) {
			return errors.New("canary_percent out of range")
		}
	case *eventv1.Event:
		return validateEvent(m)
	case *agentv1.RegisterAgentRequest:
		if strings.TrimSpace(m.GetAgentId()) == "" || m.GetBootstrapToken() == "" {
			return errors.New("agent_id and bootstrap_token are required")
		}
		if strings.TrimSpace(m.GetAgentPublicKey()) == "" {
			return errors.New("agent_public_key is required")
		}
	case *toolgatewayv1.InvokeToolRequest:
		if strings.TrimSpace(m.GetToolName()) == "" {
			return errors.New("tool_name is required")
		}
		if s := strings.TrimSpace(m.GetArgsJson()); s != "" && !json.Valid([]byte(s)) {
			return errors.New("args_json is not valid json")
		}
	}
	return nil
}

func validateEvent(e *eventv1.Event) error {
	if e == nil || strings.TrimSpace(e.GetId()) == "" {
		return errors.New("event id is required")
	}
	if e.GetOccurredAt() == nil {
		return errors.New("occurred_at is required")
	}
	if strings.TrimSpace(e.GetAssetId()) == "" {
		return errors.New("asset_id is required")
	}
	return nil
}

func handlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(observability.TraceInterceptor(), ValidateInterceptor())}
}

// ValidateSchedulerConfig 生产缺门槛或非法门槛拒绝启动。
func ValidateSchedulerConfig(cfg SchedulerConfig, demo bool) error {
	if demo {
		return nil
	}
	if cfg.ShadowMinDuration <= 0 || cfg.CanaryMinDuration <= 0 {
		return errors.New("production scheduler duration gates are missing")
	}
	if cfg.ShadowMinRequests == 0 || cfg.CanaryMinRequests == 0 {
		return errors.New("production scheduler request gates are missing")
	}
	if cfg.CanaryPercent < kernel.CanaryPercentMin || cfg.CanaryPercent > kernel.CanaryPercentMax {
		return errors.New("illegal canary percent")
	}
	if cfg.ShadowMinDuration < time.Second || cfg.CanaryMinDuration < time.Second {
		return errors.New("illegal scheduler duration")
	}
	return nil
}
