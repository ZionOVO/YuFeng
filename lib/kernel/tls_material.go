package kernel

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// TLSBundle 是一套自签证书权威 + 服务器证书 + 客户端证书。
type TLSBundle struct {
	CACert, CAKey         []byte
	ServerCert, ServerKey []byte
	ClientCert, ClientKey []byte
}

// GenerateTLSBundle 生成测试与容器编排使用的相互传输层安全协议物料。
func GenerateTLSBundle(serverDNS []string, serverIPs []net.IP) (*TLSBundle, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "yufeng-ca", Organization: []string{"yufeng"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	if len(serverDNS) == 0 {
		serverDNS = []string{"localhost", "brain"}
	}
	if len(serverIPs) == 0 {
		serverIPs = []net.IP{net.ParseIP("127.0.0.1")}
	}
	srvTpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "yufeng-brain"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     append([]string{}, serverDNS...),
		IPAddresses:  append([]net.IP{}, serverIPs...),
	}
	srvDER, err := x509.CreateCertificate(rand.Reader, srvTpl, caTpl, &serverKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	cliKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	cliTpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "yufeng-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cliDER, err := x509.CreateCertificate(rand.Reader, cliTpl, caTpl, &cliKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	caKeyPEM, err := ecdsaPEM(caKey)
	if err != nil {
		return nil, err
	}
	srvKeyPEM, err := ecdsaPEM(serverKey)
	if err != nil {
		return nil, err
	}
	cliKeyPEM, err := ecdsaPEM(cliKey)
	if err != nil {
		return nil, err
	}
	return &TLSBundle{
		CACert:     certPEM(caDER),
		CAKey:      caKeyPEM,
		ServerCert: certPEM(srvDER),
		ServerKey:  srvKeyPEM,
		ClientCert: certPEM(cliDER),
		ClientKey:  cliKeyPEM,
	}, nil
}

// WriteTLSBundle 把证书与私钥写到目录。
func WriteTLSBundle(dir string, b *TLSBundle) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	files := map[string][]byte{
		"ca.crt": b.CACert, "ca.key": b.CAKey,
		"server.crt": b.ServerCert, "server.key": b.ServerKey,
		"client.crt": b.ClientCert, "client.key": b.ClientKey,
	}
	for name, raw := range files {
		mode := os.FileMode(0o644)
		if len(name) > 4 && name[len(name)-4:] == ".key" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, mode); err != nil {
			return err
		}
	}
	return nil
}

// ExistingTLSBundle 读取目录中已能互验的相互传输层安全协议物料。
// 权威、服务端证书、客户端证书必须齐全，且后两者由该权威签发且未过期。
func ExistingTLSBundle(dir string) (*TLSBundle, error) {
	read := func(name string) ([]byte, error) {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			return nil, fmt.Errorf("%s is empty", name)
		}
		return raw, nil
	}
	b := &TLSBundle{}
	var err error
	if b.CACert, err = read("ca.crt"); err != nil {
		return nil, err
	}
	if b.CAKey, err = read("ca.key"); err != nil {
		return nil, err
	}
	if b.ServerCert, err = read("server.crt"); err != nil {
		return nil, err
	}
	if b.ServerKey, err = read("server.key"); err != nil {
		return nil, err
	}
	if b.ClientCert, err = read("client.crt"); err != nil {
		return nil, err
	}
	if b.ClientKey, err = read("client.key"); err != nil {
		return nil, err
	}
	if err := verifyIssuedBy(b.CACert, b.ServerCert); err != nil {
		return nil, fmt.Errorf("server cert: %w", err)
	}
	if err := verifyIssuedBy(b.CACert, b.ClientCert); err != nil {
		return nil, fmt.Errorf("client cert: %w", err)
	}
	return b, nil
}

// verifyIssuedBy 校验证书由指定证书颁发机构签发且当前仍在有效期内。
func verifyIssuedBy(caPEM, certPEM []byte) error {
	ca, err := parseCertPEM(caPEM)
	if err != nil {
		return err
	}
	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return err
	}
	if err := cert.CheckSignatureFrom(ca); err != nil {
		return err
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return fmt.Errorf("certificate is not currently valid")
	}
	return nil
}

// parseCertPEM 从隐私增强邮件编码中解析一张可供校验的证书。
func parseCertPEM(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("pem is not a certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// loadClientCA 从文件加载客户端证书颁发机构并构造信任池。
func loadClientCA(clientCAFile string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("client ca: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, fmt.Errorf("client ca is invalid")
	}
	return pool, nil
}

// ServerMTLSConfig 要求并校验客户端证书。
func ServerMTLSConfig(clientCAFile string) (*tls.Config, error) {
	pool, err := loadClientCA(clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
	}, nil
}

// ServerOptionalClientCertConfig 校验已出示的客户端证书；未出示时仍完成握手。
// 业务口与控制台共用 :9050：浏览器与 curl -k 不持客户端证书；贾维斯与数据面必须出示。
func ServerOptionalClientCertConfig(clientCAFile string) (*tls.Config, error) {
	pool, err := loadClientCA(clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  pool,
	}, nil
}

// HTTPClient 构造带证书颁发机构信任根与可选客户端证书的超文本传输协议客户端。
func HTTPClient(caFile, certFile, keyFile string) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("tls ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("tls ca is invalid")
		}
		cfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("tls client certificate and key are both required")
		}
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
			return nil, fmt.Errorf("tls client cert: %w", err)
		}
		// 工作负载证书为 24 小时短证书；每次新握手从磁盘重读，agentd 原子替换后无需重启。
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &certificate, nil
		}
	}
	return &http.Client{
		Timeout: ControlPlaneHTTPTimeout,
		Transport: &http.Transport{
			TLSClientConfig: cfg,
		},
	}, nil
}

// certPEM 把证书的二进制编码转换为隐私增强邮件文本。
func certPEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ecdsaPEM 把椭圆曲线数字签名算法私钥转换为隐私增强邮件文本。
func ecdsaPEM(key *ecdsa.PrivateKey) ([]byte, error) {
	raw, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), nil
}
