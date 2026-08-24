package edgecore

import (
	"net/http"

	commonv1 "yufeng/proto/gen/commonv1"
)

// Intercepts 报告该姿态是否允许对本次请求写 403。
func Intercepts(p commonv1.IngressPosture) bool {
	switch p {
	case commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ:
		return true
	default:
		return false
	}
}

// Observes 报告该姿态是否为观察壳（永远不得 403）。
func Observes(p commonv1.IngressPosture) bool {
	switch p {
	case commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT,
		commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE:
		return true
	default:
		return false
	}
}

// ResolvePosture 把未指定收成反代拦截（今日缺省）。
func ResolvePosture(p commonv1.IngressPosture) commonv1.IngressPosture {
	if p == commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED {
		return commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY
	}
	return p
}

// StatusInput 是检查覆盖度与入口姿态映射到超文本传输协议状态码的输入。
type StatusInput struct {
	Posture          commonv1.IngressPosture
	GateAction       Action
	View             CanonicalView
	Oversize         bool
	BodyPresent      bool
	EngineCrash      bool
	MissingRequestID bool
	InFlightExceeded bool
	ExtAuthzTimeout  bool
}

// HTTPStatus 按 api 第 21.2 节把覆盖情况收成状态码。
// 观察壳永远 200；拦截姿态下超体 413、畸形 400，不当无发现放行。
func HTTPStatus(in StatusInput) (code int, wouldHaveBlocked bool) {
	posture := ResolvePosture(in.Posture)
	would := in.GateAction == ActionBlock
	if Observes(posture) {
		return http.StatusOK, would
	}
	if in.InFlightExceeded {
		return http.StatusServiceUnavailable, false
	}
	if in.MissingRequestID || in.EngineCrash {
		if posture == commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ {
			return http.StatusOK, false
		}
		return http.StatusServiceUnavailable, false
	}
	if posture == commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ && in.ExtAuthzTimeout {
		return http.StatusOK, false
	}
	if in.Oversize {
		switch posture {
		case commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY:
			return http.StatusRequestEntityTooLarge, false
		case commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ:
			if in.BodyPresent {
				return http.StatusForbidden, false
			}
			return http.StatusOK, false
		}
	}
	if in.View.Rejected || coverageHasError(in.View.Coverage) {
		switch posture {
		case commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY:
			return http.StatusBadRequest, false
		case commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ:
			return http.StatusForbidden, false
		}
	}
	if in.GateAction == ActionBlock {
		return http.StatusForbidden, true
	}
	return http.StatusOK, would
}

func coverageHasError(cov []Coverage) bool {
	for _, c := range cov {
		if c.Status == commonv1.CoverageStatus_COVERAGE_STATUS_ERROR {
			return true
		}
	}
	return false
}

// MarkBodyPartial 在超体时把 body 面标成 PARTIAL。
func MarkBodyPartial(view *CanonicalView, inspected, total int64) {
	if view == nil {
		return
	}
	for i := range view.Coverage {
		if view.Coverage[i].Target == commonv1.InspectionSurface_INSPECTION_SURFACE_BODY {
			view.Coverage[i].Status = commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL
			view.Coverage[i].Inspected = inspected
			view.Coverage[i].Total = total
			return
		}
	}
	view.Coverage = append(view.Coverage, Coverage{
		Target:    commonv1.InspectionSurface_INSPECTION_SURFACE_BODY,
		Status:    commonv1.CoverageStatus_COVERAGE_STATUS_PARTIAL,
		Inspected: inspected, Total: total,
	})
}
