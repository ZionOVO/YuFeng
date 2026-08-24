package edgecore

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
)

const (
	shapeMaxSourceBytes = 2048
	shapeMaxSelectors   = 16
	shapeMinPrefixSegs  = 2
)

var uuidCharset = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type compiledShape struct {
	methods     map[string]bool
	route       []string
	pathPrefix  string
	constraints []compiledShapeConstraint
}

type compiledShapeConstraint struct {
	selector string
	charset  string
	minLen   int32
	maxLen   int32
}

func compileShape(src *artifactv1.ShapeSource) (*compiledShape, error) {
	if err := ValidateShapeSource(src); err != nil {
		return nil, err
	}
	compiled := &compiledShape{
		methods: map[string]bool{}, pathPrefix: src.GetPathPrefix(),
		constraints: make([]compiledShapeConstraint, 0, len(src.GetConstraints())),
	}
	for _, method := range src.GetMethods() {
		compiled.methods[strings.ToUpper(method)] = true
	}
	if src.GetRouteTemplate() != "" {
		compiled.route = splitPath(src.GetRouteTemplate())
	}
	for _, constraint := range src.GetConstraints() {
		compiled.constraints = append(compiled.constraints, compiledShapeConstraint{
			selector: constraint.GetSelector(),
			charset:  constraint.GetCharset(),
			minLen:   constraint.GetMinLen(),
			maxLen:   constraint.GetMaxLen(),
		})
	}
	return compiled, nil
}

// Violates 判断规范请求是否违反当前已编译的正向请求形状。
func (s *compiledShape) Violates(req Request, view CanonicalView) bool {
	if s == nil {
		return false
	}
	path := view.Path
	if path == "" {
		path = req.Path
	}
	if len(s.route) > 0 {
		if !compiledRouteMatch(s.route, path) {
			return false
		}
	} else if !strings.HasPrefix(path, s.pathPrefix) {
		return false
	}
	method := view.Method
	if method == "" {
		method = strings.ToUpper(req.Method)
	}
	if !s.methods[strings.ToUpper(method)] {
		return true
	}
	for _, constraint := range s.constraints {
		value, ok := selectorValue(constraint.selector, req, view)
		if !ok || int32(len(value)) < constraint.minLen || int32(len(value)) > constraint.maxLen || !charsetOK(constraint.charset, value) {
			return true
		}
	}
	return false
}

// EvaluateShapeViolation 使用生产规范化档判断单个请求是否违反受限形状草案。
// 调查协调器只把它用于内存中的代表样本回放，不执行输入输出或发布动作。
func EvaluateShapeViolation(source *artifactv1.ShapeSource, req Request) (bool, error) {
	compiled, err := compileShape(source)
	if err != nil {
		return false, err
	}
	view := Canonicalize(req.Method, req.Path, req.Query, req.Headers, req.Body, DefaultInspectionProfile())
	return compiled.Violates(req, view), nil
}

func compiledRouteMatch(template []string, path string) bool {
	parts := splitPath(path)
	if len(template) != len(parts) {
		return false
	}
	for i := range template {
		if strings.HasPrefix(template[i], "{") && strings.HasSuffix(template[i], "}") {
			if parts[i] == "" {
				return false
			}
			continue
		}
		if template[i] != parts[i] {
			return false
		}
	}
	return true
}

// parseShapeSource 解析形状制品载荷：直接 ShapeSource，或提案意图里的 shape_source。
func parseShapeSource(payload []byte) (*artifactv1.ShapeSource, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty shape payload")
	}
	if len(payload) > shapeMaxSourceBytes {
		return nil, fmt.Errorf("shape source exceeds %d bytes", shapeMaxSourceBytes)
	}
	var src artifactv1.ShapeSource
	if err := protojson.Unmarshal(payload, &src); err == nil && shapeSourcePresent(&src) {
		return &src, nil
	}
	var wrap struct {
		ShapeSource json.RawMessage `json:"shapeSource"`
	}
	if err := json.Unmarshal(payload, &wrap); err != nil {
		return nil, fmt.Errorf("parse shape: %w", err)
	}
	if len(wrap.ShapeSource) == 0 {
		var snake struct {
			ShapeSource json.RawMessage `json:"shape_source"`
		}
		if err := json.Unmarshal(payload, &snake); err != nil {
			return nil, fmt.Errorf("parse shape: %w", err)
		}
		wrap.ShapeSource = snake.ShapeSource
	}
	if len(wrap.ShapeSource) == 0 {
		return nil, fmt.Errorf("shape payload missing shape_source")
	}
	if err := protojson.Unmarshal(wrap.ShapeSource, &src); err != nil {
		return nil, fmt.Errorf("parse shape_source: %w", err)
	}
	return &src, nil
}

