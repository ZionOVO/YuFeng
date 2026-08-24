package brain

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/eventbus"
	"yufeng/lib/kernel"
	"yufeng/lib/store"
)

// BuildInfo 是构建元数据。
type BuildInfo struct {
	Version         string
	ContractVersion string
	SHA             string
	Time            string
}

// Options 是中台运行时选项。
type Options struct {
	SessionTTL            time.Duration
	AllowSelfRegistration bool
	PasswordMinLength     int32
	SigningKey            ed25519.PrivateKey
	ShadowMinDuration     time.Duration
	ShadowMinRequests     uint64
	CanaryMinDuration     time.Duration
	CanaryMinRequests     uint64
	AgentBootstrapToken   string
	UnitBootstrapToken    string
	ModelSideToken        string
	JarvisAgentID         string
	CentralWorkerID       string
	Bus                   *eventbus.Bus
	// DemoTriage 打开 §18.1.1 演示谓词与单令牌工具网关。不得与生产默认同时开。
	DemoTriage bool
	// DevInsecure 允许文件私钥与明文监听。
	DevInsecure bool
	// ArtifactSigner 负责生产制品签发；生产模式不得回退到 SigningKey。
	ArtifactSigner kernel.Signer
	// ConsoleDir 是控制台静态产物目录（console/dist）；空则不托管 /app。
	ConsoleDir string
	// WorkloadCertificateIssuer 经独立签发进程签发 24 小时 worker 客户端证书。
	WorkloadCertificateIssuer kernel.WorkloadCertificateIssuer
	// TrustedProxyPrefixes 只授权这些直接对端提供转发来源头。
	TrustedProxyPrefixes []netip.Prefix
}

