// yfctl 提供运维、签发与显式开发演示命令。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "keys":
		err = runKeys(os.Args[2:])
	case "login":
		err = runLogin(os.Args[2:])
	case "publish":
		err = runPublish(os.Args[2:])
	case "signer":
		err = runSigner(os.Args[2:])
	case "policy-enforce":
		err = runPolicyEnforce(os.Args[2:])
	case "retire":
		err = runRetire(os.Args[2:])
	case "tls":
		err = runTLS(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		if handled, developmentErr := runDevelopmentCommand(os.Args[1], os.Args[2:]); handled {
			err = developmentErr
		} else {
			fmt.Fprintf(os.Stderr, "未知子命令 %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `用法：
	  yfctl keys -out <目录>  幂等生成正式制品签发密钥
  yfctl login -brain <中台> -username <用户> -password <密码>
  yfctl publish -brain <中台> -token <令牌> -asset <资产> -payload <规则载荷> [-canary 25]
  yfctl signer -socket <unix> -key <hex私钥>
  yfctl policy-enforce -brain <中台> -username <管理员> -password <密码> -asset <资产>
  yfctl retire -brain <中台> -username <管理员> -password <密码> -asset <资产> -release <发布>
  yfctl tls [-out 目录]    生成 compose / 本地用的双向 TLS 证书`+developmentUsage())
}

func runKeys(args []string) error {
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	dir := fs.String("out", "", "密钥输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*dir) == "" {
		return fmt.Errorf("keys output directory is required")
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return err
	}
	keyPath := filepath.Join(*dir, "signing.key.hex")
	legacyKeyPath := filepath.Join(*dir, "dev.key.hex")
	pubPath := filepath.Join(*dir, "pubkey.hex")
	priv, err := loadOrCreateSigningKey(keyPath, legacyKeyPath)
	if err != nil {
		return err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("derive signing public key")
	}
	if err := writePublicKey(pubPath, pub); err != nil {
		return err
	}
	fmt.Printf("签发密钥已就绪：%s\n", *dir)
	return nil
}

// loadOrCreateSigningKey 在正式文件尚不存在时优先沿用旧版 compose 的签发私钥。
// 旧文件无效时必须失败关闭，避免静默生成新密钥后让既有签名全部失效。
func loadOrCreateSigningKey(path, legacyPath string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return decodeSigningKey(raw)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	var priv ed25519.PrivateKey
	if legacyPath != "" {
		legacyRaw, legacyErr := os.ReadFile(legacyPath)
		switch {
		case legacyErr == nil:
			priv, err = decodeSigningKey(legacyRaw)
			if err != nil {
				return nil, fmt.Errorf("legacy signing key: %w", err)
			}
		case !os.IsNotExist(legacyErr):
			return nil, legacyErr
		}
	}
	if len(priv) == 0 {
		_, priv, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return loadOrCreateSigningKey(path, "")
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString(hex.EncodeToString(priv) + "\n"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return priv, nil
}

func decodeSigningKey(raw []byte) (ed25519.PrivateKey, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("stored signing key is invalid")
	}
	return ed25519.PrivateKey(decoded), nil
}

func writePublicKey(path string, pub ed25519.PublicKey) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".pubkey-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(hex.EncodeToString(pub) + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
