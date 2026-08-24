package edgecore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	coreruleset "github.com/corazawaf/coraza-coreruleset/v4"
	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"

	"yufeng/lib/kernel"
	commonv1 "yufeng/proto/gen/commonv1"
)

var (
	sharedCRSOnce sync.Once
	sharedCRS     *CorazaDetector
	sharedCRSErr  error
)

// SharedCoraza 返回进程内共享的 DetectionOnly 核心规则集检测器。
func SharedCoraza() (*CorazaDetector, error) {
	sharedCRSOnce.Do(func() {
		sharedCRS, sharedCRSErr = NewCorazaDetector()
	})
	return sharedCRS, sharedCRSErr
}

const corazaDetectorID = "crs"

// CRSAutoGovernRule 判定规则标识是否允许进入自动治理通道。
// 91x / 920 / 913 等协议与扫描器类不得进自动治理。
func CRSAutoGovernRule(ruleID string) bool {
	id := strings.TrimSpace(ruleID)
	if len(id) < 3 {
		return false
	}
	switch {
	case strings.HasPrefix(id, "930"), strings.HasPrefix(id, "931"),
		strings.HasPrefix(id, "932"), strings.HasPrefix(id, "933"),
		strings.HasPrefix(id, "934"), strings.HasPrefix(id, "941"),
		strings.HasPrefix(id, "942"), strings.HasPrefix(id, "943"),
		strings.HasPrefix(id, "944"):
		return true
	default:
		return false
	}
}

// CorazaDetector 永远 DetectionOnly，只出发现。
type CorazaDetector struct {
	waf coraza.WAF
}

// NewCorazaDetector 按架构冻结清单装载核心规则集。
func NewCorazaDetector() (*CorazaDetector, error) {
	directives := `
Include @coraza.conf-recommended
SecRuleEngine DetectionOnly
SecRequestBodyAccess On
SecRequestBodyInMemoryLimit 65536
SecRequestBodyLimit 65536
SecRequestBodyNoFilesLimit 65536
SecRequestBodyLimitAction ProcessPartial
SecResponseBodyAccess Off
SecAuditEngine Off
Include @crs-setup.conf.example
Include @owasp_crs/REQUEST-901-INITIALIZATION.conf
Include @owasp_crs/REQUEST-930-APPLICATION-ATTACK-LFI.conf
Include @owasp_crs/REQUEST-931-APPLICATION-ATTACK-RFI.conf
Include @owasp_crs/REQUEST-932-APPLICATION-ATTACK-RCE.conf
Include @owasp_crs/REQUEST-934-APPLICATION-ATTACK-GENERIC.conf
Include @owasp_crs/REQUEST-941-APPLICATION-ATTACK-XSS.conf
Include @owasp_crs/REQUEST-942-APPLICATION-ATTACK-SQLI.conf
`
	waf, err := coraza.NewWAF(coraza.NewWAFConfig().WithRootFS(coreruleset.FS).WithDirectives(directives))
	if err != nil {
		return nil, fmt.Errorf("create coraza waf: %w", err)
	}
	return &CorazaDetector{waf: waf}, nil
}

// ID 返回 Coraza 检查器的稳定标识。
func (d *CorazaDetector) ID() string { return corazaDetectorID }

// Tier 返回 Coraza 检查器的同步微秒级成本层级。
func (d *CorazaDetector) Tier() CostTier { return CostSyncMicros }

// Inspect 只出发现与覆盖度，永不带拦截动作。
func (d *CorazaDetector) Inspect(ctx context.Context, input InspectionInput) (Inspection, error) {
	_ = ctx
	view := input.View
	req := RequestFromView(view)
	req.ClientAddress = input.ClientAddress
	dets, err := d.Detect(req)
	out := Inspection{Coverage: view.Coverage, Rejected: view.Rejected}
	if err != nil {
		out.Coverage = append(append([]Coverage(nil), view.Coverage...), CoverageError(commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY))
		return out, err
	}
	for i := range dets {
		dets[i].InspectorID = corazaDetectorID
		dets[i].ProfileDigest = view.ProfileDigest
	}
	out.Detections = dets
	return out, nil
}

// Evaluate 只产出观察发现，Action 永远不是 Block。
func (d *CorazaDetector) Evaluate(ctx context.Context, req Request) (Verdict, error) {
	_ = ctx
	dets, err := d.Detect(req)
	if err != nil {
		return Verdict{Action: ActionObserve, Message: err.Error()}, err
	}
	if len(dets) == 0 {
		return Verdict{Action: ActionAllow}, nil
	}
	return Verdict{Action: ActionObserve, RuleID: dets[0].RuleID, Confidence: 1, Message: "crs detection"}, nil
}

