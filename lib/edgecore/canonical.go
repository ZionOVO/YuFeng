package edgecore

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/url"
	"strings"

	"google.golang.org/protobuf/proto"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/kernel"
)

// DefaultInspectionProfile 是架构冻结的默认超文本传输协议检查配置档。
func DefaultInspectionProfile() *artifactv1.HttpInspectionProfile {
	return &artifactv1.HttpInspectionProfile{
		NormalizePath:         true,
		PercentDecodeRounds:   2,
		EncodedSlash:          "reject",
		DuplicateQuery:        "first",
		DuplicateHeader:       "first",
		ClTeConflict:          "reject",
		JsonDuplicateKey:      "reject",
		MultipartMaxParts:     16,
		MultipartMaxPartBytes: kernel.EngineBodyLimitBytes,
		DecompressMaxBytes:    kernel.EngineBodyLimitBytes,
		MaxHeaders:            64,
		MaxParams:             128,
		JsonMaxDepth:          8,
		EngineBodyLimitBytes:  kernel.EngineBodyLimitBytes,
	}
}

// CanonicalView 是反代与外部授权共用的规范请求视图。
type CanonicalView struct {
	Method   string
	Path     string
	Query    url.Values
	Headers  map[string][]string
	Body     []byte
	Host     string
	Rejected bool
	Coverage []Coverage
	// ProfileDigest 钉死产生本视图的规范化配置档。
	ProfileDigest string
}

// Coverage 是单个检查面的覆盖度。
type Coverage struct {
	Target    commonv1.InspectionSurface
	Status    commonv1.CoverageStatus
	Inspected int64
	Total     int64
}

// Canonicalize 按配置档把原始请求变成规范视图。纯函数。
func Canonicalize(method, rawPath, rawQuery string, headers map[string]string, body []byte, profile *artifactv1.HttpInspectionProfile) CanonicalView {
	multi := make(map[string][]string, len(headers))
	for k, v := range headers {
		multi[k] = []string{v}
	}
	return canonicalize(method, rawPath, rawQuery, multi, body, profile)
}

// CanonicalizeHTTP 接受多值头，按配置档折叠重复键。
func CanonicalizeHTTP(method, rawPath, rawQuery string, headers map[string][]string, body []byte, profile *artifactv1.HttpInspectionProfile) CanonicalView {
	return canonicalize(method, rawPath, rawQuery, headers, body, profile)
}

