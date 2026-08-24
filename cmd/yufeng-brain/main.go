// yufeng-brain 是后端控制面，承载账本、治理、网络接口与智能代理控制队列。
// 控制台静态产物由本进程托管在 /app。
// 智能代理运行时不在本进程内；yufeng-jarvis 与 yufeng-agentd 是独立进程。
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"yufeng/lib/brain"
	"yufeng/lib/eventbus"
	"yufeng/lib/kernel"
	"yufeng/lib/observability"
	"yufeng/lib/store"
)

var (
	version = "dev"
	sha     = "unknown"
	builtAt = "unknown"
)

func main() {
	var (
		addr                   = flag.String("addr", ":9050", "业务监听地址")
		adminAddr              = flag.String("admin-addr", ":19090", "管理面监听地址（探针与指标）")
		dsn                    = flag.String("dsn", "postgres://localhost:5432/yufeng?sslmode=disable", "PostgreSQL DSN")
		dsnPasswordFile        = flag.String("dsn-password-file", "", "PostgreSQL 密码文件")
		trafficDSN             = flag.String("traffic-dsn", os.Getenv("YUFENG_TRAFFIC_DSN"), "流量入账 PostgreSQL DSN；生产使用独立受限角色")
		trafficPasswordFile    = flag.String("traffic-dsn-password-file", "", "流量入账 PostgreSQL 密码文件")
		bootstrapUser          = flag.String("bootstrap-admin-user", "admin", "初始管理员用户名")
		bootstrapPass          = flag.String("bootstrap-admin-pass", "", "仅开发使用的初始管理员明文密码")
		bootstrapPassFile      = flag.String("bootstrap-admin-pass-file", "", "初始管理员密码文件")
		allowSelfReg           = flag.Bool("allow-self-registration", false, "是否开放自助注册")
		signingKey             = flag.String("signing-key", "", "制品签名私钥 hex 文件；仅 -dev-insecure")
		signingSocket          = flag.String("signing-socket", "", "生产签名套接字")
		signingPub             = flag.String("signing-pubkey", "", "套接字签名对应的公钥 hex 文件")
		workloadSigning        = flag.String("workload-signing-socket", "", "独立工作负载证书签发套接字")
		agentBootToken         = flag.String("agent-bootstrap-token", "", "仅开发使用的 Agent 注册明文引导令牌")
		agentBootTokenFile     = flag.String("agent-bootstrap-token-file", "", "Agent 注册引导令牌文件")
		unitBootToken          = flag.String("unit-bootstrap-token", "", "仅开发使用的单元注册明文引导令牌")
		unitBootTokenFile      = flag.String("unit-bootstrap-token-file", "", "单元首次注册引导令牌文件")
		modelSideToken         = flag.String("modelside-token", "", "仅开发使用的 ModelSide 结果上报明文令牌")
		modelSideTokenFile     = flag.String("modelside-token-file", "", "ModelSide 结果上报令牌文件")
		jarvisAgentID          = flag.String("jarvis-agent-id", "jarvis-1", "会话指令投递的 Jarvis Agent 标识")
		centralWorkerID        = flag.String("central-worker-id", "", "标准 Compose 中央调查监督进程标识")
		centralWorkerToken     = flag.String("central-worker-bootstrap-token", os.Getenv("YUFENG_CENTRAL_WORKER_BOOTSTRAP_TOKEN"), "中央调查监督进程一次性引导令牌")
		centralWorkerTokenFile = flag.String("central-worker-bootstrap-token-file", "", "中央调查监督进程一次性引导令牌文件")
		centralWorkerKey       = flag.String("central-worker-public-key", "", "中央调查监督进程登记公钥")
		centralWorkerKeyFile   = flag.String("central-worker-public-key-file", "", "中央调查监督进程登记公钥文件")
		centralWorkerCert      = flag.String("central-worker-client-cert", "", "中央调查监督进程客户端证书")
		natsPort               = flag.Int("nats-port", -1, "内嵌 NATS 端口；-1 关闭")
		natsURL                = flag.String("nats-url", "", "外部 NATS URL；非空时优先外部")
		devInsecure            = flag.Bool("dev-insecure", false, "允许明文与文件私钥（仅开发）")
		tlsCert                = flag.String("tls-cert", "", "TLS 证书")
		tlsKey                 = flag.String("tls-key", "", "TLS 私钥")
		tlsClientCA            = flag.String("tls-client-ca", "", "双向 TLS 客户端权威")
		guardWindow            = flag.Duration("guard-window", 30*time.Second, "守护窗口周期")
		denyThreshold          = flag.Uint64("deny-threshold", 1, "坏窗口误报举报阈值")
		guardBad               = flag.Int("guard-bad-windows", 2, "连续坏窗口自动回滚阈值")
		auditCheckpoint        = flag.String("audit-checkpoint", "", "审计哈希链进程外只追加检查点路径")
		consoleDir             = flag.String("console-dir", firstNonEmpty(os.Getenv("YUFENG_CONSOLE_DIR"), "console/dist"), "控制台静态目录")
		trustedProxyCIDRs      = flag.String("trusted-proxy-cidrs", "", "可信直接代理网段，逗号分隔；空则忽略转发来源头")
	)
	development := registerBrainDevelopmentFlags()
	flag.Parse()
	demoTriage := *development.demoTriage
	bootstrapPassword, err := resolveFlagSecret(*bootstrapPass, *bootstrapPassFile, *devInsecure, "bootstrap admin password")
	if err != nil {
		log.Fatal(err)
	}
	agentBootstrap, err := resolveFlagSecret(*agentBootToken, *agentBootTokenFile, *devInsecure, "agent bootstrap token")
	if err != nil {
		log.Fatal(err)
	}
	unitBootstrap, err := resolveFlagSecret(*unitBootToken, *unitBootTokenFile, *devInsecure, "unit bootstrap token")
	if err != nil {
		log.Fatal(err)
	}
	modelSideCredential, err := resolveFlagSecret(*modelSideToken, *modelSideTokenFile, *devInsecure, "modelside token")
	if err != nil {
		log.Fatal(err)
	}
	centralBootstrap := strings.TrimSpace(*centralWorkerToken)
	if strings.TrimSpace(*centralWorkerID) != "" {
		centralBootstrap, err = resolveFlagSecret(*centralWorkerToken, *centralWorkerTokenFile, *devInsecure, "central worker bootstrap token")
		if err != nil {
			log.Fatal(err)
		}
	}
	governanceDSN, err := dsnWithPasswordFile(*dsn, *dsnPasswordFile, *devInsecure)
	if err != nil {
		log.Fatalf("governance dsn: %v", err)
	}
	trafficDatabaseDSN, err := dsnWithPasswordFile(*trafficDSN, *trafficPasswordFile, *devInsecure)
	if err != nil {
		log.Fatalf("traffic dsn: %v", err)
	}
	trustedProxies, err := parseTrustedProxyPrefixes(*trustedProxyCIDRs)
	if err != nil {
		log.Fatalf("trusted proxy cidrs: %v", err)
	}
	if err := kernel.ValidateProductionTLS(*devInsecure, *tlsCert, *tlsKey); err != nil {
		log.Fatalf("tls: %v", err)
	}
	if err := kernel.ValidateProductionMTLS(*devInsecure, *tlsClientCA); err != nil {
		log.Fatalf("mtls: %v", err)
	}
	if err := kernel.ValidateProductionSigner(*devInsecure, *signingKey, *signingSocket); err != nil {
		log.Fatalf("signer: %v", err)
	}
	if !*devInsecure && strings.TrimSpace(*workloadSigning) == "" {
		log.Fatal("production worker enrollment requires workload-signing-socket")
	}
	if err := kernel.ValidateProductionSecrets(*devInsecure, bootstrapPassword, agentBootstrap, unitBootstrap); err != nil {
		log.Fatalf("secrets: %v", err)
	}
	if err := kernel.ValidateProductionAgentBootstrap(*devInsecure, agentBootstrap, *jarvisAgentID); err != nil {
		log.Fatalf("agent bootstrap: %v", err)
	}
	if demoTriage && !*devInsecure {
		log.Fatal("development triage cannot be combined with production tls")
	}
	if !*devInsecure && strings.TrimSpace(trafficDatabaseDSN) == "" {
		log.Fatal("traffic-dsn with a restricted database role is required")
	}
	shutdownObs, err := observability.Setup("yufeng-brain")
	if err != nil {
		log.Fatalf("observability: %v", err)
	}
	defer func() { _ = shutdownObs(context.Background()) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, store.Config{DSN: governanceDSN, TrafficDSN: trafficDatabaseDSN, TrafficMaxConns: 4})
	if err != nil {
		log.Fatalf("打开数据库: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatalf("数据库迁移: %v", err)
	}
	if !*devInsecure {
		if err := store.ValidateRestrictedTrafficRole(ctx, st.Pool(), st.TrafficPool()); err != nil {
			log.Fatalf("流量数据库角色: %v", err)
		}
	}
	if err := brain.InvalidateAccessTokens(ctx, st.Pool()); err != nil {
		log.Fatalf("作废访问令牌: %v", err)
	}
	if err := brain.EnsureBootstrapAdmin(ctx, st.Pool(), *bootstrapUser, bootstrapPassword); err != nil {
		log.Fatalf("初始管理员: %v", err)
	}
	if err := brain.EnsureBootstrapJarvis(ctx, st.Pool(), *jarvisAgentID); err != nil {
		log.Fatalf("初始贾维斯: %v", err)
	}
	if strings.TrimSpace(*centralWorkerID) != "" {
		workerPublicKey := strings.TrimSpace(*centralWorkerKey)
		if strings.TrimSpace(*centralWorkerKeyFile) != "" {
			if workerPublicKey != "" {
				log.Fatal("central worker public key and public key file are mutually exclusive")
			}
			raw, readErr := os.ReadFile(strings.TrimSpace(*centralWorkerKeyFile))
			if readErr != nil {
				log.Fatalf("中央调查监督进程公钥: %v", readErr)
			}
			workerPublicKey = string(raw)
		}
		certHash, certificatePublicKey, err := certificateIdentity(*centralWorkerCert)
		if err != nil {
			log.Fatalf("中央调查监督进程证书: %v", err)
		}
		canonicalWorkerKey, err := canonicalPublicKeyPEM(workerPublicKey)
		if err != nil || canonicalWorkerKey != certificatePublicKey {
			log.Fatal("central worker public key must match the client certificate")
		}
		if err := brain.SeedCentralWorkerBootstrap(ctx, st.Pool(), *centralWorkerID, canonicalWorkerKey, certHash, centralBootstrap); err != nil {
			log.Fatalf("中央调查监督进程引导: %v", err)
		}
	}
	signingPrivateKey, err := brain.LoadOrCreateSigningKey(*signingKey)
	if err != nil {
		log.Fatalf("签名密钥: %v", err)
	}
	var artifactSigner kernel.Signer
	var workloadIssuer kernel.WorkloadCertificateIssuer
	if *signingSocket != "" {
		pub := signingPrivateKey.Public().(ed25519.PublicKey)
		if *signingPub != "" {
			raw, rerr := os.ReadFile(*signingPub)
			if rerr != nil {
				log.Fatalf("signing pubkey: %v", rerr)
			}
			b, derr := hex.DecodeString(strings.TrimSpace(string(raw)))
			if derr != nil || len(b) != ed25519.PublicKeySize {
				log.Fatal("signing pubkey is invalid")
			}
			pub = ed25519.PublicKey(b)
		}
		artifactSigner, err = kernel.NewSocketSigner(*signingSocket, pub)
		if err != nil {
			log.Fatalf("signing socket: %v", err)
		}
	}
	if *workloadSigning != "" {
		workloadIssuer, err = kernel.NewSocketWorkloadCertificateIssuer(*workloadSigning)
		if err != nil {
			log.Fatalf("workload signing socket: %v", err)
		}
	}
	var bus *eventbus.Bus
	if *natsURL != "" {
		bus, err = eventbus.NewExternal(*natsURL)
		if err != nil {
			log.Fatalf("外部 NATS: %v", err)
		}
		defer bus.Close()
	} else if *natsPort > 0 {
		bus, err = eventbus.NewEmbedded("127.0.0.1", *natsPort)
		if err != nil {
			log.Fatalf("内嵌 NATS: %v", err)
		}
		defer bus.Close()
	}

	sched := brain.SchedulerConfig{Interval: *guardWindow, DenyThreshold: *denyThreshold, GuardBadWindows: *guardBad}
	if !demoTriage {
		sched = brain.ProductionScheduler(*guardWindow)
	}
	sched.DemoTriage = demoTriage
	sched.SigningKey = signingPrivateKey
	sched.ArtifactSigner = artifactSigner
	if err := brain.ValidateSchedulerConfig(sched, demoTriage); err != nil {
		log.Fatalf("scheduler: %v", err)
	}
	brain.StartScheduler(ctx, st.Pool(), sched)
	maintenanceAgents := brain.NewAgentServer(st.Pool(), agentBootstrap, signingPrivateKey)
	maintenanceAgents.SetProductionBindingMode()
	brain.StartTrafficMaintenance(ctx, st.Pool(), maintenanceAgents, *jarvisAgentID)
	brain.StartUnitHealthMonitor(ctx, st.Pool(), 30*time.Second)
	brain.StartShadowCandidateCoordinator(ctx, st.Pool(), signingPrivateKey, artifactSigner)
	if *auditCheckpoint != "" {
		if artifactSigner == nil && !*devInsecure {
			log.Fatal("production audit checkpoints require the typed signing socket")
		}
		brain.StartAuditCheckpointLoop(ctx, st.Pool(), *auditCheckpoint, kernel.AuditCheckpointPeriod, artifactSigner)
	}
	brain.StartLedgerMaintenance(ctx, st.Pool(), *auditCheckpoint, artifactSigner)
	if bus != nil {
		brain.StartOutboxLoop(ctx, st.Pool(), bus)
	}

	srv := kernel.NewProductionHTTPServer(*addr, brain.NewMux(st, brain.BuildInfo{Version: version, ContractVersion: "v1", SHA: sha, Time: builtAt}, brain.Options{
		SessionTTL: 12 * time.Hour, PasswordMinLength: brain.MinPasswordLength, AllowSelfRegistration: *allowSelfReg,
		SigningKey: signingPrivateKey, AgentBootstrapToken: agentBootstrap, UnitBootstrapToken: unitBootstrap, ModelSideToken: modelSideCredential, JarvisAgentID: *jarvisAgentID, CentralWorkerID: *centralWorkerID, Bus: bus,
		DemoTriage: demoTriage, DevInsecure: *devInsecure, ArtifactSigner: artifactSigner,
		ConsoleDir:                *consoleDir,
		WorkloadCertificateIssuer: workloadIssuer,
		TrustedProxyPrefixes:      trustedProxies,
	}))
	// 业务口要等模型出网写回，写超时必须覆盖 ChatCompleteTimeout。
	srv.WriteTimeout = kernel.ChatCompleteTimeout + kernel.HTTPWriteTimeout
	admin := &http.Server{
		Addr:              *adminAddr,
		Handler:           observability.Handler(brain.ReadyFunc(st), version, "v1"),
		ReadHeaderTimeout: kernel.HTTPReadHeaderTimeout,
	}
	if *tlsClientCA != "" {
		// 业务口与 /app 共用 :9050：校验已出示的客户端证书，不强制浏览器持证。
		mtls, err := kernel.ServerOptionalClientCertConfig(*tlsClientCA)
		if err != nil {
			log.Fatalf("tls client ca: %v", err)
		}
		srv.TLSConfig = mtls
	}
	errCh := make(chan error, 2)
	go func() {
		if *tlsCert != "" && *tlsKey != "" {
			errCh <- srv.ListenAndServeTLS(*tlsCert, *tlsKey)
			return
		}
		errCh <- srv.ListenAndServe()
	}()
	go func() { errCh <- admin.ListenAndServe() }()
	log.Printf("yufeng-brain 业务 %s 管理面 %s", *addr, *adminAddr)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	case <-ctx.Done():
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		if err := admin.Shutdown(shCtx); err != nil {
			log.Printf("admin shutdown: %v", err)
		}
	}
}

