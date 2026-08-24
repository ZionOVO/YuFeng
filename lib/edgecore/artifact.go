package edgecore

import (
	"crypto/ed25519"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"

	"yufeng/lib/kernel"
)

// 数据面只装载与验签；签发在治理内核（lib/kernel/artifact.go）。
// 文件以 protojson 存储方便人读，签名与身份规范见 kernel 包。
//
// [制品]: ../../docs/glossary.md#artifact

// LoadArtifact 从文件读取制品（protojson 格式）并交治理内核验签。
// 验签失败、id 与信封不符、缺少签名都返回错误——数据面不加载不可信制品。
func LoadArtifact(path string, pub ed25519.PublicKey) (*artifactv1.Artifact, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	var a artifactv1.Artifact
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("parse artifact %s: %w", path, err)
	}
	if err := kernel.VerifyArtifact(&a, pub); err != nil {
		return nil, fmt.Errorf("artifact %s: %w", a.Id, err)
	}
	return &a, nil
}
