package kernel

import (
	"crypto/ed25519"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// SignUnitListenPlan 用私钥签发单元监听计划。
// 签名覆盖排除 Signature 后的确定性 proto 字节。
func SignUnitListenPlan(plan *artifactv1.UnitListenPlan, key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid private key length %d", len(key))
	}
	if plan == nil {
		return errors.New("unit listen plan is nil")
	}
	if plan.Signature != nil && len(plan.Signature.Sig) > 0 {
		return errors.New("unit listen plan is already signed")
	}
	raw, err := unitListenPlanBytes(plan)
	if err != nil {
		return err
	}
	plan.Signature = &artifactv1.Signature{
		KeyId:    keyID(key),
		Sig:      ed25519.Sign(key, raw),
		SignedAt: timestamppb.Now(),
	}
	return nil
}

// SignUnitListenPlanWithSigner 用生产签名器签发单元监听计划。
func SignUnitListenPlanWithSigner(plan *artifactv1.UnitListenPlan, signer Signer) error {
	if signer == nil {
		return errors.New("signer is nil")
	}
	if plan == nil {
		return errors.New("unit listen plan is nil")
	}
	if plan.Signature != nil && len(plan.Signature.Sig) > 0 {
		return errors.New("unit listen plan is already signed")
	}
	raw, err := unitListenPlanBytes(plan)
	if err != nil {
		return err
	}
	var sig []byte
	if typed, ok := signer.(typedSigner); ok {
		envelope, marshalErr := proto.MarshalOptions{Deterministic: true}.Marshal(plan)
		if marshalErr != nil {
			return marshalErr
		}
		sig, err = typed.SignTyped(SignOperationListenPlan, envelope)
	} else {
		sig, err = signer.Sign(raw)
	}
	if err != nil {
		return fmt.Errorf("sign unit listen plan: %w", err)
	}
	plan.Signature = &artifactv1.Signature{KeyId: signer.KeyID(), Sig: sig, SignedAt: timestamppb.Now()}
	return nil
}

// VerifyUnitListenPlan 校验单元监听计划的签名与密钥指纹。
func VerifyUnitListenPlan(plan *artifactv1.UnitListenPlan, pub ed25519.PublicKey) error {
	if plan == nil {
		return errors.New("unit listen plan is nil")
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key length %d", len(pub))
	}
	if plan.Signature == nil || len(plan.Signature.Sig) == 0 {
		return errors.New("unit listen plan signature is missing")
	}
	if plan.Signature.KeyId != KeyID(pub) {
		return errors.New("unit listen plan key id does not match public key")
	}
	raw, err := unitListenPlanBytes(plan)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, raw, plan.Signature.Sig) {
		return errors.New("unit listen plan signature verification failed")
	}
	return nil
}

// unitListenPlanBytes 返回排除签名后的确定性监听计划编码。
func unitListenPlanBytes(plan *artifactv1.UnitListenPlan) ([]byte, error) {
	clone := proto.Clone(plan).(*artifactv1.UnitListenPlan)
	clone.Signature = nil
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	if err != nil {
		return nil, fmt.Errorf("marshal unit listen plan: %w", err)
	}
	return raw, nil
}
