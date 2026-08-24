package kernel

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// 制品身份与签名的规范定义（全仓唯一出处）：
//   - 规范字节 = 排除 id 与 signature 后，按确定性 proto 二进制序列化的完整制品信封；
//   - id = "sha256:" + hex(sha256(规范字节))；
//   - 签名覆盖同一份规范字节。
//
// 门禁通过、回放报告写入后，由治理内核计算 id 并签发；
// 数据面只调用 VerifyArtifact 验签（信任边界见 docs/architecture.md §1）。
//
// [制品]: ../../docs/glossary.md#artifact

// ArtifactID 返回制品的全信封内容地址。
// 计算时在副本上清空 id 与 signature，因此签名前后的结果一致。
func ArtifactID(a *artifactv1.Artifact) (string, error) {
	if a == nil {
		return "", errors.New("artifact is nil")
	}
	canonical, err := canonicalBytes(a)
	if err != nil {
		return "", fmt.Errorf("marshal artifact for id: %w", err)
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// VerifyArtifact 校验制品的 id 与 Ed25519 签名，通过返回 nil。
// 不修改传入的制品（在副本上计算规范序列化）。
func VerifyArtifact(a *artifactv1.Artifact, pub ed25519.PublicKey) error {
	if a == nil {
		return errors.New("artifact is nil")
	}
	want, err := ArtifactID(a)
	if err != nil {
		return err
	}
	if a.Id != want {
		return fmt.Errorf("artifact id %s does not match envelope hash (want %s)", a.Id, want)
	}
	if a.Signature == nil || len(a.Signature.Sig) == 0 {
		return fmt.Errorf("artifact %s: missing signature", a.Id)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length %d (want %d)", len(pub), ed25519.PublicKeySize)
	}
	canonical, err := canonicalBytes(a)
	if err != nil {
		return fmt.Errorf("artifact %s: marshal: %w", a.Id, err)
	}
	if !ed25519.Verify(pub, canonical, a.Signature.Sig) {
		return fmt.Errorf("artifact %s: signature verification failed", a.Id)
	}
	return nil
}

// SignArtifact 用私钥就地填充 id 与签名。
// id 为空时自动按全信封哈希补齐；id 非空但与信封不符、或制品已签名时拒绝签发。
func SignArtifact(a *artifactv1.Artifact, key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key length %d", len(key))
	}
	if a == nil {
		return errors.New("artifact is nil")
	}
	if a.Signature != nil && len(a.Signature.Sig) > 0 {
		return fmt.Errorf("artifact %s: already signed, refusing to re-sign", a.Id)
	}
	want, err := ArtifactID(a)
	if err != nil {
		return err
	}
	if a.Id != "" && a.Id != want {
		return fmt.Errorf("artifact id %s does not match envelope hash (want %s), refusing to sign", a.Id, want)
	}
	if a.Id == "" {
		a.Id = want
	}
	canonical, err := canonicalBytes(a)
	if err != nil {
		return fmt.Errorf("marshal for signing: %w", err)
	}
	a.Signature = &artifactv1.Signature{
		KeyId:    keyID(key),
		Sig:      ed25519.Sign(key, canonical),
		SignedAt: timestamppb.Now(),
	}
	return nil
}

// SignArtifactWithSigner 用 Signer 签发制品，供生产套接字签名端使用。
func SignArtifactWithSigner(a *artifactv1.Artifact, s Signer) error {
	if s == nil {
		return errors.New("signer is nil")
	}
	if a == nil {
		return errors.New("artifact is nil")
	}
	if a.Signature != nil && len(a.Signature.Sig) > 0 {
		return fmt.Errorf("artifact %s: already signed, refusing to re-sign", a.Id)
	}
	want, err := ArtifactID(a)
	if err != nil {
		return err
	}
	if a.Id != "" && a.Id != want {
		return fmt.Errorf("artifact id %s does not match envelope hash (want %s), refusing to sign", a.Id, want)
	}
	if a.Id == "" {
		a.Id = want
	}
	canonical, err := canonicalBytes(a)
	if err != nil {
		return fmt.Errorf("marshal for signing: %w", err)
	}
	var sig []byte
	if typed, ok := s.(typedSigner); ok {
		envelope, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(a)
		if marshalErr != nil {
			return marshalErr
		}
		sig, err = typed.SignTyped(SignOperationArtifact, envelope)
	} else {
		sig, err = s.Sign(canonical)
	}
	if err != nil {
		return err
	}
	a.Signature = &artifactv1.Signature{KeyId: s.KeyID(), Sig: sig, SignedAt: timestamppb.Now()}
	return nil
}

// canonicalBytes 返回签名与 id 计算共用的规范序列化：排除 id 与 signature。
func canonicalBytes(a *artifactv1.Artifact) ([]byte, error) {
	clone := proto.Clone(a).(*artifactv1.Artifact)
	clone.Id = ""
	clone.Signature = nil
	return proto.MarshalOptions{Deterministic: true}.Marshal(clone)
}

// KeyID 取公钥的安全哈希算法 256 位十六进制摘要作为完整密钥指纹，避免短前缀碰撞。
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// keyID 从私钥导出公钥并计算完整密钥指纹。
func keyID(key ed25519.PrivateKey) string {
	return KeyID(key.Public().(ed25519.PublicKey))
}
