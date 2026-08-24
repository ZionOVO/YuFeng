package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"yufeng/lib/kernel"
)

func runTLS(args []string) error {
	fs := flag.NewFlagSet("tls", flag.ContinueOnError)
	dir := fs.String("out", ".tmp/tls", "输出目录")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		*dir = fs.Arg(0)
	}
	// compose 的 keys 服务每次镜像更新都会重跑；已有可互验物料必须沿用，
	// 否则磁盘权威轮换而中台进程仍握旧证书，数据面验签会 ECDSA 失败。
	if bundle, err := kernel.ExistingTLSBundle(*dir); err == nil {
		if err := writeTLSClientPublicKey(*dir, bundle.ClientCert); err != nil {
			return err
		}
		fmt.Printf("沿用已有双向 TLS 物料：%s\n", *dir)
		return nil
	}
	bundle, err := kernel.GenerateTLSBundle([]string{"localhost", "brain"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		return err
	}
	if err := kernel.WriteTLSBundle(*dir, bundle); err != nil {
		return err
	}
	if err := writeTLSClientPublicKey(*dir, bundle.ClientCert); err != nil {
		return err
	}
	fmt.Printf("已生成双向 TLS 物料：%s\n", *dir)
	return nil
}

func writeTLSClientPublicKey(dir string, certificatePEM []byte) error {
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return errors.New("client certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "client-public.pem"), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), 0o644)
}

func addTLSFlags(fs *flag.FlagSet) (ca, cert, key *string) {
	ca = fs.String("tls-ca", os.Getenv("YUFENG_TLS_CA"), "中台 TLS 权威")
	cert = fs.String("tls-cert", os.Getenv("YUFENG_TLS_CERT"), "客户端证书")
	key = fs.String("tls-key", os.Getenv("YUFENG_TLS_KEY"), "客户端私钥")
	return ca, cert, key
}

func brainHTTP(ca, cert, key string) (*http.Client, error) {
	if ca == "" && cert == "" && key == "" {
		return http.DefaultClient, nil
	}
	if ca == "" || cert == "" || key == "" {
		return nil, errors.New("tls-ca tls-cert and tls-key are required together")
	}
	return kernel.HTTPClient(ca, cert, key)
}
