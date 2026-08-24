package brain

// 第一阶段指令种类与工具集，与 docs/api.md §18.1.1 表一致。
const (
	instructionSession    = "SESSION_MESSAGE"
	instructionTriage     = "EVENT_TRIAGE"
	instructionCaseReview = "CASE_REVIEW"

	bindingAssetPrefix     = "asset:"
	sessionContentMaxBytes = 8192
)

var sessionInstructionTools = []string{"model.generate", "session.reply"}

var triageInstructionTools = []string{
	"model.generate",
	"triage.get",
	"triage.complete",
}

var demoTriageInstructionTools = []string{
	"event.get",
	"event.list",
	"cluster.get",
	"release.list",
	"govern.propose",
	"govern.gate",
	"govern.start_shadow",
}

func assetBinding(assetID string) string {
	return bindingAssetPrefix + assetID
}

func assetIDFromBinding(b string) (string, bool) {
	if len(b) > len(bindingAssetPrefix) && b[:len(bindingAssetPrefix)] == bindingAssetPrefix {
		return b[len(bindingAssetPrefix):], true
	}
	return "", false
}