// Detect 返回检测键级发现。
func (d *CorazaDetector) Detect(req Request) ([]Detection, error) {
	tx := d.waf.NewTransaction()
	defer func() {
		tx.ProcessLogging()
		_ = tx.Close()
	}()
	src := req.ClientAddress.Unmap().String()
	if !req.ClientAddress.IsValid() {
		src = "0.0.0.0"
	}
	tx.ProcessConnection(src, 0, "0.0.0.0", 0)
	uri := req.Path
	if req.Query != "" {
		uri += "?" + req.Query
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	tx.ProcessURI(uri, method, "HTTP/1.1")
	host := "app.example"
	if req.Headers != nil {
		if h := req.Headers["host"]; h != "" {
			host = h
		}
	}
	tx.SetServerName(strings.Split(host, ":")[0])
	tx.AddRequestHeader("Host", host)
	tx.AddRequestHeader("User-Agent", "yufeng-edge")
	tx.AddRequestHeader("Accept", "*/*")
	if req.Headers != nil {
		for k, v := range req.Headers {
			if strings.EqualFold(k, "host") {
				continue
			}
			tx.AddRequestHeader(k, v)
		}
	}
	_ = tx.ProcessRequestHeaders()
	body := req.Body
	if len(body) > kernel.EngineBodyLimitBytes {
		body = body[:kernel.EngineBodyLimitBytes]
	}
	if len(body) > 0 {
		if _, _, err := tx.WriteRequestBody(body); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ProcessRequestBody(); err != nil {
		return nil, err
	}
	var out []Detection
	seen := map[string]bool{}
	for _, matched := range tx.MatchedRules() {
		rid := matched.Rule().ID()
		// 901 初始化与协议探测不是可治理检测键。
		if rid < 930000 || rid >= 950000 {
			continue
		}
		id := fmt.Sprintf("%d", rid)
		if seen[id] || rid == 0 {
			continue
		}
		// 核心规则集的 skipAfter 与 paranoia 控制规则也会出现在 MatchedRules，
		// 但没有攻击消息，不能当作业务检测上报。
		if strings.TrimSpace(matched.Message()) == "" {
			continue
		}
		class := mapCRSClass(matched.Rule().Tags())
		seen[id] = true
		loc, selector := matchedTarget(matched.MatchedDatas(), req)
		out = append(out, Detection{
			InspectorID:    corazaDetectorID,
			RuleID:         id,
			Class:          class,
			Score:          1,
			Location:       loc,
			Selector:       selector,
			Phase:          "request",
			Version:        kernel.CRSVersion,
			ManifestDigest: kernel.CRSTarballSHA256,
		})
	}
	return out, nil
}

func matchedTarget(data []types.MatchData, req Request) (commonv1.InspectionSurface, string) {
	if len(data) == 0 {
		return commonv1.InspectionSurface_INSPECTION_SURFACE_UNSPECIFIED, ""
	}
	name := strings.ToUpper(data[0].Variable().Name())
	key := strings.TrimSpace(data[0].Key())
	switch name {
	case "ARGS_GET", "QUERY_STRING":
		return commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, selectorName("query", key)
	case "REQUEST_HEADERS", "REQUEST_HEADERS_NAMES":
		return commonv1.InspectionSurface_INSPECTION_SURFACE_HEADER, selectorName("header", key)
	case "ARGS_POST", "REQUEST_BODY", "FILES", "FILES_NAMES":
		return commonv1.InspectionSurface_INSPECTION_SURFACE_BODY, selectorName("body", key)
	case "REQUEST_URI", "REQUEST_FILENAME", "REQUEST_BASENAME":
		return commonv1.InspectionSurface_INSPECTION_SURFACE_PATH, "path"
	case "ARGS":
		if query, err := url.ParseQuery(req.Query); err == nil && query.Has(key) {
			return commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, selectorName("query", key)
		}
		return commonv1.InspectionSurface_INSPECTION_SURFACE_BODY, selectorName("arg", key)
	default:
		return commonv1.InspectionSurface_INSPECTION_SURFACE_UNSPECIFIED, selectorName(strings.ToLower(name), key)
	}
}

func selectorName(prefix, key string) string {
	if key == "" {
		return prefix
	}
	return prefix + "." + strings.ToLower(key)
}

func mapCRSClass(tags []string) commonv1.AttackClass {
	for _, tag := range tags {
		normalized := strings.ToLower(tag)
		switch {
		case strings.HasSuffix(normalized, "attack-sqli"):
			return commonv1.AttackClass_ATTACK_CLASS_SQLI
		case strings.HasSuffix(normalized, "attack-xss"):
			return commonv1.AttackClass_ATTACK_CLASS_XSS
		case strings.HasSuffix(normalized, "attack-lfi"), strings.HasSuffix(normalized, "attack-rfi"):
			return commonv1.AttackClass_ATTACK_CLASS_PATH_TRAVERSAL
		case strings.HasSuffix(normalized, "attack-ssrf"):
			return commonv1.AttackClass_ATTACK_CLASS_SSRF
		case strings.HasSuffix(normalized, "attack-rce"):
			return commonv1.AttackClass_ATTACK_CLASS_CMDI
		}
	}
	return commonv1.AttackClass_ATTACK_CLASS_UNMAPPED
}
