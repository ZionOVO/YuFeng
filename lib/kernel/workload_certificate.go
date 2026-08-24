package kernel

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

// WorkloadCertificate 是独立工作负载证书机构签发的短期客户端证书。
type WorkloadCertificate struct {
	Certificate string
	Chain       string
	ExpiresAt   time.Time
}

// WorkloadCertificateIssuer 只暴露证书签发，不暴露证书机构私钥。
type WorkloadCertificateIssuer interface {
	Issue(workerID, certificateRequest string, ttl time.Duration) (WorkloadCertificate, error)
}

type workloadCertificateAuthority struct {
	certificate    *x509.Certificate
	certificatePEM string
	privateKey     ed25519.PrivateKey
}

// LoadOrCreateWorkloadCertificateAuthority 在独立签发进程内加载或创建工作负载证书机构。
func LoadOrCreateWorkloadCertificateAuthority(keyPath, certificatePath string) (*workloadCertificateAuthority, error) {
	keyPEM, keyErr := os.ReadFile(keyPath)
	certificatePEM, certificateErr := os.ReadFile(certificatePath)
	if keyErr == nil && certificateErr == nil {
		block, _ := pem.Decode(keyPEM)
		if block == nil {
			return nil, errors.New("workload ca private key is invalid")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		privateKey, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("workload ca private key type is invalid")
		}
		certificateBlock, _ := pem.Decode(certificatePEM)
		if certificateBlock == nil {
			return nil, errors.New("workload ca certificate is invalid")
		}
		certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
		if err != nil {
			return nil, err
		}
		certificatePublicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
		if !ok || !bytes.Equal(certificatePublicKey, privateKey.Public().(ed25519.PublicKey)) || !certificate.IsCA ||
			certificate.KeyUsage&x509.KeyUsageCertSign == 0 || certificate.CheckSignatureFrom(certificate) != nil {
			return nil, errors.New("workload ca key and certificate do not form a valid authority")
		}
		now := time.Now().UTC()
		if now.Before(certificate.NotBefore) || !now.Before(certificate.NotAfter) {
			return nil, errors.New("workload ca certificate is not currently valid")
		}
		return &workloadCertificateAuthority{certificate: certificate, certificatePEM: string(certificatePEM), privateKey: privateKey}, nil
	}
	if !errors.Is(keyErr, os.ErrNotExist) || !errors.Is(certificateErr, os.ErrNotExist) {
		return nil, errors.New("workload ca key and certificate must both exist or both be absent")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	serial, err := randomCertificateSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "yufeng workload ca"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	certificateOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeExclusiveFile(keyPath, keyOut, 0o600); err != nil {
		return nil, err
	}
	if err := writeExclusiveFile(certificatePath, certificateOut, 0o644); err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &workloadCertificateAuthority{certificate: certificate, certificatePEM: string(certificateOut), privateKey: privateKey}, nil
}

func writeExclusiveFile(path string, content []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (a *workloadCertificateAuthority) Issue(workerID, certificateRequest string, ttl time.Duration) (WorkloadCertificate, error) {
	if strings.TrimSpace(workerID) == "" || ttl <= 0 || ttl > 24*time.Hour {
		return WorkloadCertificate{}, errors.New("workload certificate binding is invalid")
	}
	request, _, err := parseWorkloadCertificateRequest(workerID, certificateRequest)
	if err != nil {
		return WorkloadCertificate{}, err
	}
	serial, err := randomCertificateSerial()
	if err != nil {
		return WorkloadCertificate{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: workerID},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(ttl), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, request.PublicKey, a.privateKey)
	if err != nil {
		return WorkloadCertificate{}, err
	}
	return WorkloadCertificate{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), Chain: a.certificatePEM, ExpiresAt: template.NotAfter}, nil
}

// ValidateWorkloadCertificateRequest 校验客户端证书请求与登记公钥完全一致并返回公钥指纹。
func ValidateWorkloadCertificateRequest(workerID, certificateRequest, publicKeyPEM string) (string, error) {
	_, requestPublicDER, err := parseWorkloadCertificateRequest(workerID, certificateRequest)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil || block.Type != "PUBLIC KEY" {
		return "", errors.New("workload public key is invalid")
	}
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(hex.EncodeToString(publicDER), hex.EncodeToString(requestPublicDER)) {
		return "", errors.New("workload certificate request public key mismatch")
	}
	sum := sha256.Sum256(requestPublicDER)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func parseWorkloadCertificateRequest(workerID, certificateRequest string) (*x509.CertificateRequest, []byte, error) {
	block, _ := pem.Decode([]byte(certificateRequest))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, nil, errors.New("workload certificate request is invalid")
	}
	request, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || request.CheckSignature() != nil {
		return nil, nil, errors.New("workload certificate request signature is invalid")
	}
	if request.Subject.CommonName != workerID {
		return nil, nil, errors.New("workload certificate request subject mismatch")
	}
	publicDER, err := x509.MarshalPKIXPublicKey(request.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return request, publicDER, nil
}

func randomCertificateSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}

// WorkloadCertificateSHA256 返回证书原始 DER 的安全哈希算法 256 位摘要。
func WorkloadCertificateSHA256(certificatePEM string) (string, error) {
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("workload certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:]), nil
}

type workloadIssueRequest struct {
	WorkerID           string        `json:"worker_id"`
	CertificateRequest string        `json:"certificate_request"`
	TTL                time.Duration `json:"ttl"`
}

type workloadIssueResponse struct {
	Certificate WorkloadCertificate `json:"certificate"`
	Error       string              `json:"error,omitempty"`
}

const workloadIssuerMessageLimit = 1 << 20

// ServeWorkloadCertificateIssuer 在独立 Unix 套接字上签发短期工作负载证书。
func ServeWorkloadCertificateIssuer(ln net.Listener, issuer WorkloadCertificateIssuer) error {
	return ServeWorkloadCertificateIssuerForUID(ln, issuer, -1)
}

// ServeWorkloadCertificateIssuerForUID 只接受指定本机用户的类型化证书签发请求。
func ServeWorkloadCertificateIssuerForUID(ln net.Listener, issuer WorkloadCertificateIssuer, allowedUID int) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close() //nolint:errcheck // 单次证书签发应答完成后尽力释放短连接。
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			if err := verifyUnixPeerUID(c, allowedUID); err != nil {
				return
			}
			raw, err := io.ReadAll(io.LimitReader(c, workloadIssuerMessageLimit+1))
			if err != nil || len(raw) > workloadIssuerMessageLimit {
				_ = json.NewEncoder(c).Encode(workloadIssueResponse{Error: "invalid request"})
				return
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			var request workloadIssueRequest
			if err := decoder.Decode(&request); err != nil {
				_ = json.NewEncoder(c).Encode(workloadIssueResponse{Error: "invalid request"})
				return
			}
			var trailing json.RawMessage
			if err := decoder.Decode(&trailing); err != io.EOF {
				_ = json.NewEncoder(c).Encode(workloadIssueResponse{Error: "invalid request"})
				return
			}
			certificate, err := issuer.Issue(request.WorkerID, request.CertificateRequest, request.TTL)
			response := workloadIssueResponse{Certificate: certificate}
			if err != nil {
				response.Error = err.Error()
			}
			_ = json.NewEncoder(c).Encode(response)
		}(conn)
	}
}

