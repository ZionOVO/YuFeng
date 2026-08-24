package kernel

import (
	"errors"
	"testing"
)

func TestRejectOnboardingRuntimeConstraintsUnimplemented(t *testing.T) {
	cases := []string{"nftables", "iptables", "shell", "unknown", "sh -c id", "seccomp"}
	for _, v := range cases {
		err := RejectOnboardingRuntimeConstraints(v)
		if !errors.Is(err, ErrUnimplementedPrimitive) {
			t.Fatalf("%q want unimplemented, got %v", v, err)
		}
	}
	if err := RejectOnboardingRuntimeConstraints("", "  "); err != nil {
		t.Fatalf("empty values must pass: %v", err)
	}
}
