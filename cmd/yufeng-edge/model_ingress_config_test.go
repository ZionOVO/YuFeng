package main

import (
	"strings"
	"testing"
	"time"
)

func TestModelIngressDefaultsFromEnvironmentUsesEdgeWindowNames(t *testing.T) {
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_ITEMS", "8192")
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_BYTES", "67108864")
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE", "45s")

	defaults, err := modelIngressDefaultsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.maxItems != 8192 || defaults.maxBytes != 67108864 || defaults.maxAge != 45*time.Second {
		t.Fatalf("defaults=%+v", defaults)
	}
}

func TestModelIngressDefaultsFromEnvironmentRejectsInvalidAge(t *testing.T) {
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_ITEMS", "")
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_BYTES", "")
	t.Setenv("YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE", "tomorrow")
	if _, err := modelIngressDefaultsFromEnvironment(); err == nil || !strings.Contains(err.Error(), "YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE") {
		t.Fatalf("error=%v", err)
	}
}

func TestModelIngressHardLimitRejectsPlatformOverflow(t *testing.T) {
	if _, err := modelIngressHardLimit(65537, 1<<20, time.Second); err == nil {
		t.Fatal("item hard limit above the platform boundary must fail")
	}
}
