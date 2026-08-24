package kernel

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// SignGeneration 签发资产世代信封签名。规范字节排除 envelope_signature。
func SignGeneration(g *artifactv1.AssetGeneration, key ed25519.PrivateKey) error {
	if g == nil {
		return errors.New("generation is nil")
	}
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key length %d", len(key))
	}
	if g.EnvelopeSignature != nil && len(g.EnvelopeSignature.Sig) > 0 {
		return errors.New("generation already signed")
	}
	canon, err := generationCanonical(g)
	if err != nil {
		return err
	}
	g.EnvelopeSignature = &artifactv1.Signature{
		KeyId:    keyID(key),
		Sig:      ed25519.Sign(key, canon),
		SignedAt: timestamppb.Now(),
	}
	return nil
}

// SignGenerationWithSigner 用生产签发器签世代信封。
func SignGenerationWithSigner(g *artifactv1.AssetGeneration, s Signer) error {
	if s == nil {
		return errors.New("signer is nil")
	}
	if g == nil {
		return errors.New("generation is nil")
	}
	if g.EnvelopeSignature != nil && len(g.EnvelopeSignature.Sig) > 0 {
		return errors.New("generation already signed")
	}
	canon, err := generationCanonical(g)
	if err != nil {
		return err
	}
	var sig []byte
	if typed, ok := s.(typedSigner); ok {
		envelope, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(g)
		if marshalErr != nil {
			return marshalErr
		}
		sig, err = typed.SignTyped(SignOperationAssetGeneration, envelope)
	} else {
		sig, err = s.Sign(canon)
	}
	if err != nil {
		return err
	}
	g.EnvelopeSignature = &artifactv1.Signature{KeyId: s.KeyID(), Sig: sig, SignedAt: timestamppb.Now()}
	return nil
}

// VerifyGeneration 校验世代信封签名。
func VerifyGeneration(g *artifactv1.AssetGeneration, pub ed25519.PublicKey) error {
	if g == nil {
		return errors.New("generation is nil")
	}
	if g.EnvelopeSignature == nil || len(g.EnvelopeSignature.Sig) == 0 {
		return errors.New("generation envelope missing signature")
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length %d", len(pub))
	}
	canon, err := generationCanonical(g)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, canon, g.EnvelopeSignature.Sig) {
		return errors.New("generation envelope signature verification failed")
	}
	return nil
}

// generationCanonical 返回排除信封签名后的确定性世代编码。
func generationCanonical(g *artifactv1.AssetGeneration) ([]byte, error) {
	clone := proto.Clone(g).(*artifactv1.AssetGeneration)
	clone.EnvelopeSignature = nil
	return proto.MarshalOptions{Deterministic: true}.Marshal(clone)
}
