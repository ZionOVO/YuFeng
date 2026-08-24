package brain

import (
	"strings"

	commonv1 "yufeng/proto/gen/commonv1"
	eventv1 "yufeng/proto/gen/eventv1"
)

// triageFacts 是 EVENT_TRIAGE 入队谓词的已解析事实。
// UploadEvents 先查库填好本结构，再调用 shouldEnqueueTriage——表驱动测试即行为说明。
type triageFacts struct {
	Accepted           bool
	VerdictAllow       bool
	HasHTTP            bool
	EventAlreadyQueued bool
	OpenRuleOnPath     bool
	PendingSamePath    bool
	JarvisHasPubkey    bool
}

// shouldEnqueueTriage 判定是否为该已接受事件签发 EVENT_TRIAGE。
// 任一为假则静默跳过；不得把「未叫醒」表现为 UploadEvents 错误。
func shouldEnqueueTriage(f triageFacts) bool {
	return f.Accepted &&
		f.VerdictAllow &&
		f.HasHTTP &&
		f.JarvisHasPubkey &&
		!f.EventAlreadyQueued &&
		!f.OpenRuleOnPath &&
		!f.PendingSamePath
}

func shouldEnqueueProduction(accepted, jarvis bool, reason commonv1.TriageReason, pendingSameIdentity bool) bool {
	if !accepted || !jarvis || pendingSameIdentity {
		return false
	}
	switch reason {
	case commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMITIGATED,
		commonv1.TriageReason_TRIAGE_REASON_DETECTED_UNMAPPED,
		commonv1.TriageReason_TRIAGE_REASON_SUSPECTED_MISS:
		return true
	default:
		return false
	}
}

func eventHasHTTP(e *eventv1.Event) bool {
	if e == nil {
		return false
	}
	h := e.GetHttp()
	return h != nil && strings.TrimSpace(h.Method) != "" && strings.TrimSpace(h.Path) != ""
}

func eventVerdictAllow(e *eventv1.Event) bool {
	return e != nil && e.Verdict == eventv1.Verdict_VERDICT_ALLOW
}