func canonicalize(method, rawPath, rawQuery string, headers map[string][]string, body []byte, profile *artifactv1.HttpInspectionProfile) CanonicalView {
	if profile == nil {
		profile = DefaultInspectionProfile()
	}
	view := CanonicalView{Method: strings.ToUpper(method), Headers: map[string][]string{}, ProfileDigest: inspectionProfileDigest(profile)}
	path := rawPath
	if path == "" {
		path = "/"
	}
	for i := 0; i < int(profile.PercentDecodeRounds); i++ {
		if hasEncodedSlash(path) && profile.EncodedSlash == "reject" {
			view.Rejected = true
		}
		decoded, err := url.PathUnescape(path)
		if err != nil {
			view.Rejected = true
			break
		}
		if decoded == path {
			break
		}
		path = decoded
	}
	if profile.NormalizePath {
		path = normalizePath(path)
	}
	view.Path = path

	if malformedQuery(rawQuery) {
		view.Rejected = true
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		view.Rejected = true
		q = url.Values{}
	}
	switch profile.DuplicateQuery {
	case "first":
		first := url.Values{}
		for k, vs := range q {
			if len(vs) > 0 {
				first[k] = []string{vs[0]}
			}
		}
		q = first
	case "last":
		last := url.Values{}
		for k, vs := range q {
			if len(vs) > 0 {
				last[k] = []string{vs[len(vs)-1]}
			}
		}
		q = last
	}
	if profile.PercentDecodeRounds > 1 {
		for k, vs := range q {
			for i, v := range vs {
				cur := v
				// ParseQuery 已解一轮，再补 rounds-1 轮。
				for n := 1; n < int(profile.PercentDecodeRounds); n++ {
					dec, err := url.QueryUnescape(cur)
					if err != nil || dec == cur {
						break
					}
					cur = dec
				}
				vs[i] = cur
			}
			q[k] = vs
		}
	}
	view.Query = q
	paramCount := 0
	for _, vs := range q {
		paramCount += len(vs)
	}
	if profile.MaxParams > 0 && paramCount > int(profile.MaxParams) {
		view.Rejected = true
	}

	if headerVal(headers, "Content-Length") != "" && headerVal(headers, "Transfer-Encoding") != "" && profile.ClTeConflict == "reject" {
		view.Rejected = true
	}
	if headerVal(headers, "X-Duplicate-Content-Length") != "" || len(headerVals(headers, "Content-Length")) > 1 {
		view.Rejected = true
	}
	if profile.MaxHeaders > 0 && len(headers) > int(profile.MaxHeaders) {
		view.Rejected = true
	}
	for k, vs := range headers {
		picked := pickDuplicate(vs, profile.DuplicateHeader)
		view.Headers[httpCanonical(k)] = picked
	}
	if host := headerVal(headers, "Host"); host != "" {
		view.Host = host
	}

	// Go 的真实主机在 r.Host，通常不在 Header map；调用方应写入 Host 键。

	limit := int(profile.EngineBodyLimitBytes)
	if limit <= 0 {
		limit = kernel.EngineBodyLimitBytes
	}
	maxInflate := int(profile.DecompressMaxBytes)
	if maxInflate <= 0 {
		maxInflate = kernel.EngineBodyLimitBytes
	}
	enc := strings.ToLower(headerVal(headers, "Content-Encoding"))
	bodyOpaque := false
	if enc != "" && enc != "identity" && len(body) > 0 {
		algorithm := strings.TrimSpace(strings.Split(enc, ",")[0])
		if !allowsDecompress(profile, algorithm) {
			// 空 decompress_algorithms 不解压：压缩体不算已检查。
			bodyOpaque = true
			body = nil
		} else {
			r, closeFn, err := decompressionReader(algorithm, body)
			if err != nil {
				view.Rejected = true
				body = nil
			} else {
				raw, err := io.ReadAll(io.LimitReader(r, int64(maxInflate)+1))
				closeErr := closeFn()
				if err != nil && !strings.Contains(err.Error(), "EOF") {
					view.Rejected = true
				}
				if closeErr != nil {
					view.Rejected = true
				}
				if len(raw) > maxInflate {
					view.Rejected = true
				}
				body = raw
			}
		}
	}
	ct := strings.ToLower(headerVal(headers, "Content-Type"))
	if strings.Contains(ct, "application/json") {
		if profile.JsonDuplicateKey == "reject" && hasDuplicateJSONKey(body) {
			view.Rejected = true
		}
		if profile.JsonMaxDepth > 0 && jsonExceedsDepth(body, int(profile.JsonMaxDepth)) {
			view.Rejected = true
		}
	}
	if strings.Contains(ct, "multipart/") {
		if !bytes.Contains(body, []byte("Content-Disposition")) {
			view.Rejected = true
			body = nil
		} else if rejectMultipartLimits(body, ct, profile) {
			view.Rejected = true
		}
	}

	var bodyStatus commonv1.CoverageStatus
	inspected := int64(0)
	total := int64(len(body))
	if bodyOpaque {
		bodyStatus = commonv1.CoverageStatus_COVERAGE_STATUS_ERROR
	} else if len(body) == 0 {
		bodyStatus = commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT
	} else if len(body) > limit {
		view.Body = append([]byte(nil), body[:limit]...)
		bodyStatus = commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL
		inspected = int64(limit)
	} else {
		view.Body = append([]byte(nil), body...)
		bodyStatus = commonv1.CoverageStatus_COVERAGE_STATUS_FULL
		inspected = total
	}

	view.Coverage = []Coverage{
		{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_PATH, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL, Inspected: int64(len(view.Path)), Total: int64(len(view.Path))},
		{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_QUERY, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL, Inspected: int64(len(rawQuery)), Total: int64(len(rawQuery))},
		{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_HEADER, Status: commonv1.CoverageStatus_COVERAGE_STATUS_FULL},
		{Target: commonv1.InspectionSurface_INSPECTION_SURFACE_BODY, Status: bodyStatus, Inspected: inspected, Total: total},
	}
	return view
}

