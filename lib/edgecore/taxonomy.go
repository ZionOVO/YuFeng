package edgecore

import (
	"strings"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// DefaultTaxonomyMapper 是已签名分类映射器的默认闭集（按规则号前缀，不认检测器标识）。
func DefaultTaxonomyMapper() *artifactv1.TaxonomyMapper {
	return &artifactv1.TaxonomyMapper{
		TaxonomyVersion: "tax/v1",
		Rules: []*artifactv1.TaxonomyRule{
			{RulePrefix: "942", AttackClass: commonv1.AttackClass_ATTACK_CLASS_SQLI},
			{RulePrefix: "941", AttackClass: commonv1.AttackClass_ATTACK_CLASS_XSS},
			{RulePrefix: "930", AttackClass: commonv1.AttackClass_ATTACK_CLASS_PATH_TRAVERSAL},
			{RulePrefix: "931", AttackClass: commonv1.AttackClass_ATTACK_CLASS_PATH_TRAVERSAL},
			{RulePrefix: "932", AttackClass: commonv1.AttackClass_ATTACK_CLASS_CMDI},
			{RulePrefix: "933", AttackClass: commonv1.AttackClass_ATTACK_CLASS_CMDI},
			{RulePrefix: "943", AttackClass: commonv1.AttackClass_ATTACK_CLASS_SSRF},
			{RulePrefix: "934", AttackClass: commonv1.AttackClass_ATTACK_CLASS_CMDI},
			{RulePrefix: "944", AttackClass: commonv1.AttackClass_ATTACK_CLASS_CMDI},
		},
	}
}

// MapAttackClass 只认已签名映射器。缺席或未映射返回 UNMAPPED。
func MapAttackClass(mapper *artifactv1.TaxonomyMapper, ruleID string) commonv1.AttackClass {
	if mapper == nil {
		return commonv1.AttackClass_ATTACK_CLASS_UNMAPPED
	}
	id := strings.TrimSpace(ruleID)
	if id == "" {
		return commonv1.AttackClass_ATTACK_CLASS_UNMAPPED
	}
	for _, r := range mapper.Rules {
		if r == nil {
			continue
		}
		if r.RuleId != "" && r.RuleId == id {
			return r.AttackClass
		}
	}
	var best *artifactv1.TaxonomyRule
	for _, r := range mapper.Rules {
		if r == nil || r.RulePrefix == "" {
			continue
		}
		if strings.HasPrefix(id, r.RulePrefix) {
			if best == nil || len(r.RulePrefix) > len(best.RulePrefix) {
				best = r
			}
		}
	}
	if best == nil {
		return commonv1.AttackClass_ATTACK_CLASS_UNMAPPED
	}
	return best.AttackClass
}

// AutoGovernable 报告该检测键是否可由已签名映射器进入自动治理。
// 不认检测器标识；未映射或映射器缺席为假。
func AutoGovernable(key *commonv1.DetectionKey, mapper *artifactv1.TaxonomyMapper) bool {
	if key == nil || mapper == nil {
		return false
	}
	class := MapAttackClass(mapper, key.RuleId)
	switch class {
	case commonv1.AttackClass_ATTACK_CLASS_SQLI,
		commonv1.AttackClass_ATTACK_CLASS_XSS,
		commonv1.AttackClass_ATTACK_CLASS_PATH_TRAVERSAL,
		commonv1.AttackClass_ATTACK_CLASS_SSRF,
		commonv1.AttackClass_ATTACK_CLASS_CMDI:
		return true
	default:
		return false
	}
}

// ApplyTaxonomy 用映射器覆盖发现类；映射器缺席则标未映射。
func ApplyTaxonomy(found []Detection, mapper *artifactv1.TaxonomyMapper) []Detection {
	if mapper == nil {
		return found
	}
	out := make([]Detection, len(found))
	copy(out, found)
	for i := range out {
		out[i].Class = MapAttackClass(mapper, out[i].RuleID)
	}
	return out
}
