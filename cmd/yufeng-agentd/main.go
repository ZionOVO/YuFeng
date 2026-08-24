// yufeng-agentd 是执行实例监督进程：领取工作项并孵化 yufeng-run。
// 本文件只做装配；领取、孵化与回执在 agents/runtime。
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

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/lib/kernel"

	agentv1 "yufeng/proto/gen/agentv1"
	"yufeng/proto/gen/agentv1/agentv1connect"
	modelv1 "yufeng/proto/gen/modelv1"
	"yufeng/proto/gen/modelv1/modelv1connect"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	"yufeng/proto/gen/toolgatewayv1/toolgatewayv1connect"
	workerv1 "yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

var version = "dev"

func main() {
	var (
		brain               = flag.String("brain", "http://127.0.0.1:9050", "中台地址")
		workerID            = flag.String("worker", "agentd-1", "Worker 标识")
		enroll              = flag.Bool("enroll", false, "生成本机密钥并提交待管理员核对的外部 worker 注册")
		activate            = flag.Bool("activate", false, "等待管理员批准并领取本机密钥加密的 worker 激活包")
		activationPack      = flag.String("activation-package", "", "一次性 worker 激活包；首次会话成功后删除")
		workerBootstrap     = flag.String("worker-bootstrap-token", os.Getenv("YUFENG_WORKER_BOOTSTRAP_TOKEN"), "绑定 RUN_SUPERVISOR 身份的一次性引导令牌")
		workerBootstrapFile = flag.String("worker-bootstrap-token-file", "", "绑定 RUN_SUPERVISOR 身份的一次性引导令牌文件")
		agentBootstrap      = flag.String("bootstrap-token", "dev-agent-bootstrap-token", "旧 Agent 引导令牌；仅 -dev-agent-compat")
		devAgentCompat      = flag.Bool("dev-agent-compat", false, "开发期使用旧 Agent 身份领取 run")
		devInsecure         = flag.Bool("dev-insecure", false, "允许连接明文中台（仅本地开发）")
		publicKey           = flag.String("public-key", "", "登记用公钥；外部客户端默认读取 state-dir/worker-public.pem")
		runBin              = flag.String("run", "", "yufeng-run 路径；空则同目录或 PATH")
		tlsCA               = flag.String("tls-ca", os.Getenv("YUFENG_TLS_CA"), "中台 TLS 权威")
		tlsCert             = flag.String("tls-cert", os.Getenv("YUFENG_TLS_CERT"), "Agent 客户端证书")
		tlsKey              = flag.String("tls-key", os.Getenv("YUFENG_TLS_KEY"), "Agent 客户端私钥")
		tlsSeedCert         = flag.String("tls-seed-cert", "", "首次启动时复制到私有状态目录的只读客户端证书")
		tlsSeedKey          = flag.String("tls-seed-key", "", "首次启动时复制到私有状态目录的只读客户端私钥")
		stateDir            = flag.String("state-dir", firstNonEmpty(os.Getenv("YUFENG_AGENTD_STATE_DIR"), "/var/lib/yufeng/agentd"), "run 监督身份刷新令牌目录")
	)
	flag.Parse()
	if strings.TrimSpace(*workerBootstrap) != "" && strings.TrimSpace(*workerBootstrapFile) != "" {
		log.Fatal("worker bootstrap token and token file are mutually exclusive")
	}
	if strings.TrimSpace(*workerBootstrapFile) != "" {
		raw, err := os.ReadFile(*workerBootstrapFile)
		if err != nil {
			log.Fatalf("读取 worker 引导令牌: %v", err)
		}
		*workerBootstrap = strings.TrimSpace(string(raw))
		if *workerBootstrap == "" {
			log.Fatal("worker bootstrap token file is empty")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *enroll {
		if !strings.HasPrefix(*brain, "https://") || strings.TrimSpace(*tlsCA) == "" {
			log.Fatal("worker enrollment requires an https brain and -tls-ca")
		}
		hc, err := kernel.HTTPClient(*tlsCA, "", "")
		if err != nil {
			log.Fatalf("tls client: %v", err)
		}
		if err := requestWorkerEnrollment(ctx, hc, *brain, *workerID, *stateDir); err != nil {
			log.Fatalf("提交 worker 注册: %v", err)
		}
		return
	}
	if strings.TrimSpace(*publicKey) == "" {
		raw, err := os.ReadFile(workerPublicKeyPath(*stateDir))
		if err != nil {
			log.Fatalf("读取 worker 公钥: %v", err)
		}
		*publicKey = string(raw)
	}
	activationPath := strings.TrimSpace(*activationPack)
	if *activate {
		if activationPath != "" {
			log.Fatal("worker activation retrieval cannot be combined with -activation-package")
		}
		if !strings.HasPrefix(*brain, "https://") || strings.TrimSpace(*tlsCA) == "" {
			log.Fatal("worker activation retrieval requires an https brain and -tls-ca")
		}
		var retrieve bool
		var err error
		activationPath, retrieve, err = resolveWorkerActivationState(*stateDir, *workerID)
		if err != nil {
			log.Fatalf("检查 worker 激活状态: %v", err)
		}
		if retrieve {
			hc, err := kernel.HTTPClient(*tlsCA, "", "")
			if err != nil {
				log.Fatalf("tls client: %v", err)
			}
			client := workerv1connect.NewWorkerServiceClient(hc, *brain)
			activationPath, err = retrieveWorkerActivationPackage(ctx, client, *stateDir)
			if err != nil {
				log.Fatalf("领取 worker 激活包: %v", err)
			}
		}
	}
	if activationPath != "" {
		if *devAgentCompat {
			log.Fatal("worker activation package is incompatible with dev agent compatibility")
		}
		if !strings.HasPrefix(*brain, "https://") || strings.TrimSpace(*tlsCA) == "" {
			log.Fatal("worker activation package requires an https brain and -tls-ca")
		}
		if strings.TrimSpace(*workerBootstrap) != "" || strings.TrimSpace(*tlsSeedCert) != "" || strings.TrimSpace(*tlsSeedKey) != "" {
			log.Fatal("worker activation package cannot be combined with bootstrap or tls seed flags")
		}
		bootstrap, err := prepareWorkerActivationPackage(activationPath, *stateDir, *workerID)
		if err != nil {
			log.Fatalf("准备 worker 激活包: %v", err)
		}
		*workerBootstrap = bootstrap
	}
	var registrationApproval workerRegistrationApproval
	if activationPath != "" {
		var err error
		registrationApproval, err = persistWorkerRegistrationApproval(*stateDir, activationPath)
		if err != nil {
			log.Fatalf("保存 worker 批准清单: %v", err)
		}
	} else {
		var err error
		registrationApproval, err = loadWorkerRegistrationApproval(*stateDir)
		if err != nil {
			log.Fatalf("读取 worker 批准清单: %v", err)
		}
	}
	if strings.HasPrefix(*brain, "https://") {
		if err := seedWorkerTLSMaterial(*stateDir, *tlsSeedCert, *tlsSeedKey); err != nil {
			log.Fatalf("准备 worker 客户端证书: %v", err)
		}
		if *tlsCert == "" {
			*tlsCert = workerClientCertificatePath(*stateDir)
		}
		if *tlsKey == "" {
			*tlsKey = workerClientKeyPath(*stateDir)
		}
	}
	if strings.HasPrefix(*brain, "https://") && (*tlsCA == "" || *tlsCert == "" || *tlsKey == "") {
		log.Fatal("https brain requires -tls-ca -tls-cert -tls-key")
	}
	if err := validateAgentdTransport(*brain, *devInsecure, *tlsCA, *tlsCert, *tlsKey); err != nil {
		log.Fatal(err)
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
	modelClient := modelv1connect.NewModelGatewayServiceClient(hc, *brain)
	workerClient := workerv1connect.NewWorkerServiceClient(hc, *brain)
	sess := &runtime.AccessSession{}

	statePath := workerRefreshFile(*stateDir)
	if *devAgentCompat {
		if err := establishCompatSession(ctx, agentClient, *workerID, *agentBootstrap, *publicKey, sess); err != nil {
			log.Fatalf("接入兼容 Agent: %v", err)
		}
	} else if err := establishWorkerSession(ctx, workerClient, *workerID, *workerBootstrap, *publicKey, statePath, sess); err != nil {
		log.Fatalf("接入 run 监督身份: %v", err)
	}
	if activationPath != "" {
		if err := acknowledgeWorkerActivation(ctx, workerClient, activationPath, sess.AccessToken()); err != nil {
			log.Fatalf("确认 worker 激活包: %v", err)
		}
		if err := consumeWorkerActivationState(activationPath, *stateDir); err != nil {
			log.Fatalf("销毁已消费的 worker 激活状态: %v", err)
		}
	}
	if err := registerRunWorker(ctx, workerClient, *workerID, *stateDir, sess.AccessToken(), registrationApproval); err != nil {
		log.Fatalf("注册 run 监督进程: %v", err)
	}
	if *devAgentCompat {
		sess.Renew = func(ctx context.Context, refresh string) (string, string, error) {
			resp, err := agentClient.RefreshAccessToken(ctx, connect.NewRequest(&agentv1.RefreshAccessTokenRequest{RefreshToken: refresh}))
			if err != nil {
				return "", "", err
			}
			return resp.Msg.AccessToken, resp.Msg.RefreshToken, nil
		}
	} else {
		sess.Renew = workerAccessRenewer(workerClient, *workerID, statePath, saveWorkerRefresh)
	}
	log.Printf("run 监督进程已注册：%s", *workerID)
	var maintain func(context.Context) error
	if !*devAgentCompat && strings.HasPrefix(*brain, "https://") {
		maintain = workerCertificateRenewer(hc, workerClient, *workerID, *tlsCert, *tlsKey, statePath, sess)
	}
	if err := runtime.RunWorker(ctx, connectWork{client: workerClient, workerID: *workerID, sess: sess}, connectTools{client: toolClient, models: modelClient, sess: sess}, sess, *runBin, maintain); err != nil && err != context.Canceled {
		log.Fatalf("工作循环: %v", err)
	}
}

func validateAgentdTransport(brain string, devInsecure bool, tlsCA, tlsCert, tlsKey string) error {
	u, err := url.Parse(strings.TrimSpace(brain))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("brain must be an absolute http or https URL")
	}
	if !devInsecure && u.Scheme != "https" {
		return errors.New("production agentd requires an https brain; use -dev-insecure only for local development")
	}
	if u.Scheme == "https" && (strings.TrimSpace(tlsCA) == "" || strings.TrimSpace(tlsCert) == "" || strings.TrimSpace(tlsKey) == "") {
		return errors.New("https brain requires -tls-ca -tls-cert -tls-key")
	}
	return nil
}

type connectWork struct {
	client   workerv1connect.WorkerServiceClient
	workerID string
	sess     *runtime.AccessSession
}

// Poll 使用工作负载身份领取一个待执行工作项。
func (c connectWork) Poll(ctx context.Context) (*workerv1.WorkItem, error) {
	var resp *connect.Response[workerv1.PollWorkResponse]
	err := c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.PollWorkRequest{WorkerId: c.workerID, LongPollSeconds: 20})
		req.Header().Set("Authorization", "Bearer "+access)
		var err error
		resp, err = c.client.PollWork(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp.Msg.Work, nil
}

// Complete 提交与当前租约标识和纪元严格匹配的成功回执。
func (c connectWork) Complete(ctx context.Context, workID, leaseID string, leaseEpoch int64, result, receipt string) error {
	return c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.CompleteWorkRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, ResultRef: result, Receipt: receipt})
		req.Header().Set("Authorization", "Bearer "+access)
		_, err := c.client.CompleteWork(ctx, req)
		return err
	})
}

