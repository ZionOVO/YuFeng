// yufeng-edge 是数据面二进制，承载反向代理、外部授权、旁路观察与镜像观察四种入口姿态。
//
// 连接中台时，业务监听只使用已验签监听计划和资产世代；断网自治要求两者的有效缓存同时存在。
// 遥测先写本地可靠帧缓冲，再由后台循环上传。
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
	"os"
	"os/signal"
	"strings"
	"syscall"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
	"yufeng/lib/observability"
)

func main() {
	modelIngressDefaults, err := modelIngressDefaultsFromEnvironment()
	if err != nil {
		log.Fatal(err)
	}
	var (
		adminAddr           = flag.String("admin-addr", ":19092", "管理面监听地址")
		pubkeyHex           = flag.String("pubkey", "", "验签公钥（hex 文件）")
		brainURL            = flag.String("brain", "", "中台地址；非空则注册并从 brain 拉取发布/上报遥测")
		unitID              = flag.String("unit", "", "单元标识（brain 模式）")
		unitVer             = flag.String("unit-version", firstNonEmpty(os.Getenv("YUFENG_UNIT_VERSION"), "unknown"), "单元版本（brain 模式）")
		devMode             = flag.Bool("dev-insecure", false, "允许连接明文中台（仅本地开发）")
		bootTokenFile       = flag.String("bootstrap-token-file", "", "首次注册用的部署级引导令牌文件")
		dataDir             = flag.String("data-dir", firstNonEmpty(os.Getenv("YUFENG_DATA_DIR"), ".tmp"), "发布缓存与会话目录")
		spoolDir            = flag.String("spool-dir", firstNonEmpty(os.Getenv("YUFENG_SPOOL_DIR"), ""), "遥测分段目录；空则落在 data-dir/spool")
		tlsCA               = flag.String("tls-ca", os.Getenv("YUFENG_TLS_CA"), "中台 TLS 权威")
		tlsCert             = flag.String("tls-cert", os.Getenv("YUFENG_TLS_CERT"), "单元客户端证书")
		tlsKey              = flag.String("tls-key", os.Getenv("YUFENG_TLS_KEY"), "单元客户端私钥")
		sourceKey           = flag.String("source-hmac-key", os.Getenv("YUFENG_SOURCE_HMAC_KEY"), "来源假名 32 字节密钥文件")
		modelSide           = flag.String("modelside", firstNonEmpty(os.Getenv("YUFENG_MODELSIDE_ENDPOINT"), "unix://"+kernel.DefaultModelSideSocket), "ModelSide 地址；空则关闭旁路")
		modelSideCA         = flag.String("modelside-tls-ca", os.Getenv("YUFENG_MODELSIDE_TLS_CA"), "跨主机 ModelSide TLS 权威")
		modelSideCert       = flag.String("modelside-tls-cert", os.Getenv("YUFENG_MODELSIDE_TLS_CERT"), "跨主机 ModelSide 客户端证书")
		modelSideKey        = flag.String("modelside-tls-key", os.Getenv("YUFENG_MODELSIDE_TLS_KEY"), "跨主机 ModelSide 客户端私钥")
		modelWindowMaxItems = flag.Uint64("model-ingress-window-max-items", modelIngressDefaults.maxItems, "Edge 模型输入缓存窗口本机条目硬上限")
		modelWindowMaxBytes = flag.Uint64("model-ingress-window-max-bytes", modelIngressDefaults.maxBytes, "Edge 模型输入缓存窗口本机保留字节硬上限")
		modelWindowMaxAge   = flag.Duration("model-ingress-window-max-age", modelIngressDefaults.maxAge, "Edge 模型输入缓存窗口本机排队年龄硬上限")
	)
	development := registerEdgeDevelopmentFlags()
	flag.Parse()
	modelHardLimit, err := modelIngressHardLimit(*modelWindowMaxItems, *modelWindowMaxBytes, *modelWindowMaxAge)
	if err != nil {
		log.Fatalf("Edge 模型输入缓存窗口配置: %v", err)
	}
	if err := validateLaunchMode(*brainURL, *development.localDemo, *devMode); err != nil {
		log.Fatal(err)
	}
	if strings.TrimSpace(*pubkeyHex) == "" {
		log.Fatal("pubkey is required")
	}
	shutdownObs, err := observability.Setup("yufeng-edge")
	if err != nil {
		log.Fatalf("observability: %v", err)
	}
	defer func() { _ = shutdownObs(context.Background()) }()

	pub, err := loadPubKey(*pubkeyHex)
	if err != nil {
		log.Fatalf("加载公钥: %v", err)
	}
	if *brainURL != "" {
		if strings.TrimSpace(*unitID) == "" {
			log.Fatal("brain mode requires -unit")
		}
		source, err := loadSourcePseudonymizer(*sourceKey)
		if err != nil {
			log.Fatalf("来源假名密钥: %v", err)
		}
		if strings.HasPrefix(*brainURL, "https://") && (*tlsCA == "" || *tlsCert == "" || *tlsKey == "") {
			log.Fatal("https brain requires -tls-ca -tls-cert -tls-key")
		}
		var hc *http.Client
		if *tlsCA != "" || *tlsCert != "" || *tlsKey != "" {
			var cerr error
			hc, cerr = kernel.HTTPClient(*tlsCA, *tlsCert, *tlsKey)
			if cerr != nil {
				log.Fatalf("tls client: %v", cerr)
			}
		}
		modelSender, err := newModelTrafficSender(*modelSide, *modelSideCA, *modelSideCert, *modelSideKey, *devMode)
		if err != nil {
			log.Fatalf("ModelSide 连接配置: %v", err)
		}
		sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		bootToken, err := loadEdgeSecret(*bootTokenFile)
		if err != nil {
			log.Fatalf("注册引导令牌: %v", err)
		}
		if err := runBrainMode(sigCtx, *brainURL, *adminAddr, *unitID, *unitVer, bootToken, *dataDir, *spoolDir, pub, source, hc, modelSender, modelHardLimit); err != nil {
			log.Fatalf("brain 模式: %v", err)
		}
		return
	}
	launchEdgeDevelopmentMode(development, *adminAddr, pub)
}

func loadEdgeSecret(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("bootstrap-token-file is required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(raw))
	if secret == "" {
		return "", errors.New("bootstrap token file is empty")
	}
	return secret, nil
}

func loadSourcePseudonymizer(path string) (edgecore.SourcePseudonymizer, error) {
	if strings.TrimSpace(path) == "" {
		return edgecore.SourcePseudonymizer{}, errors.New("source pseudonym key file is required")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return edgecore.SourcePseudonymizer{}, err
	}
	return edgecore.NewSourcePseudonymizer(key)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func loadPubKey(path string) (ed25519.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, err
	}
	// ed25519.Verify 对错误长度的公钥直接 panic；装载期拦住坏配置
	if len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key length %d invalid (want %d)", len(b), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(b), nil
}