// NewMux 装配所有 Connect 服务与超文本传输协议探针，是入口函数手工装配的唯一位置。
func NewMux(st *store.Store, info BuildInfo, opts Options) http.Handler {
	mux := http.NewServeMux()
	registered := map[string]struct{}{}
	handle := func(path string, handler http.Handler) {
		registered[path] = struct{}{}
		mux.Handle(path, handler)
	}
	pub := opts.SigningKey.Public().(ed25519.PublicKey)
	if opts.ArtifactSigner != nil {
		if sp := opts.ArtifactSigner.Public(); len(sp) == ed25519.PublicKeySize {
			pub = sp
		}
	}

	health := NewHealthServer(st, info.Version, info.ContractVersion, info.SHA, info.Time)
	path, handler := health.Handler()
	handle(path, handler)

	auth := NewAuthServer(st.Pool(), opts.SessionTTL, opts.AllowSelfRegistration, opts.PasswordMinLength)
	auth.SetTrustedProxies(opts.TrustedProxyPrefixes)
	path, handler = auth.Handler()
	handle(path, handler)

	users := NewUserServer(st.Pool(), opts.PasswordMinLength)
	path, handler = users.Handler()
	handle(path, handler)

	registry := NewRegistryServer(st.Pool(), pub, opts.UnitBootstrapToken)
	registry.SetTrustedProxies(opts.TrustedProxyPrefixes)
	path, handler = registry.Handler()
	handle(path, handler)

	agents := NewAgentServer(st.Pool(), opts.AgentBootstrapToken, opts.SigningKey)
	if !opts.DevInsecure {
		agents.allowUnboundShared = false
	}
	path, handler = agents.Handler()
	handle(path, handler)
	if opts.JarvisAgentID != "" && opts.AgentBootstrapToken != "" {
		_ = SeedAgentBootstrap(context.Background(), st.Pool(), opts.JarvisAgentID, opts.AgentBootstrapToken)
	}

	telemetry := NewTelemetryServer(st.Pool(), opts.Bus, agents, opts.JarvisAgentID, st.TrafficPool())
	telemetry.demoTriage = opts.DemoTriage
	path, handler = telemetry.Handler()
	handle(path, handler)

	modelResults := NewModelResultServer(st.Pool(), pub, opts.ModelSideToken, agents, opts.JarvisAgentID)
	path, handler = modelResults.Handler()
	handle(path, handler)

	relay := NewSensitiveRelay()
	if err := RecoverSensitiveGenerationOutcomes(context.Background(), st.Pool()); err != nil {
		panic(err)
	}
	if err := ResetSensitiveEvidenceRequests(context.Background(), st.Pool()); err != nil {
		panic(err)
	}
	cases := NewCaseServer(st.Pool())
	path, handler = cases.Handler()
	handle(path, handler)
	evidence := NewEvidenceServer(st.Pool(), relay, agents, opts.JarvisAgentID)
	path, handler = evidence.Handler()
	handle(path, handler)
	modules := NewModuleCatalogServer(st.Pool())
	path, handler = modules.Handler()
	handle(path, handler)
	interaction := NewAgentInteractionServer(st.Pool())
	path, handler = interaction.Handler()
	handle(path, handler)
	profiles := NewAgentProfileServer(st.Pool())
	path, handler = profiles.Handler()
	handle(path, handler)

	govern := NewGovernServer(st.Pool(), opts.SigningKey, opts.ShadowMinDuration, opts.ShadowMinRequests, opts.CanaryMinDuration, opts.CanaryMinRequests)
	govern.demoTriage = opts.DemoTriage
	govern.artifactSigner = opts.ArtifactSigner
	path, handler = govern.Handler()
	handle(path, handler)

	artifacts := NewArtifactServer(st.Pool())
	path, handler = artifacts.Handler()
	handle(path, handler)

	assets := NewAssetServer(st.Pool(), opts.CentralWorkerID)
	assets.signingKey = opts.SigningKey
	assets.artifactSigner = opts.ArtifactSigner
	path, handler = assets.Handler()
	handle(path, handler)

	audit := NewAuditServer(st.Pool())
	path, handler = audit.Handler()
	handle(path, handler)

	console := NewConsoleServer(st.Pool())
	path, handler = console.Handler()
	handle(path, handler)

	sessions := NewSessionServer(st.Pool(), agents, opts.JarvisAgentID)
	path, handler = sessions.Handler()
	handle(path, handler)

	runs := NewRunServer(st.Pool(), opts.SigningKey)
	path, handler = runs.Handler()
	handle(path, handler)

	workers := NewWorkerServer(st.Pool(), opts.SigningKey, opts.DevInsecure, opts.WorkloadCertificateIssuer)
	workers.sensitiveRelay = relay
	path, handler = workers.Handler()
	handle(path, handler)

	grants := NewGrantServer(st.Pool())
	path, handler = grants.Handler()
	handle(path, handler)

	onboard := NewOnboardingServer(st.Pool(), opts.JarvisAgentID)
	onboard.sensitiveRelay = relay
	onboard.capabilityPub = opts.SigningKey.Public().(ed25519.PublicKey)
	onboard.artifactPub = pub
	if opts.DevInsecure {
		onboard.signingKey = opts.SigningKey
	}
	onboard.artifactSigner = opts.ArtifactSigner
	path, handler = onboard.Handler()
	handle(path, handler)
	path, handler = onboard.ModelHandler()
	handle(path, handler)

	tools := NewToolGatewayServer(st.Pool(), opts.SigningKey)
	tools.demoTriage = opts.DemoTriage
	tools.artifactSigner = opts.ArtifactSigner
	tools.artifactPub = pub
	tools.sensitiveRelay = relay
	path, handler = tools.Handler()
	handle(path, handler)

	commands := NewCommandServer(st.Pool())
	path, handler = commands.Handler()
	handle(path, handler)
	if err := CheckRequiredServices(registered); err != nil {
		panic(err)
	}
	MountConsole(mux, opts.ConsoleDir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.DevInsecure {
			r = r.WithContext(context.WithValue(r.Context(), clientCertRequiredKey{}, true))
		}
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
			r = r.WithContext(context.WithValue(r.Context(), clientCertHashKey{}, hex.EncodeToString(sum[:])))
		}
		mux.ServeHTTP(w, r)
	})
}

type clientCertHashKey struct{}
type clientCertRequiredKey struct{}

func clientCertHash(ctx context.Context) string {
	hash, _ := ctx.Value(clientCertHashKey{}).(string)
	return hash
}

func requireAgentClientCert(ctx context.Context) error {
	required, _ := ctx.Value(clientCertRequiredKey{}).(bool)
	if required && clientCertHash(ctx) == "" {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("agent client certificate is required"))
	}
	return nil
}

// ReadyFunc 供独立管理面 /readyz 探测 PostgreSQL。
func ReadyFunc(st *store.Store) func(context.Context) error {
	return func(ctx context.Context) error {
		return pingPostgres(ctx, st)
	}
}
