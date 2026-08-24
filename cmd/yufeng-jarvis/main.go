// yufeng-jarvis 是独立编排智能代理进程，只经智能代理控制面与工具网关访问中台。
//
// 主循环在 agents/runtime。本文件只做装配。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/lib/kernel"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	"yufeng/proto/gen/toolgatewayv1/toolgatewayv1connect"
)

func main() {
	var (
		brain         = flag.String("brain", "http://127.0.0.1:9050", "中台地址")
		agentID       = flag.String("agent", "", "与 -jarvis-agent-id 同义")
		jarvisAgentID = flag.String("jarvis-agent-id", "jarvis-1", "贾维斯 Agent 标识")
		bootstrap     = flag.String("bootstrap-token", "", "仅开发使用的明文引导令牌")
		bootstrapFile = flag.String("bootstrap-token-file", "", "Agent 引导令牌文件")
		modelURL      = flag.String("model-url", "", "仅 -dev-insecure：HTTP 假出口；生产禁止")
		modelKey      = flag.String("model-key", "", "仅 -dev-insecure 的 HTTP 密钥")
		pubKey        = flag.String("public-key", "", "登记用公钥；空则从 state-dir 生成或读取本机密钥")
		devInsecure   = flag.Bool("dev-insecure", false, "允许明文中台或直接模型出口（仅本地开发）")
		tlsCA         = flag.String("tls-ca", os.Getenv("YUFENG_TLS_CA"), "中台 TLS 权威")
		tlsCert       = flag.String("tls-cert", os.Getenv("YUFENG_TLS_CERT"), "Agent 客户端证书")
		tlsKey        = flag.String("tls-key", os.Getenv("YUFENG_TLS_KEY"), "Agent 客户端私钥")
		stateDir      = flag.String("state-dir", firstNonEmpty(os.Getenv("YUFENG_STATE_DIR"), ""), "刷新令牌落盘目录；空则不持久化")
	)
	flag.Parse()
	bootstrapToken, err := loadJarvisBootstrap(*bootstrap, *bootstrapFile, *devInsecure)
	if err != nil {
		log.Fatal(err)
	}
	if err := validateJarvisTransport(*brain, *devInsecure, *tlsCA, *tlsCert, *tlsKey); err != nil {
		log.Fatal(err)
	}
	id := strings.TrimSpace(*jarvisAgentID)
	if strings.TrimSpace(*agentID) != "" {
		id = strings.TrimSpace(*agentID)
	}
	if id == "" {
		id = "jarvis-1"
	}
	if strings.TrimSpace(*pubKey) == "" {
		var err error
		*pubKey, err = loadOrCreateJarvisPublicKey(*stateDir)
		if err != nil {
			log.Fatalf("准备 Jarvis 本机密钥: %v", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if strings.HasPrefix(*brain, "https://") && (*tlsCA == "" || *tlsCert == "" || *tlsKey == "") {
		log.Fatal("https brain requires -tls-ca -tls-cert -tls-key")
	}
	hc := http.DefaultClient
	if *tlsCA != "" || *tlsCert != "" || *tlsKey != "" {
		c, err := kernel.HTTPClient(*tlsCA, *tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("tls client: %v", err)
		}
		hc = c
	}

	agentClient := agentv1connect.NewAgentControlServiceClient(hc, *brain)
	toolClient := toolgatewayv1connect.NewToolGatewayServiceClient(hc, *brain)

	sess := &runtime.AccessSession{}
	statePath := refreshFile(*stateDir)
	if err := establishSession(ctx, agentClient, id, bootstrapToken, *pubKey, statePath, sess); err != nil {
		log.Fatalf("接入 Agent: %v", err)
	}
	sess.Renew = func(ctx context.Context, refresh string) (string, string, error) {
		resp, err := agentClient.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: refresh}))
		if err != nil {
			return "", "", err
		}
		if err := saveRefresh(statePath, id, resp.Msg.RefreshToken); err != nil {
			log.Printf("persist refresh: %v", err)
		}
		return resp.Msg.AccessToken, resp.Msg.RefreshToken, nil
	}
	log.Printf("jarvis 已接入：%s", id)

	provider, err := selectProvider(providerConfig{
		DevInsecure: *devInsecure,
		ModelURL:    *modelURL,
		ModelKey:    *modelKey,
		BrainURL:    *brain,
		Token:       func() string { return sess.Access },
		Client:      hc,
	})
	if err != nil {
		log.Fatalf("model provider: %v", err)
	}
	if err := runtime.RunInstructions(ctx, provider, connectTools{client: toolClient}, connectInstr{client: agentClient, agentID: id, sess: sess}, sess); err != nil && !errorsIsCancel(err) {
		log.Fatalf("指令循环: %v", err)
	}
}

