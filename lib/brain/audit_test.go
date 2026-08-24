package brain

import (
	"testing"
	"time"
)

func TestAuditEntryHashCanonicalizesDetails(t *testing.T) {
	at := time.Date(2026, 8, 15, 10, 0, 0, 123, time.UTC)
	a := auditEntryHash(at, "user", "u1", "act", "obj", "o1", `{"created_by":"admin"}`, "prev")
	b := auditEntryHash(at, "user", "u1", "act", "obj", "o1", `{"created_by": "admin"}`, "prev")
	if a != b {
		t.Fatalf("details JSON 空白差异不应影响审计哈希: %s != %s", a, b)
	}
	if a == "" {
		t.Fatal("审计哈希为空")
	}
}
