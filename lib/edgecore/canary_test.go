package edgecore

import "testing"

func TestCanarySelectedDeterministic(t *testing.T) {
	const id = "550e8400-e29b-41d4-a716-446655440000"
	first := CanarySelected(id, 5)
	second := CanarySelected(id, 5)
	if first != second {
		t.Fatal("同一请求标识分桶结果不应变化")
	}
	if !CanarySelected(id, 100) {
		t.Fatal("100% 必须命中")
	}
	if CanarySelected(id, 0) {
		t.Fatal("0% 不得命中")
	}
}

func TestCanarySelectedRoughDistribution(t *testing.T) {
	const samples = 20000
	selected := 0
	for i := 0; i < samples; i++ {
		if CanarySelected(string(rune(i)), 5) {
			selected++
		}
	}
	rate := float64(selected) / samples
	if rate < 0.03 || rate > 0.07 {
		t.Fatalf("5%% 分桶比例 = %.4f, want 接近 0.05", rate)
	}
}

func TestCanarySelectedUnitStableAcrossRequestIDs(t *testing.T) {
	first := CanarySelectedUnit("unit-1", "rel-1", 5)
	for i := 0; i < 20; i++ {
		id, err := NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		_ = id
		if CanarySelectedUnit("unit-1", "rel-1", 5) != first {
			t.Fatal("same unit+release must stay in the same bucket")
		}
	}
}

func TestNewRequestIDUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id, err := NewRequestID()
		if err != nil {
			t.Fatal(err)
		}
		if len(id) != 32 {
			t.Fatalf("request id 长度 = %d, want 32", len(id))
		}
		if seen[id] {
			t.Fatal("request id 碰撞")
		}
		seen[id] = true
	}
}