func loadJarvisBootstrap(plain, path string, devInsecure bool) (string, error) {
	plain, path = strings.TrimSpace(plain), strings.TrimSpace(path)
	if plain != "" && path != "" {
		return "", errors.New("bootstrap token and bootstrap token file are mutually exclusive")
	}
	if plain != "" {
		if !devInsecure {
			return "", errors.New("production jarvis requires bootstrap-token-file")
		}
		return plain, nil
	}
	if path == "" {
		return "", errors.New("bootstrap-token-file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("bootstrap token file is empty")
	}
	return value, nil
}

func validateJarvisTransport(brain string, devInsecure bool, tlsCA, tlsCert, tlsKey string) error {
	u, err := url.Parse(strings.TrimSpace(brain))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("brain must be an absolute http or https URL")
	}
	if !devInsecure && u.Scheme != "https" {
		return errors.New("production jarvis requires an https brain; use -dev-insecure only for local development")
	}
	if u.Scheme == "https" && (strings.TrimSpace(tlsCA) == "" || strings.TrimSpace(tlsCert) == "" || strings.TrimSpace(tlsKey) == "") {
		return errors.New("https brain requires -tls-ca -tls-cert -tls-key")
	}
	return nil
}

func establishSession(ctx context.Context, agentClient agentv1connect.AgentControlServiceClient, id, bootstrap, pubKey, statePath string, sess *runtime.AccessSession) error {
	wait := 200 * time.Millisecond
	const maxWait = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if tok := loadRefresh(statePath, id); tok != "" {
			resp, err := agentClient.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: tok}))
			if err == nil {
				sess.Access, sess.Refresh = resp.Msg.AccessToken, resp.Msg.RefreshToken
				if err := saveRefresh(statePath, id, sess.Refresh); err != nil {
					log.Printf("persist refresh: %v", err)
				}
				return nil
			}
			if connect.CodeOf(err) == connect.CodeUnauthenticated {
				return err
			}
			log.Printf("续期失败，将重试: %v", err)
		} else {
			reg, err := agentClient.RegisterAgent(ctx, connect.NewRequest(&agentv1.RegisterAgentRequest{
				AgentId: id, BootstrapToken: bootstrap, AgentPublicKey: pubKey,
			}))
			if err == nil {
				sess.Access, sess.Refresh = reg.Msg.AccessToken, reg.Msg.RefreshToken
				if err := saveRefresh(statePath, id, sess.Refresh); err != nil {
					log.Printf("persist refresh: %v", err)
				}
				return nil
			}
			if connect.CodeOf(err) == connect.CodeUnauthenticated || connect.CodeOf(err) == connect.CodePermissionDenied {
				return err
			}
			log.Printf("注册 Agent 失败，将重试: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait *= 2
		if wait > maxWait {
			wait = maxWait
		}
	}
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func errorsIsCancel(err error) bool {
	return err != nil && (err == context.Canceled || err == context.DeadlineExceeded)
}

type connectInstr struct {
	client  agentv1connect.AgentControlServiceClient
	agentID string
	sess    *runtime.AccessSession
}

// Poll 使用智能代理身份长轮询待处理指令。
func (c connectInstr) Poll(ctx context.Context) ([]*agentv1.AgentInstruction, error) {
	req := connect.NewRequest(&agentv1.PollInstructionsRequest{AgentId: c.agentID, LongPollSeconds: int32(kernel.AgentLongPollDefault.Seconds()), MaxInstructions: 1})
	req.Header().Set("Authorization", "Bearer "+c.sess.Access)
	resp, err := c.client.PollInstructions(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg.Instructions, nil
}

// Extend 延长指令租约并取得轮换后的能力令牌。
func (c connectInstr) Extend(ctx context.Context, instructionID, leaseID string, leaseEpoch int64, capabilityToken string) (*agentv1.ExtendInstructionLeaseResponse, error) {
	req := connect.NewRequest(&agentv1.ExtendInstructionLeaseRequest{InstructionId: instructionID, LeaseId: leaseID, LeaseEpoch: leaseEpoch})
	req.Header().Set("Authorization", "Bearer "+c.sess.Access)
	req.Header().Set("X-Yufeng-Capability", "Bearer "+capabilityToken)
	resp, err := c.client.ExtendInstructionLease(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Ack 提交与当前租约严格匹配的指令终态。
func (c connectInstr) Ack(ctx context.Context, instructionID, leaseID string, leaseEpoch int64, status, errStr string) error {
	req := connect.NewRequest(&agentv1.AckInstructionRequest{
		InstructionId: instructionID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, Status: status, Error: errStr,
	})
	req.Header().Set("Authorization", "Bearer "+c.sess.Access)
	_, err := c.client.AckInstruction(ctx, req)
	return err
}

type connectTools struct {
	client toolgatewayv1connect.ToolGatewayServiceClient
}

// Invoke 按当前会话形态携带访问令牌与能力令牌调用服务端工具。
func (c connectTools) Invoke(ctx context.Context, accessToken, capabilityToken, name, argsJSON string) (string, error) {
	req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: name, ArgsJson: argsJSON})
	if accessToken != "" {
		req.Header().Set("Authorization", "Bearer "+accessToken)
		req.Header().Set("X-Yufeng-Capability", "Bearer "+capabilityToken)
	} else {
		req.Header().Set("Authorization", "Bearer "+capabilityToken)
	}
	resp, err := c.client.InvokeTool(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Msg.ResultJson, nil
}