func inspectionProfileDigest(profile *artifactv1.HttpInspectionProfile) string {
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(profile)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizePath(p string) string {
	if p == "" {
		return "/"
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, part)
		}
	}
	return "/" + strings.Join(out, "/")
}

func httpCanonical(k string) string { return strings.ToLower(k) }

func allowsDecompress(profile *artifactv1.HttpInspectionProfile, alg string) bool {
	if profile == nil || len(profile.DecompressAlgorithms) == 0 {
		return false
	}
	for _, a := range profile.DecompressAlgorithms {
		if strings.EqualFold(strings.TrimSpace(a), alg) {
			return true
		}
	}
	return false
}

func decompressionReader(algorithm string, body []byte) (io.Reader, func() error, error) {
	switch strings.ToLower(algorithm) {
	case "gzip":
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		return r, r.Close, nil
	case "deflate":
		r, err := zlib.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		return r, r.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported content encoding %s", algorithm)
	}
}

func rejectMultipartLimits(body []byte, contentType string, profile *artifactv1.HttpInspectionProfile) bool {
	if profile == nil {
		return false
	}
	maxParts := int(profile.MultipartMaxParts)
	maxPart := int(profile.MultipartMaxPartBytes)
	if maxParts <= 0 && maxPart <= 0 {
		return false
	}
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		return true
	}
	reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	n := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return true
		}
		n++
		limit := int64(maxPart)
		if limit <= 0 {
			limit = int64(len(body))
		}
		raw, err := io.ReadAll(io.LimitReader(part, limit+1))
		closeErr := part.Close()
		if err != nil || closeErr != nil || (maxPart > 0 && len(raw) > maxPart) {
			return true
		}
		if maxParts > 0 && n > maxParts {
			return true
		}
	}
	return false
}

func hasEncodedSlash(path string) bool {
	return strings.Contains(strings.ToLower(path), "%2f")
}

func malformedQuery(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '%' {
			continue
		}
		if i+2 >= len(raw) || !isHex(raw[i+1]) || !isHex(raw[i+2]) {
			return true
		}
	}
	return false
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func headerVals(h map[string][]string, name string) []string {
	if h == nil {
		return nil
	}
	if vs, ok := h[name]; ok {
		return vs
	}
	want := httpCanonical(name)
	for k, vs := range h {
		if httpCanonical(k) == want {
			return vs
		}
	}
	return nil
}

func headerVal(h map[string][]string, name string) string {
	vs := headerVals(h, name)
	if len(vs) == 0 {
		return ""
	}
	return vs[0]
}

func pickDuplicate(vs []string, mode string) []string {
	if len(vs) == 0 {
		return nil
	}
	switch mode {
	case "last":
		return []string{vs[len(vs)-1]}
	case "combine":
		return append([]string(nil), vs...)
	default:
		return []string{vs[0]}
	}
}

func jsonExceedsDepth(b []byte, max int) bool {
	if len(b) == 0 || max <= 0 {
		return false
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return false
	}
	return jsonDepth(v) > max
}

func jsonDepth(v any) int {
	switch t := v.(type) {
	case map[string]any:
		max := 1
		for _, child := range t {
			if d := 1 + jsonDepth(child); d > max {
				max = d
			}
		}
		return max
	case []any:
		max := 1
		for _, child := range t {
			if d := 1 + jsonDepth(child); d > max {
				max = d
			}
		}
		return max
	default:
		return 0
	}
}

func hasDuplicateJSONKey(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := tok.(json.Delim)
	if !ok || d != '{' {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return false
		}
		k, _ := kt.(string)
		if seen[k] {
			return true
		}
		seen[k] = true
		var skip any
		if err := dec.Decode(&skip); err != nil {
			return false
		}
	}
	return false
}

// CoverageOf 返回指定面的状态。
func CoverageOf(cov []Coverage, surface commonv1.InspectionSurface) commonv1.CoverageStatus {
	for _, c := range cov {
		if c.Target == surface {
			return c.Status
		}
	}
	return commonv1.CoverageStatus_COVERAGE_STATUS_ABSENT
}