// Fail 提交与当前租约严格匹配的失败终态。
func (c connectWork) Fail(ctx context.Context, workID, leaseID string, leaseEpoch int64, code, message string) error {
	return c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.FailWorkRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, ErrorCode: code, Message: message})
		req.Header().Set("Authorization", "Bearer "+access)
		_, err := c.client.FailWork(ctx, req)
		return err
	})
}

// Extend 延长工作租约并返回轮换后的能力令牌与取消状态。
func (c connectWork) Extend(ctx context.Context, workID, leaseID string, leaseEpoch int64) (runtime.LeaseExtension, error) {
	var resp *connect.Response[workerv1.ExtendLeaseResponse]
	err := c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.ExtendLeaseRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch})
		req.Header().Set("Authorization", "Bearer "+access)
		var err error
		resp, err = c.client.ExtendLease(ctx, req)
		return err
	})
	if err != nil {
		return runtime.LeaseExtension{}, err
	}
	return runtime.LeaseExtension{CapabilityToken: resp.Msg.CapabilityToken, CancelRequested: resp.Msg.CancelRequested}, nil
}

// Progress 记录当前工作阶段及其载荷引用。
func (c connectWork) Progress(ctx context.Context, workID, leaseID string, leaseEpoch int64, stage, payload string) error {
	return c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.ReportProgressRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch, Stage: stage, PayloadRef: payload})
		req.Header().Set("Authorization", "Bearer "+access)
		_, err := c.client.ReportProgress(ctx, req)
		return err
	})
}

