package brain

import (
	"testing"

	"yufeng/lib/kernel"
)

func TestGuardWindowFirstDenyDoesNotTripWithoutBaselineJump(t *testing.T) {
	prev := GuardSnapshot{Requests: 100, Blocks: 1, Denies: 0}
	cur := GuardSnapshot{Requests: 100, Blocks: 1, Denies: 0}
	bad, _ := GuardWindowBad(prev, cur, kernel.DenyFeedbackBlockThreshold)
	if bad {
		t.Fatal("stable window is not bad")
	}
}

func TestGuardWindow5xxAndP99(t *testing.T) {
	prev := GuardSnapshot{Requests: 1000, Upstream5xx: 1, P99Micros: 1000}
	cur := GuardSnapshot{Requests: 2000, Upstream5xx: 80, P99Micros: 20000, Denies: 0}
	bad, reasons := GuardWindowBad(prev, cur, 3)
	if !bad {
		t.Fatal("5xx/p99 jump must be bad")
	}
	if reasons == "" {
		t.Fatal("need reason")
	}
}

func TestGuardWindowAverageIsNotP99(t *testing.T) {
	prev := GuardSnapshot{Requests: 1000, LatencyMicros: 1000 * 1000, LatencySamples: 1000, P99Micros: 2000}
	cur := GuardSnapshot{Requests: 2000, LatencyMicros: 1000*1000 + 50_000*1000, LatencySamples: 2000, P99Micros: 2000}
	bad, reasons := GuardWindowBad(prev, cur, 3)
	if bad && reasons == "p99_regression" {
		t.Fatal("mean latency jump must not count as p99 regression")
	}
}

func TestGuardWindowDenyThreshold(t *testing.T) {
	prev := GuardSnapshot{Denies: 0}
	cur := GuardSnapshot{Denies: int64(kernel.DenyFeedbackBlockThreshold)}
	bad, reasons := GuardWindowBad(prev, cur, kernel.DenyFeedbackBlockThreshold)
	if !bad || reasons != "unexpected_deny" {
		t.Fatalf("deny threshold: bad=%v reasons=%s", bad, reasons)
	}
}
