package brain

import (
	"strings"
	"testing"
)

// SESSION_MESSAGE 能力令牌不得带 govern.*（docs/api.md §18.5）。
func TestSessionInstructionToolsHaveNoGovern(t *testing.T) {
	if len(sessionInstructionTools) == 0 {
		t.Fatal("session instruction tools must not be empty")
	}
	for _, tool := range sessionInstructionTools {
		if strings.HasPrefix(tool, "govern.") {
			t.Fatalf("SESSION_MESSAGE capability must not include govern.*: %v", sessionInstructionTools)
		}
	}
}
