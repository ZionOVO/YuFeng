package brain

import (
	"testing"
	"time"

	"yufeng/lib/kernel"
)

func TestDurationThresholdMet(t *testing.T) {
	tests := []struct {
		name     string
		elapsed  time.Duration
		required time.Duration
		want     bool
	}{
		{name: "zero threshold tolerates database clock lead", elapsed: -time.Millisecond, want: true},
		{name: "positive threshold not reached", elapsed: time.Second, required: 2 * time.Second, want: false},
		{name: "positive threshold reached", elapsed: 2 * time.Second, required: 2 * time.Second, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := durationThresholdMet(test.elapsed, test.required); got != test.want {
				t.Fatalf("durationThresholdMet(%s, %s)=%t want %t", test.elapsed, test.required, got, test.want)
			}
		})
	}
}

func TestAutoPromoteBlocked(t *testing.T) {
	if !autoPromoteBlocked("", "") {
		t.Fatal("empty scope and evidence must stay in shadow")
	}
	if autoPromoteBlocked("exact", "crs_mapped") {
		t.Fatal("qualified policy should promote")
	}
	if !autoPromoteBlocked("prefix", "crs_mapped") {
		t.Fatal("wide scope stays in shadow")
	}
	if !autoPromoteBlocked("exact", "model") {
		t.Fatal("model evidence stays in shadow")
	}
	if !autoPromoteBlocked("exact", "crs_unmapped") {
		t.Fatal("unmapped stays in shadow")
	}
}

func TestProductionSchedulerUsesR6(t *testing.T) {
	cfg := ProductionScheduler(0)
	if cfg.ShadowMinDuration != kernel.ShadowMinDuration || cfg.ShadowMinRequests != kernel.ShadowMinRequests {
		t.Fatalf("shadow gates %+v", cfg)
	}
	if cfg.CanaryPercent != kernel.CanaryPercentDefault {
		t.Fatalf("canary percent %d", cfg.CanaryPercent)
	}
	if cfg.Interval != kernel.GuardWindow {
		t.Fatalf("interval %s", cfg.Interval)
	}
}

func TestProductionSchedulerExplicitInterval(t *testing.T) {
	cfg := ProductionScheduler(time.Minute)
	if cfg.Interval != time.Minute {
		t.Fatalf("interval %s", cfg.Interval)
	}
}