// Saga 提交补偿事务计划或步骤回执，并返回服务端权威恢复快照。
func (c connectWork) Saga(ctx context.Context, workID, leaseID string, leaseEpoch int64, progress runtime.SagaProgress) (runtime.SagaSnapshot, error) {
	var resp *connect.Response[workerv1.ReportProgressResponse]
	err := c.sess.Call(ctx, "", func(access string) error {
		req := connect.NewRequest(&workerv1.ReportProgressRequest{WorkId: workID, LeaseId: leaseID, LeaseEpoch: leaseEpoch})
		if progress.Plan != nil {
			req.Msg.SagaPlan = runtime.SagaPlanToProto(*progress.Plan)
		}
		if progress.Receipt != nil {
			req.Msg.SagaReceipt = runtime.SagaReceiptToProto(*progress.Receipt)
		}
		req.Header().Set("Authorization", "Bearer "+access)
		var err error
		resp, err = c.client.ReportProgress(ctx, req)
		return err
	})
	if err != nil {
		return runtime.SagaSnapshot{}, err
	}
	return runtime.SagaSnapshotFromProto(resp.Msg.GetSagaSnapshot()), nil
}

type connectTools struct {
	client toolgatewayv1connect.ToolGatewayServiceClient
	models modelv1connect.ModelGatewayServiceClient
	sess   *runtime.AccessSession
}