// SocketWorkloadCertificateIssuer 经独立 Unix 套接字请求工作负载证书。
type SocketWorkloadCertificateIssuer struct{ address string }

// NewSocketWorkloadCertificateIssuer 构造不持证书机构私钥的签发客户端。
func NewSocketWorkloadCertificateIssuer(address string) (*SocketWorkloadCertificateIssuer, error) {
	if strings.TrimSpace(address) == "" {
		return nil, errors.New("workload issuer socket is required")
	}
	return &SocketWorkloadCertificateIssuer{address: address}, nil
}

// Issue 请求签发一次绑定 worker 标识与证书请求的短期证书。
func (s *SocketWorkloadCertificateIssuer) Issue(workerID, certificateRequest string, ttl time.Duration) (WorkloadCertificate, error) {
	conn, err := net.DialTimeout("unix", s.address, time.Second)
	if err != nil {
		return WorkloadCertificate{}, err
	}
	defer conn.Close() //nolint:errcheck // 单次签发结束后尽力释放短连接。
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(workloadIssueRequest{WorkerID: workerID, CertificateRequest: certificateRequest, TTL: ttl}); err != nil {
		return WorkloadCertificate{}, err
	}
	unixConnection, ok := conn.(*net.UnixConn)
	if !ok {
		return WorkloadCertificate{}, errors.New("workload issuer connection is not a Unix socket")
	}
	if err := unixConnection.CloseWrite(); err != nil {
		return WorkloadCertificate{}, err
	}
	decoder := json.NewDecoder(bufio.NewReader(conn))
	decoder.DisallowUnknownFields()
	var response workloadIssueResponse
	if err := decoder.Decode(&response); err != nil {
		return WorkloadCertificate{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return WorkloadCertificate{}, errors.New("workload issuer returned trailing response data")
	}
	if response.Error != "" {
		return WorkloadCertificate{}, fmt.Errorf("workload issuer: %s", response.Error)
	}
	if err := validateIssuedWorkloadCertificate(workerID, certificateRequest, response.Certificate, ttl); err != nil {
		return WorkloadCertificate{}, fmt.Errorf("workload issuer returned an invalid certificate: %w", err)
	}
	return response.Certificate, nil
}

func validateIssuedWorkloadCertificate(workerID, certificateRequest string, issued WorkloadCertificate, ttl time.Duration) error {
	_, requestPublicDER, err := parseWorkloadCertificateRequest(workerID, certificateRequest)
	if err != nil {
		return err
	}
	block, remaining := pem.Decode([]byte(issued.Certificate))
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(remaining)) != "" {
		return errors.New("workload certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if certificate.Subject.CommonName != workerID || !bytes.Equal(certificate.RawSubjectPublicKeyInfo, requestPublicDER) {
		return errors.New("workload certificate binding does not match the request")
	}
	expiryDelta := issued.ExpiresAt.Sub(certificate.NotAfter)
	if expiryDelta < 0 {
		expiryDelta = -expiryDelta
	}
	if issued.ExpiresAt.IsZero() || expiryDelta >= time.Second || ttl <= 0 || certificate.NotAfter.After(time.Now().UTC().Add(ttl+time.Minute)) {
		return errors.New("workload certificate expiry does not match the request")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(issued.Chain)) {
		return errors.New("workload certificate chain is invalid")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return err
	}
	return nil
}
