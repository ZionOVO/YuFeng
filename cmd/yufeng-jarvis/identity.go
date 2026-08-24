package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	jarvisPrivateKeyFile = "identity.key"
	jarvisPublicKeyFile  = "identity.pub"
)

// loadOrCreateJarvisPublicKey 读取或原子生成 Jarvis 的本机 Ed25519 身份密钥。
func loadOrCreateJarvisPublicKey(stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		return "", errors.New("state-dir is required when public-key is not provided")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return "", err
	}
	privatePath := filepath.Join(stateDir, jarvisPrivateKeyFile)
	publicPath := filepath.Join(stateDir, jarvisPublicKeyFile)
	privateRaw, privateErr := os.ReadFile(privatePath)
	publicRaw, publicErr := os.ReadFile(publicPath)
	if privateErr == nil && publicErr == nil {
		if err := validateJarvisKeyPair(privateRaw, publicRaw); err != nil {
			return "", err
		}
		return string(publicRaw), nil
	}
	if !errors.Is(privateErr, os.ErrNotExist) || !errors.Is(publicErr, os.ErrNotExist) {
		return "", errors.New("jarvis identity material must be complete or absent")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	if err := writeJarvisIdentityFile(privatePath, privatePEM, 0o600); err != nil {
		return "", err
	}
	if err := writeJarvisIdentityFile(publicPath, publicPEM, 0o600); err != nil {
		_ = os.Remove(privatePath)
		return "", err
	}
	return string(publicPEM), nil
}

func validateJarvisKeyPair(privateRaw, publicRaw []byte) error {
	privateBlock, _ := pem.Decode(privateRaw)
	publicBlock, _ := pem.Decode(publicRaw)
	if privateBlock == nil || privateBlock.Type != "PRIVATE KEY" || publicBlock == nil || publicBlock.Type != "PUBLIC KEY" {
		return errors.New("jarvis identity material is invalid")
	}
	privateValue, err := x509.ParsePKCS8PrivateKey(privateBlock.Bytes)
	if err != nil {
		return err
	}
	privateKey, ok := privateValue.(ed25519.PrivateKey)
	if !ok {
		return errors.New("jarvis identity private key is not ed25519")
	}
	publicValue, err := x509.ParsePKIXPublicKey(publicBlock.Bytes)
	if err != nil {
		return err
	}
	publicKey, ok := publicValue.(ed25519.PublicKey)
	if !ok || !privateKey.Public().(ed25519.PublicKey).Equal(publicKey) {
		return errors.New("jarvis identity key pair does not match")
	}
	return nil
}

func writeJarvisIdentityFile(path string, raw []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
