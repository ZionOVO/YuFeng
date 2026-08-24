package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"yufeng/lib/kernel"
)

func runSigner(args []string) error {
	fs := flag.NewFlagSet("signer", flag.ContinueOnError)
	socket := fs.String("socket", "/sign/sign.sock", "unix 套接字路径")
	keyFile := fs.String("key", "", "Ed25519 私钥 hex 文件")
	workloadSocket := fs.String("workload-socket", "/sign/workload-sign.sock", "工作负载证书签发 Unix 套接字")
	workloadCAKey := fs.String("workload-ca-key", "/sign/workload-ca.key", "独立工作负载证书机构私钥")
	workloadCACert := fs.String("workload-ca-cert", "/sign/workload-ca.crt", "独立工作负载证书机构证书")
	trustedClientCA := fs.String("trusted-client-ca", "", "原有智能代理客户端证书机构；仅用于生成信任证书包")
	clientCABundle := fs.String("client-ca-bundle", "/sign/client-ca-bundle.crt", "中台读取的客户端信任证书包")
	allowedUID := fs.Int("allowed-uid", -1, "允许连接签名套接字的本机用户标识；默认签名进程用户")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" {
		return errors.New("key is required")
	}
	raw, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	priv, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return err
	}
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("signing key length %d invalid", len(priv))
	}
	inner, err := kernel.NewMemorySigner(ed25519.PrivateKey(priv))
	if err != nil {
		return err
	}
	workloadIssuer, err := kernel.LoadOrCreateWorkloadCertificateAuthority(*workloadCAKey, *workloadCACert)
	if err != nil {
		return err
	}
	if err := writeClientCABundle(*clientCABundle, *trustedClientCA, *workloadCACert); err != nil {
		return err
	}
	if *allowedUID < 0 {
		*allowedUID = signerProcessUID()
	}
	if *allowedUID < 0 {
		return errors.New("signer peer user validation is unavailable")
	}
	if err := removeStaleSocket(*socket); err != nil {
		return err
	}
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		return err
	}
	defer ln.Close() //nolint:errcheck // 签发服务退出后仅做监听套接字尽力清理。
	if err := removeStaleSocket(*workloadSocket); err != nil {
		return err
	}
	workloadListener, err := net.Listen("unix", *workloadSocket)
	if err != nil {
		return err
	}
	defer workloadListener.Close() //nolint:errcheck // 签发服务退出后仅做监听套接字尽力清理。
	if err := os.Chmod(*socket, 0o660); err != nil {
		return err
	}
	if err := os.Chmod(*workloadSocket, 0o660); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = workloadListener.Close()
	}()
	fmt.Printf("signer listening %s workload=%s key_id=%s\n", *socket, *workloadSocket, inner.KeyID())
	errCh := make(chan error, 2)
	go func() { errCh <- kernel.ServeMemorySignerForUID(ln, inner, *allowedUID) }()
	go func() {
		errCh <- kernel.ServeWorkloadCertificateIssuerForUID(workloadListener, workloadIssuer, *allowedUID)
	}()
	err = <-errCh
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("signer socket path exists and is not a socket")
	}
	return os.Remove(path)
}

func writeClientCABundle(path string, sources ...string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var bundle []byte
	for _, source := range sources {
		if strings.TrimSpace(source) == "" {
			continue
		}
		raw, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		bundle = append(bundle, raw...)
		if len(bundle) > 0 && bundle[len(bundle)-1] != '\n' {
			bundle = append(bundle, '\n')
		}
	}
	if len(bundle) == 0 {
		return errors.New("at least one client certificate authority is required")
	}
	return replaceClientCABundle(path, bundle, os.Rename)
}

func replaceClientCABundle(path string, bundle []byte, rename func(string, string) error) error {
	directoryPath := filepath.Dir(path)
	temporary, err := os.CreateTemp(directoryPath, ".client-ca-bundle-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck // Rename 成功后临时路径已不存在，失败时只做尽力清理。
	if err := temporary.Chmod(0o644); err != nil {
		return errors.Join(err, temporary.Close())
	}
	written, writeErr := temporary.Write(bundle)
	if writeErr == nil && written != len(bundle) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		return errors.Join(writeErr, temporary.Close())
	}
	if err := temporary.Sync(); err != nil {
		return errors.Join(err, temporary.Close())
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
