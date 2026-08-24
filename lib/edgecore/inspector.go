package edgecore

import (
	"context"
	"net/netip"

	commonv1 "yufeng/proto/gen/commonv1"
)

// InspectionInput 把可回放的规范视图与不参与检测键的连接元数据分开。
type InspectionInput struct {
	View          CanonicalView
	ClientAddress netip.Addr
}

// Inspector 是同步检测器：只出发现与每面覆盖度，不返回拦截动作。
// 接口刻意不进 proto；新注册实现不能单靠返回值 403。
//
// [检测器]: ../../docs/glossary.md#inspector
type Inspector interface {
	// ID 返回编译期注册标识，须与世代清单 detector_id 一致。
	ID() string
	// Inspect 对规范请求视图和连接元数据给出发现。实现必须无输入输出、无时钟、无日志。
	Inspect(ctx context.Context, input InspectionInput) (Inspection, error)
}

// RequestFromView 把规范视图收成检测器输入（查询键排序，回放稳定）。
func RequestFromView(view CanonicalView) Request {
	q := ""
	if view.Query != nil {
		q = view.Query.Encode()
	}
	headers := make(map[string]string, len(view.Headers))
	for k, vs := range view.Headers {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	return Request{Method: view.Method, Path: view.Path, Query: q, Headers: headers, Body: append([]byte(nil), view.Body...)}
}

// MergeInspections 合并多只眼睛的发现；覆盖度以规范视图为准，ERROR 叠加。
func MergeInspections(view CanonicalView, parts []Inspection) Inspection {
	out := Inspection{Coverage: append([]Coverage(nil), view.Coverage...), Rejected: view.Rejected}
	for _, p := range parts {
		out.Detections = append(out.Detections, p.Detections...)
		if p.Rejected {
			out.Rejected = true
		}
		for _, c := range p.Coverage {
			if c.Status == commonv1.CoverageStatus_COVERAGE_STATUS_ERROR {
				out.Coverage = append(out.Coverage, c)
			}
		}
	}
	return out
}
