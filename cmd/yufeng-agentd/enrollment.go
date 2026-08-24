package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

type enrollmentMaterial struct {
	PublicKey           string
	CertificateRequest  string
	ActivationPublicKey string
}

func workerClientKeyPath(stateDir string) string { return filepath.Join(stateDir, "client.key") }
func workerClientCertificatePath(stateDir string) string {
	return filepath.Join(stateDir, "client.crt")
}
func workerPublicKeyPath(stateDir string) string { return filepath.Join(stateDir, "worker-public.pem") }
func workerActivationPrivateKeyPath(stateDir string) string {
	return filepath.Join(stateDir, "activation-x25519.key")
}
func workerActivationPublicKeyPath(stateDir string) string {
	return filepath.Join(stateDir, "activation-x25519.pub")
}

func requestWorkerEnrollment(ctx context.Context, client *http.Client, brainURL, workerID, stateDir string) error {
	material, err := loadOrCreateEnrollmentMaterial(stateDir, workerID)
	if err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return err
	}
	worker := workerv1connect.NewWorkerServiceClient(client, brainURL)
	response, err := worker.RequestWorkerEnrollment(ctx, connect.NewRequest(&workerv1.RequestWorkerEnrollmentRequest{
		WorkerId: workerID, WorkerKind: workerv1.WorkerKind_WORKER_KIND_RUN_SUPERVISOR,
		WorkerPublicKey: material.PublicKey, CertificateRequest: material.CertificateRequest,
		Hostname: hostname, OperatingSystem: goruntime.GOOS, Architecture: goruntime.GOARCH,
		SandboxCapabilities: runtime.VerifiedSandboxCapabilities(), ActivationPublicKey: material.ActivationPublicKey,
		Version: version, MaxConcurrency: 1, LogicalCpuCapacity: int32(goruntime.NumCPU()),
	}))
	if err != nil {
		return err
	}
	receipt := map[string]string{"enrollment_id": response.Msg.GetEnrollmentId(), "public_key_fingerprint": response.Msg.GetPublicKeyFingerprint(), "state": response.Msg.GetState()}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stateDir, "enrollment.json"), raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("worker enrollment pending id=%s fingerprint=%s\n", response.Msg.GetEnrollmentId(), response.Msg.GetPublicKeyFingerprint())
	return nil
}

func loadOrCreateEnrollmentMaterial(stateDir, workerID string) (enrollmentMaterial, error) {
	if strings.TrimSpace(stateDir) == "" || strings.TrimSpace(workerID) == "" {
		return enrollmentMaterial{}, errors.New("worker state directory and id are required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return enrollmentMaterial{}, err
	}
	keyPath := workerClientKeyPath(stateDir)
	publicPath := workerPublicKeyPath(stateDir)
	csrPath := filepath.Join(stateDir, "client.csr")
	_, keyErr := os.ReadFile(keyPath)
	publicRaw, publicErr := os.ReadFile(publicPath)
	csrRaw, csrErr := os.ReadFile(csrPath)
	if keyErr == nil && publicErr == nil && csrErr == nil {
		activationPublicKey, err := loadOrCreateActivationKey(stateDir)
		if err != nil {
			return enrollmentMaterial{}, err
		}
		return enrollmentMaterial{PublicKey: string(publicRaw), CertificateRequest: string(csrRaw), ActivationPublicKey: activationPublicKey}, nil
	}
	if !errors.Is(keyErr, os.ErrNotExist) || !errors.Is(publicErr, os.ErrNotExist) || !errors.Is(csrErr, os.ErrNotExist) {
		return enrollmentMaterial{}, errors.New("worker enrollment material must be complete or absent")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return enrollmentMaterial{}, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return enrollmentMaterial{}, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return enrollmentMaterial{}, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: workerID}}, privateKey)
	if err != nil {
		return enrollmentMaterial{}, err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	if err := writeEnrollmentFile(keyPath, privatePEM, 0o600); err != nil {
		return enrollmentMaterial{}, err
	}
	if err := writeEnrollmentFile(publicPath, publicPEM, 0o600); err != nil {
		return enrollmentMaterial{}, err
	}
	if err := writeEnrollmentFile(csrPath, csrPEM, 0o600); err != nil {
		return enrollmentMaterial{}, err
	}
	activationPublicKey, err := loadOrCreateActivationKey(stateDir)
	if err != nil {
		return enrollmentMaterial{}, err
	}
	return enrollmentMaterial{PublicKey: string(publicPEM), CertificateRequest: string(csrPEM), ActivationPublicKey: activationPublicKey}, nil
}

func loadOrCreateActivationKey(stateDir string) (string, error) {
	privatePath, publicPath := workerActivationPrivateKeyPath(stateDir), workerActivationPublicKeyPath(stateDir)
	privateRaw, privateErr := os.ReadFile(privatePath)
	publicRaw, publicErr := os.ReadFile(publicPath)
	if privateErr == nil && publicErr == nil {
		privateBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(privateRaw)))
		if err != nil {
			return "", errors.New("worker activation private key is invalid")
		}
		privateKey, err := ecdh.X25519().NewPrivateKey(privateBytes)
		if err != nil || base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()) != strings.TrimSpace(string(publicRaw)) {
			return "", errors.New("worker activation key pair is invalid")
		}
		return strings.TrimSpace(string(publicRaw)), nil
	}
	if !errors.Is(privateErr, os.ErrNotExist) || !errors.Is(publicErr, os.ErrNotExist) {
		return "", errors.New("worker activation key pair must be complete or absent")
	}
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	privateEncoded := []byte(base64.RawURLEncoding.EncodeToString(privateKey.Bytes()))
	publicEncoded := []byte(base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes()))
	if err := writeEnrollmentFile(privatePath, privateEncoded, 0o600); err != nil {
		return "", err
	}
	if err := writeEnrollmentFile(publicPath, publicEncoded, 0o600); err != nil {
		return "", err
	}
	return string(publicEncoded), nil
}

func writeEnrollmentFile(path string, content []byte, mode os.FileMode) error {
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
	if err := file.Close(); err != nil {
		return err
	}
	return syncAgentdDirectory(path)
}
