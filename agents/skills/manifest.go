package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

const contextLimit = 1 << 20

// Validate 校验技能清单的身份、发布者与所有内容地址。
func Validate(artifact *artifactv1.Artifact) (*toolv1.SkillManifest, error) {
	if artifact == nil || artifact.GetKind() != artifactv1.Kind_KIND_SKILL {
		return nil, errors.New("artifact is not a skill")
	}
	var manifest toolv1.SkillManifest
	if err := protojson.Unmarshal(artifact.GetPayload(), &manifest); err != nil {
		return nil, err
	}
	if artifact.GetSignature() == nil {
		return nil, errors.New("skill publisher key does not match artifact signature")
	}
	if err := ValidateManifest(&manifest, artifact.GetSignature().GetKeyId()); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// ValidateManifest 在签名前校验技能数据；publisherKeyID 必须来自当前签名器。
func ValidateManifest(manifest *toolv1.SkillManifest, publisherKeyID string) error {
	if manifest == nil || strings.TrimSpace(manifest.GetSkillId()) == "" || strings.TrimSpace(manifest.GetVersion()) == "" || strings.TrimSpace(manifest.GetName()) == "" {
		return errors.New("skill id, version and name are required")
	}
	if publisherKeyID == "" || manifest.GetPublisherKeyId() != publisherKeyID {
		return errors.New("skill publisher key does not match artifact signature")
	}
	if ContentAddress(manifest.GetContent()) != manifest.GetContentDigest() || manifest.GetContentRef() != manifest.GetContentDigest() {
		return errors.New("skill content address does not match content")
	}
	total := len(manifest.GetContent())
	for _, resource := range manifest.GetResources() {
		if resource == nil || resource.GetName() == "" || resource.GetMediaType() == "" ||
			resource.GetSizeBytes() != int64(len(resource.GetContent())) || resource.GetContentRef() != resource.GetContentDigest() ||
			ContentAddress(resource.GetContent()) != resource.GetContentDigest() {
			return errors.New("skill resource metadata does not match content")
		}
		total += len(resource.GetContent())
	}
	if manifest.GetMaxContextBytes() <= 0 || manifest.GetMaxContextBytes() > contextLimit || int64(total) > manifest.GetMaxContextBytes() {
		return errors.New("skill context size exceeds declared limit")
	}
	if strings.TrimSpace(manifest.GetMinRuntimeVersion()) == "" {
		return errors.New("skill minimum runtime version is required")
	}
	return nil
}

// EffectiveTools 返回 Manifest 工具集合与当前能力令牌的交集。
func EffectiveTools(manifest *toolv1.SkillManifest, allowed func(string) bool) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(manifest.GetRequiredTools())+len(manifest.GetSuggestedTools()))
	for _, name := range append(append([]string(nil), manifest.GetRequiredTools()...), manifest.GetSuggestedTools()...) {
		if !seen[name] && allowed(name) {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// CompatibleRole 报告 Manifest 是否允许当前岗位；空集合表示不限制岗位。
func CompatibleRole(role string, compatible []string) bool {
	if len(compatible) == 0 {
		return true
	}
	for _, allowed := range compatible {
		if allowed == role {
			return true
		}
	}
	return false
}

// ContentAddress 返回正文或资源的安全哈希算法 256 位内容地址。
func ContentAddress(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Reference 返回钉死到当前认知回合的稳定技能引用。
func Reference(skillID, version, digest string) string {
	return skillID + "@" + version + "#" + digest
}
