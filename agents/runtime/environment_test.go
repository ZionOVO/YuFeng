package runtime

import (
	"strings"
	"testing"
)

func TestChildEnvOmitsSecrets(t *testing.T) {
	t.Setenv("YUFENG_CAPABILITY", "secret-token")
	t.Setenv("YUFENG_ACCESS", "access-token")
	env := ChildEnv("work-1", "run-1", "nonce-1", 3, 4)
	if envHasSecret(env) {
		t.Fatalf("ChildEnv leaked secrets: %v", env)
	}
	keys := envKeys(env)
	want := map[string]bool{envWorkID: false, envRunID: false, envNonce: false, envBrokerFD: false}
	for _, k := range keys {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("missing %s", k)
		}
	}
}

func TestStripSecretsRemovesCapability(t *testing.T) {
	got := StripSecrets([]string{"PATH=/bin", "YUFENG_CAPABILITY=tok", "YUFENG_WORK_ID=w"})
	if envHasSecret(got) {
		t.Fatalf("%v", got)
	}
}

func TestLimitsFromEnvReadsHatchInjection(t *testing.T) {
	t.Setenv(envMemoryLimit, "1048576")
	t.Setenv(envRlimitCPU, "9")
	t.Setenv(envRlimitNOFILE, "64")
	got := LimitsFromEnv()
	if got.MemoryBytes != 1048576 || got.CPUSeconds != 9 || got.Files != 64 {
		t.Fatalf("%+v", got)
	}
	env := limitEnv(got)
	joined := strings.Join(env, ",")
	if !strings.Contains(joined, envRlimitNOFILE+"=64") || envHasSecret(env) {
		t.Fatalf("%v", env)
	}
}
