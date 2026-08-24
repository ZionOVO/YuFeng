package runtime

import (
	"context"
	"fmt"
	"time"
)

// SuperviseConfig 是 yufeng-agentd 孵化一次执行实例的监督参数。能力令牌只留在本结构，不得写入子进程环境。
type SuperviseConfig struct {
	Bin             string
	Args            []string
	WorkID          string
	RunID           string
	LeaseID         string
	LeaseEpoch      int64
	TTL             time.Duration
	Budget          *CallBudget
	Limits          ResourceLimit
	WorkDir         string
	Client          WorkClient
	Tools           ToolCaller
	Models          ModelCaller
	AccessSession   *AccessSession
	AccessToken     string
	CapabilityToken string
	Input           WorkInput
	SagaSnapshot    SagaSnapshot
	LeaseLost       <-chan struct{}
	ExtendEvery     time.Duration
	ctx             context.Context
}

func (c *SuperviseConfig) currentAccessToken() string {
	if c == nil {
		return ""
	}
	if c.AccessSession != nil {
		return c.AccessSession.AccessToken()
	}
	return c.AccessToken
}

func (c *SuperviseConfig) invokeCtx() context.Context {
	if c != nil && c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// Supervise 通过已连接的本地监督代理孵化 yufeng-run：监督进程代持令牌，存活时限到期或丢失租约时终止子进程树。
func Supervise(ctx context.Context, cfg SuperviseConfig) HatchResult {
	var out HatchResult
	if cfg.Budget != nil {
		if err := cfg.Budget.Consume(); err != nil {
			out.Err = err
			out.Record.Events = append(out.Record.Events, "budget")
			return out
		}
	}
	if cfg.Bin == "" {
		out.Err = fmt.Errorf("run binary is empty")
		return out
	}
	nonce, err := newNonce()
	if err != nil {
		out.Err = err
		return out
	}
	transport, err := newLocalBrokerTransport(nonce)
	if err != nil {
		out.Err = err
		return out
	}
	defer transport.Close() //nolint:errcheck // 执行实例退出后尽力关闭本地监督传输。
	signals, err := newChildSignals(nonce)
	if err != nil {
		out.Err = err
		return out
	}
	defer signals.Close()
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = defaultRunWorkDir()
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg.ctx = runCtx

	hub := &brokerHub{nonce: nonce, cfg: &cfg, capabilityToken: cfg.CapabilityToken, saga: cfg.SagaSnapshot}
	brokerReady := make(chan error, 1)
	go func() {
		conn, acceptErr := transport.Accept(runCtx)
		if acceptErr != nil {
			brokerReady <- acceptErr
			return
		}
		hub.conn = conn
		brokerReady <- nil
		hub.serve()
	}()
	go watchLease(runCtx, cancel, signals.RequestCancel, &cfg, hub.setCapability)
	if cfg.SagaSnapshot.CancelRequested {
		signals.RequestCancel()
	}

	args := cfg.Args
	if len(args) == 0 {
		ttl := cfg.TTL
		if ttl <= 0 {
			ttl = 30 * time.Second
		}
		args = []string{"-work-id", cfg.WorkID, "-ttl", ttl.String()}
	}
	transportFiles := transport.ChildFiles()
	nextExtraFD := 3 + len(transportFiles)
	brokerFD := -1
	if len(transportFiles) > 0 {
		brokerFD = 3
	}
	childEnvironment := append(ChildEnv(cfg.WorkID, cfg.RunID, nonce, brokerFD, nextExtraFD, nextExtraFD+1), transport.ChildEnvironment()...)
	childEnvironment = append(childEnvironment, signals.environment...)
	childFiles := append(transportFiles, signals.files...)
	out = Hatch(runCtx, HatchConfig{
		Bin:        cfg.Bin,
		Args:       args,
		Env:        childEnvironment,
		TTL:        cfg.TTL,
		WorkDir:    workDir,
		Limits:     cfg.Limits,
		Sandbox:    cfg.Input.IsInvestigation(),
		ExtraFiles: childFiles,
	})
	_ = transport.Close()
	// 未使用监督协议的普通子进程会在未连接时正常退出；关闭监听器会让
	// Accept 返回错误，最终仍必须由缺失终态回执判失败，不能把它误报为传输故障。
	<-brokerReady
	out.EnvKeys = hub.envKeys()
	out.TerminalKind, out.TerminalPayload = hub.terminal()
	if out.Err == nil {
		switch out.TerminalKind {
		case "done":
		case "fail":
			out.Err = fmt.Errorf("run reported failure: %s", firstNonEmpty(out.TerminalPayload, "unknown failure"))
		default:
			out.Err = fmt.Errorf("run exited without terminal broker receipt")
		}
	}
	return out
}

func watchLease(ctx context.Context, loseLease, requestCancel context.CancelFunc, cfg *SuperviseConfig, setCapability func(string)) {
	if cfg == nil {
		return
	}
	if cfg.LeaseLost != nil {
		go func() {
			select {
			case <-cfg.LeaseLost:
				loseLease()
			case <-ctx.Done():
			}
		}()
	}
	if cfg.Client == nil || cfg.ExtendEvery <= 0 || cfg.WorkID == "" {
		return
	}
	ticker := time.NewTicker(cfg.ExtendEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			extension, err := cfg.Client.Extend(ctx, cfg.WorkID, cfg.LeaseID, cfg.LeaseEpoch)
			if err != nil {
				loseLease()
				return
			}
			if setCapability != nil {
				setCapability(extension.CapabilityToken)
			}
			if extension.CancelRequested && requestCancel != nil {
				requestCancel()
			}
		}
	}
}
