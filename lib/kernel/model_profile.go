package kernel

import (
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

// NormalizeModelProfile 校验并规范化签名模型档案，返回不共享可变字段的副本。
// 运行时阈值、窗口、上限和字段闭集只能来自该档案。
//
// [签名模型档案]: ../../docs/glossary.md#signed-model-profile
func NormalizeModelProfile(profile *artifactv1.ModelProfile) (*artifactv1.ModelProfile, error) {
	if profile == nil {
		return nil, errors.New("model profile is required")
	}
	out := &artifactv1.ModelProfile{
		ProfileId:                  strings.TrimSpace(profile.GetProfileId()),
		ModelGroup:                 strings.TrimSpace(profile.GetModelGroup()),
		ModelType:                  strings.TrimSpace(profile.GetModelType()),
		ModelVersion:               strings.TrimSpace(profile.GetModelVersion()),
		AlertThreshold:             profile.GetAlertThreshold(),
		ReviewFloor:                profile.GetReviewFloor(),
		ReviewWindowSeconds:        profile.GetReviewWindowSeconds(),
		MaxReviewPerUnit:           profile.GetMaxReviewPerUnit(),
		MaxReviewPerRoute:          profile.GetMaxReviewPerRoute(),
		DedupeRule:                 profile.GetDedupeRule(),
		MaxBodyBytes:               profile.GetMaxBodyBytes(),
		ReviewNewRoutes:            profile.GetReviewNewRoutes(),
		ReviewInsufficientCoverage: profile.GetReviewInsufficientCoverage(),
	}
	if out.ProfileId == "" || out.ModelGroup == "" || out.ModelType == "" || out.ModelVersion == "" {
		return nil, errors.New("model profile coordinates are required")
	}
	if !finiteUnitInterval(out.AlertThreshold) || !finiteUnitInterval(out.ReviewFloor) || out.AlertThreshold <= out.ReviewFloor {
		return nil, errors.New("model profile thresholds are invalid")
	}
	if out.ReviewWindowSeconds <= 0 || out.MaxReviewPerUnit <= 0 || out.MaxReviewPerRoute <= 0 {
		return nil, errors.New("model profile sampling limits must be positive")
	}
	if out.MaxReviewPerRoute > out.MaxReviewPerUnit {
		return nil, errors.New("model route review limit exceeds unit limit")
	}
	if out.DedupeRule != artifactv1.ModelDedupeRule_MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE {
		return nil, errors.New("model profile dedupe rule is unsupported")
	}
	if out.MaxReviewPerRoute != 1 {
		return nil, errors.New("highest-score dedupe requires one review representative per route")
	}
	if out.MaxBodyBytes <= 0 || out.MaxBodyBytes > EngineBodyLimitBytes {
		return nil, errors.New("model profile body limit is invalid")
	}
	headers := make(map[string]struct{}, len(profile.GetAllowedHeaders()))
	for _, raw := range profile.GetAllowedHeaders() {
		name := strings.ToLower(strings.TrimSpace(raw))
		if !validHeaderName(name) || sensitiveModelHeader(name) {
			return nil, errors.New("model profile allowed header is invalid")
		}
		headers[name] = struct{}{}
	}
	out.AllowedHeaders = make([]string, 0, len(headers))
	for name := range headers {
		out.AllowedHeaders = append(out.AllowedHeaders, name)
	}
	sort.Strings(out.AllowedHeaders)
	return out, nil
}

// DefaultModelProfile 返回由 Brain 签发的 0.2.0 基线模型档案。
func DefaultModelProfile() *artifactv1.ModelProfile {
	return &artifactv1.ModelProfile{
		ProfileId:                  "http-threat-model/default",
		ModelGroup:                 "http-threat",
		ModelType:                  "PVM",
		ModelVersion:               "v1",
		AlertThreshold:             ModelAlertThresholdDefault,
		ReviewFloor:                ModelReviewFloorDefault,
		ReviewWindowSeconds:        int32(ModelReviewWindow.Seconds()),
		MaxReviewPerUnit:           ModelReviewPerUnit,
		MaxReviewPerRoute:          ModelReviewPerRoute,
		DedupeRule:                 artifactv1.ModelDedupeRule_MODEL_DEDUPE_RULE_METHOD_ROUTE_HIGHEST_SCORE,
		AllowedHeaders:             []string{"accept", "content-type", "user-agent"},
		MaxBodyBytes:               EngineBodyLimitBytes,
		ReviewNewRoutes:            true,
		ReviewInsufficientCoverage: true,
	}
}

func finiteUnitInterval(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validHeaderName(name string) bool {
	if name == "" || http.CanonicalHeaderKey(name) == "" {
		return false
	}
	for _, r := range name {
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return true
}

func sensitiveModelHeader(name string) bool {
	switch name {
	case "authorization", "cookie", "proxy-authorization", "proxy-authenticate", "set-cookie", "x-api-key":
		return true
	default:
		return false
	}
}
