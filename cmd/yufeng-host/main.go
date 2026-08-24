// yufeng-host 在 Linux 与 OpenWrt 资产上执行白名单内的确定性维护原语。
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"

	"yufeng/lib/edgeclient"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	assetv1 "yufeng/proto/gen/assetv1"
	commandv1 "yufeng/proto/gen/commandv1"
	"yufeng/proto/gen/commandv1/commandv1connect"
	registryv1 "yufeng/proto/gen/registryv1"
)

func main() {
	var (
		brain         = flag.String("brain", "", "中台 HTTPS 地址")
		unitID        = flag.String("unit", "", "单元标识")
		version       = flag.String("version", "unknown", "程序版本")
		configPath    = flag.String("config", "", "0600 权限的 Host 配置文件")
		bootTokenFile = flag.String("bootstrap-token-file", "", "首次注册引导令牌文件")
		tlsCA         = flag.String("tls-ca", "", "中台传输层安全权威文件")
		tlsCert       = flag.String("tls-cert", "", "单元客户端证书文件")
		tlsKey        = flag.String("tls-key", "", "单元客户端私钥文件")
		devInsecure   = flag.Bool("dev-insecure", false, "仅本地开发允许明文中台")
	)
	flag.Parse()
	if runtime.GOOS != "linux" {
		log.Fatal("yufeng-host production execution is supported only on Linux and OpenWrt")
	}
	if currentUserIsRoot() {
		log.Fatal("yufeng-host must run as a non-root user")
	}
	if strings.TrimSpace(*brain) == "" || strings.TrimSpace(*unitID) == "" || strings.TrimSpace(*configPath) == "" {
		log.Fatal("brain, unit and config are required")
	}
	if err := validateBrainURL(*brain, *devInsecure); err != nil {
		log.Fatal(err)
	}
	cfg, err := loadHostConfig(*configPath)
	if err != nil {
		log.Fatalf("加载 Host 配置: %v", err)
	}
	pub, err := loadHostPublicKey(cfg.ArtifactPublicKeyFile)
	if err != nil {
		log.Fatalf("加载制品公钥: %v", err)
	}
	bootstrap, err := readSecretFile(*bootTokenFile)
	if err != nil {
		log.Fatalf("读取引导令牌: %v", err)
	}
	var hc *http.Client
	if strings.HasPrefix(*brain, "https://") {
		hc, err = kernel.HTTPClient(*tlsCA, *tlsCert, *tlsKey)
		if err != nil {
			log.Fatalf("传输层安全客户端: %v", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := edgeclient.New(*brain, hc)
	client.BootstrapToken = bootstrap
	sess, err := client.Register(ctx, &registryv1.RegisterRequest{
		UnitId: *unitID, Kind: registryv1.UnitKind_UNIT_KIND_HOST, Version: *version,
		ContractVersion: "v1", Asset: &assetv1.Asset{Id: *unitID, DisplayName: *unitID},
	})
	if err != nil {
		log.Fatalf("注册 Host: %v", err)
	}
	executor, err := newHostExecutor(cfg, pub, func(loadCtx context.Context, artifactID string) (*artifactv1.Artifact, error) {
		return fetchReleasedArtifact(loadCtx, client, sess, artifactID)
	})
	if err != nil {
		log.Fatalf("初始化 Host 执行器: %v", err)
	}
	defer executor.Close()
	commands := commandv1connect.NewCommandServiceClient(hcOrDefault(hc), *brain)
	log.Printf("yufeng-host 已注册：unit=%s asset=%s", sess.UnitID, sess.AssetID)
	runHostLoop(ctx, commands, sess, executor)
}

func runHostLoop(ctx context.Context, commands commandv1connect.CommandServiceClient, sess *edgeclient.Session, executor *hostExecutor) {
	for ctx.Err() == nil {
		snapshot := sess.Snapshot()
		poll := connect.NewRequest(&commandv1.PollCommandsRequest{UnitId: snapshot.UnitID, LongPollSeconds: 20})
		poll.Header().Set("Authorization", "Bearer "+snapshot.Token)
		resp, err := commands.PollCommands(ctx, poll)
		if err != nil {
			log.Printf("轮询指令失败: %v", err)
			if !waitContext(ctx, 5*time.Second) {
				return
			}
			continue
		}
		for _, cmd := range resp.Msg.GetCommands() {
			reporter := func(reportCtx context.Context, receipt *commandv1.StepReceipt) error {
				snapshot := sess.Snapshot()
				req := connect.NewRequest(&commandv1.ReportStepRequest{
					CommandId: cmd.GetCommandId(), UnitId: snapshot.UnitID, LeaseId: cmd.GetLeaseId(),
					LeaseEpoch: cmd.GetLeaseEpoch(), Receipts: []*commandv1.StepReceipt{receipt},
				})
				req.Header().Set("Authorization", "Bearer "+snapshot.Token)
				_, err := commands.ReportStep(reportCtx, req)
				return err
			}
			if err := executor.Execute(ctx, cmd, reporter); err != nil {
				log.Printf("执行指令 %s: %v", cmd.GetCommandId(), err)
			}
		}
	}
}

func fetchReleasedArtifact(ctx context.Context, client *edgeclient.Client, sess *edgeclient.Session, artifactID string) (*artifactv1.Artifact, error) {
	for {
		page, err := client.ListReleases(ctx, sess, true)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.GetArtifact().GetId() == artifactID {
				return item.GetArtifact(), nil
			}
		}
		edgeclient.CommitCursor(sess, page.NextCursor)
		if !page.HasMore {
			return nil, errors.New("artifact is not released to this host")
		}
	}
}

func validateBrainURL(raw string, devInsecure bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("brain must be an absolute HTTP or HTTPS URL")
	}
	if u.Scheme != "https" && !devInsecure {
		return errors.New("production host requires an HTTPS brain")
	}
	return nil
}

func readSecretFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("secret file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New("secret file is empty")
	}
	return secret, nil
}

func loadHostPublicKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	if len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key length %d invalid", len(decoded))
	}
	return ed25519.PublicKey(decoded), nil
}

func hcOrDefault(hc *http.Client) *http.Client {
	if hc != nil {
		return hc
	}
	return http.DefaultClient
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