func shapeSourcePresent(src *artifactv1.ShapeSource) bool {
	if src == nil {
		return false
	}
	return len(src.Methods) > 0 || src.RouteTemplate != "" || src.PathPrefix != "" || len(src.Constraints) > 0
}

// ValidateShapeSource 拒绝过宽或非法的正向形状。
func ValidateShapeSource(src *artifactv1.ShapeSource) error {
	if src == nil {
		return fmt.Errorf("shape_source is required")
	}
	if len(src.Methods) == 0 {
		return fmt.Errorf("shape methods required")
	}
	if src.RouteTemplate == "" && src.PathPrefix == "" {
		return fmt.Errorf("shape requires route_template or path_prefix")
	}
	if src.PathPrefix != "" {
		if src.PathPrefix == "/" || pathSegmentCount(src.PathPrefix) < shapeMinPrefixSegs {
			return fmt.Errorf("shape path_prefix too wide")
		}
	}
	if len(src.Constraints) > shapeMaxSelectors {
		return fmt.Errorf("shape selectors exceed %d", shapeMaxSelectors)
	}
	for _, c := range src.Constraints {
		if c == nil || c.Selector == "" {
			return fmt.Errorf("shape constraint selector required")
		}
		if c.MaxLen <= 0 {
			return fmt.Errorf("shape constraint %s missing max_len", c.Selector)
		}
		if c.MinLen < 0 || c.MinLen > c.MaxLen {
			return fmt.Errorf("shape constraint %s invalid length bounds", c.Selector)
		}
		switch c.Charset {
		case "", "ascii_print", "digit", "alpha", "hex", "uuid":
		case "any":
			return fmt.Errorf("shape charset any forbidden")
		default:
			return fmt.Errorf("shape charset %s unknown", c.Charset)
		}
	}
	return nil
}

// ShapeViolates 在范围内且不满足正向约束时为真（闸应拦截）。
func ShapeViolates(src *artifactv1.ShapeSource, req Request, view CanonicalView) bool {
	compiled, err := compileShape(src)
	if err != nil {
		return false
	}
	return compiled.Violates(req, view)
}

func pathSegmentCount(p string) int {
	n := 0
	for _, part := range strings.Split(p, "/") {
		if part != "" {
			n++
		}
	}
	return n
}

func routeTemplateMatch(tmpl, path string) bool {
	ts := splitPath(tmpl)
	ps := splitPath(path)
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if ts[i] != ps[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, "/") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func selectorValue(sel string, req Request, view CanonicalView) (string, bool) {
	kind, name, ok := strings.Cut(sel, ".")
	if !ok || name == "" {
		return "", false
	}
	switch strings.ToLower(kind) {
	case "query":
		if view.Query != nil {
			if !view.Query.Has(name) {
				return "", false
			}
			return view.Query.Get(name), true
		}
		q, err := url.ParseQuery(req.Query)
		if err != nil || !q.Has(name) {
			return "", false
		}
		return q.Get(name), true
	case "header":
		if vs := view.Headers[httpCanonical(name)]; len(vs) > 0 {
			return vs[0], true
		}
		if req.Headers != nil {
			if v, ok := req.Headers[name]; ok {
				return v, true
			}
			if v, ok := req.Headers[httpCanonical(name)]; ok {
				return v, true
			}
		}
		return "", false
	case "json":
		return jsonField(view.Body, name)
	default:
		return "", false
	}
}

func jsonField(body []byte, path string) (string, bool) {
	if len(body) == 0 {
		return "", false
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", false
	}
	cur := root
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		next, ok := m[part]
		if !ok {
			return "", false
		}
		cur = next
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", v), "0"), "."), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	default:
		return "", false
	}
}

func charsetOK(kind, val string) bool {
	switch kind {
	case "", "ascii_print":
		for _, r := range val {
			if r < 32 || r > 126 {
				return false
			}
		}
		return true
	case "digit":
		for _, r := range val {
			if !unicode.IsDigit(r) {
				return false
			}
		}
		return true
	case "alpha":
		for _, r := range val {
			if !unicode.IsLetter(r) {
				return false
			}
		}
		return true
	case "hex":
		for _, r := range val {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
		return true
	case "uuid":
		return uuidCharset.MatchString(val)
	default:
		return false
	}
}

func stringInFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
