package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"yufeng/agents/runtime"
	"yufeng/proto/gen/workerv1"
	"yufeng/proto/gen/workerv1/workerv1connect"
)

const workerCertificateRenewBefore = 6 * time.Hour

func seedWorkerTLSMaterial(stateDir, certificateSource, keySource string) error {
	if certificateSource == "" && keySource == "" {
		return nil
	}
	if strings.TrimSpace(certificateSource) == "" || strings.TrimSpace(keySource) == "" {
		return errors.New("worker tls seed certificate and key are required together")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	certificatePath := workerClientCertificatePath(stateDir)
	keyPath := workerClientKeyPath(stateDir)
	certificateRaw, certificateErr := os.ReadFile(certificatePath)
	keyRaw, keyErr := os.ReadFile(keyPath)
	if certificateErr == nil && keyErr == nil {
		if _, err := tls.X509KeyPair(certificateRaw, keyRaw); err != nil {
			return fmt.Errorf("validate persisted worker tls material: %w", err)
		}
		return nil
	}
	if !errors.Is(certificateErr, os.ErrNotExist) || !errors.Is(keyErr, os.ErrNotExist) {
		return errors.New("worker tls material must be complete or absent")
	}
	certificateRaw, err := os.ReadFile(certificateSource)
	if err != nil {
		return err
	}
	keyRaw, err = os.ReadFile(keySource)
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(certificateRaw, keyRaw); err != nil {
		return fmt.Errorf("validate worker tls seed material: %w", err)
	}
	if err := writeEnrollmentFile(keyPath, keyRaw, 0o600); err != nil {
		return err
	}
	if err := writeEnrollmentFile(certificatePath, certificateRaw, 0o600); err != nil {
		return err
	}
	return nil
}

func workerCertificateRenewer(httpClient *http.Client, client workerv1connect.WorkerServiceClient, workerID, certificatePath, keyPath, refreshPath string, session *runtime.AccessSession) func(context.Context) error {
	return workerCertificateRenewerWithSaver(httpClient, client, workerID, certificatePath, keyPath, refreshPath, session, saveWorkerRefresh)
}

func workerCertificateRenewerWithSaver(httpClient *http.Client, client workerv1connect.WorkerServiceClient, workerID, certificatePath, keyPath, refreshPath string,
	session *runtime.AccessSession, save func(path, workerID, refresh string) error) func(context.Context) error {
	return func(ctx context.Context) error {
		expiresAt, err := workerCertificateExpiry(certificatePath)
		if err != nil {
			return err
		}
		if time.Until(expiresAt) > workerCertificateRenewBefore {
			return nil
		}
		certificateRequest, err := workerCertificateRequest(workerID, keyPath)
		if err != nil {
			return err
		}
		req := connect.NewRequest(&workerv1.RenewWorkerCertificateRequest{WorkerId: workerID, CertificateRequest: certificateRequest})
		access, refresh := session.Tokens()
		req.Header().Set("Authorization", "Bearer "+access)
		response, err := client.RenewWorkerCertificate(ctx, req)
		if err != nil {
			return err
		}
		bundle := response.Msg.GetClientCertificate() + response.Msg.GetCertificateChain()
		if err := replaceWorkerCertificate(certificatePath, []byte(bundle)); err != nil {
			return err
		}
		httpClient.CloseIdleConnections()
		refreshed, err := client.RefreshWorkerAccessToken(ctx, connect.NewRequest(&workerv1.RefreshWorkerAccessTokenRequest{
			WorkerId: workerID, RefreshToken: refresh,
		}))
		if err != nil {
			return err
		}
		access, rotatedRefresh := refreshed.Msg.GetAccessToken(), refreshed.Msg.GetRefreshToken()
		if strings.TrimSpace(access) == "" || strings.TrimSpace(rotatedRefresh) == "" {
			return errors.New("worker refresh response is incomplete")
		}
		if save == nil {
			return session.FailRefreshPersistence(errors.New("worker refresh saver is not configured"))
		}
		if err := save(refreshPath, workerID, rotatedRefresh); err != nil {
			return session.FailRefreshPersistence(err)
		}
		session.SetTokens(access, rotatedRefresh)
		return nil
	}
}

func workerCertificateExpiry(path string) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, errors.New("worker client certificate is invalid")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, err
	}
	return certificate.NotAfter, nil
}

func workerCertificateRequest(workerID, keyPath string) (string, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("worker client private key is invalid")
	}
	var parsed any
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		parsed, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return "", errors.New("worker client private key type is invalid")
	}
	if err != nil {
		return "", err
	}
	privateKey, ok := parsed.(crypto.Signer)
	if !ok {
		return "", errors.New("worker client private key type is invalid")
	}
	request, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: strings.TrimSpace(workerID)}}, privateKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: request})), nil
}

func replaceWorkerCertificate(path string, content []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".client-certificate-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath) //nolint:errcheck // 原子替换成功后临时路径已不存在。
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
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
	return replaceAgentdFile(temporaryPath, path)
}