// Generate 携带监督进程代持的双令牌调用统一模型网关。
func (c connectTools) Generate(ctx context.Context, accessToken, capabilityToken string, message *modelv1.GenerateRequest) (*modelv1.GenerateResponse, error) {
	var resp *connect.Response[modelv1.GenerateResponse]
	err := c.sess.Call(ctx, accessToken, func(access string) error {
		req := connect.NewRequest(message)
		req.Header().Set("Authorization", "Bearer "+access)
		req.Header().Set("X-Yufeng-Capability", "Bearer "+capabilityToken)
		var err error
		resp, err = c.models.Generate(ctx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// Invoke 携带访问令牌与当前租约能力令牌调用服务端工具。
func (c connectTools) Invoke(ctx context.Context, accessToken, capabilityToken, name, argsJSON string) (string, error) {
	var resp *connect.Response[toolgatewayv1.InvokeToolResponse]
	err := c.sess.Call(ctx, accessToken, func(access string) error {
		req := connect.NewRequest(&toolgatewayv1.InvokeToolRequest{ToolName: name, ArgsJson: argsJSON})
		req.Header().Set("Authorization", "Bearer "+access)
		req.Header().Set("X-Yufeng-Capability", "Bearer "+capabilityToken)
		var err error
		resp, err = c.client.InvokeTool(ctx, req)
		return err
	})
	if err != nil {
		return "", err
	}
	return resp.Msg.ResultJson, nil
}