func parseTrustedProxyPrefixes(raw string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func resolveFlagSecret(plain, path string, devInsecure bool, name string) (string, error) {
	plain, path = strings.TrimSpace(plain), strings.TrimSpace(path)
	if plain != "" && path != "" {
		return "", errors.New(name + " and its file are mutually exclusive")
	}
	if plain != "" {
		if !devInsecure {
			return "", errors.New("production requires " + name + " from a file")
		}
		return plain, nil
	}
	if path == "" {
		if devInsecure {
			return "", nil
		}
		return "", errors.New(name + " file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New(name + " file is empty")
	}
	return secret, nil
}

func dsnWithPasswordFile(raw, passwordFile string, devInsecure bool) (string, error) {
	raw, passwordFile = strings.TrimSpace(raw), strings.TrimSpace(passwordFile)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User == nil {
		return "", errors.New("database dsn is invalid")
	}
	_, embedded := parsed.User.Password()
	if embedded && !devInsecure {
		return "", errors.New("production database dsn must not contain a password")
	}
	if passwordFile == "" {
		if devInsecure {
			return raw, nil
		}
		return "", errors.New("production database password file is required")
	}
	password, err := resolveFlagSecret("", passwordFile, devInsecure, "database password")
	if err != nil {
		return "", err
	}
	parsed.User = url.UserPassword(parsed.User.Username(), password)
	return parsed.String(), nil
}

func certificateIdentity(path string) (string, string, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return "", "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", "", errors.New("client certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(certificate.Raw)
	publicKey := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	return hex.EncodeToString(sum[:]), publicKey, nil
}

func canonicalPublicKeyPEM(raw string) (string, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil || block.Type != "PUBLIC KEY" {
		return "", errors.New("public key is invalid")
	}
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: encoded})), nil
}

func firstNonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}
