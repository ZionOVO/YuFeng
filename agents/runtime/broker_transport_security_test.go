package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsPipeSecurityDescriptorAllowsOnlyCurrentUserAndLocalSystem(t *testing.T) {
	const currentUserSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"
	descriptor, err := resolveWindowsPipeSecurityDescriptor(func() (string, error) {
		return currentUserSID, nil
	})
	if err != nil {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor() error = %v", err)
	}
	const want = "D:P(A;;GA;;;SY)(A;;GA;;;S-1-5-21-1111111111-2222222222-3333333333-1001)"
	if descriptor != want {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor() = %q, want %q", descriptor, want)
	}
	for _, forbidden := range []string{"WD", "AU", "BU", "AN", "NU"} {
		if strings.Contains(descriptor, ";;;"+forbidden+")") {
			t.Fatalf("security descriptor unexpectedly grants %s: %q", forbidden, descriptor)
		}
	}
}

func TestWindowsPipeSecurityDescriptorRejectsMalformedOrInjectedSID(t *testing.T) {
	tests := []struct {
		name string
		sid  string
	}{
		{name: "empty", sid: ""},
		{name: "leading whitespace", sid: " S-1-5-21-1"},
		{name: "missing sub authority", sid: "S-1-5"},
		{name: "unsupported revision", sid: "S-2-5-21-1"},
		{name: "empty component", sid: "S-1-5--21"},
		{name: "alphabetic component", sid: "S-1-5-ADMIN"},
		{name: "ace injection", sid: "S-1-5-21-1)(A;;GA;;;WD"},
		{name: "authority overflow", sid: "S-1-281474976710656-1"},
		{name: "sub authority overflow", sid: "S-1-5-4294967296"},
		{name: "too many sub authorities", sid: "S-1-5-1-2-3-4-5-6-7-8-9-10-11-12-13-14-15-16"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if descriptor, err := windowsPipeSecurityDescriptor(tt.sid); err == nil {
				t.Fatalf("windowsPipeSecurityDescriptor(%q) = %q, want error", tt.sid, descriptor)
			}
		})
	}
}

func TestWindowsPipeSecurityDescriptorFailsClosedWhenSIDResolutionFails(t *testing.T) {
	wantErr := errors.New("token user unavailable")
	descriptor, err := resolveWindowsPipeSecurityDescriptor(func() (string, error) {
		return "", wantErr
	})
	if descriptor != "" {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor() = %q, want empty descriptor", descriptor)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor() error = %v, want %v", err, wantErr)
	}
	if descriptor, err := resolveWindowsPipeSecurityDescriptor(func() (string, error) {
		return "S-1-5-21-1)(A;;GA;;;WD", nil
	}); err == nil || descriptor != "" {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor() = (%q, %v), want empty descriptor and error", descriptor, err)
	}
	if descriptor, err := resolveWindowsPipeSecurityDescriptor(nil); err == nil || descriptor != "" {
		t.Fatalf("resolveWindowsPipeSecurityDescriptor(nil) = (%q, %v), want empty descriptor and error", descriptor, err)
	}
}

func TestDarwinBrokerSocketPathKeepsCompleteNonce(t *testing.T) {
	const nonce = "0123456789abcdef0123456789abcdef"
	path, err := darwinBrokerSocketPath(nonce)
	if err != nil {
		t.Fatalf("darwinBrokerSocketPath() error = %v", err)
	}
	const want = "/tmp/yfr-0123456789abcdef0123456789abcdef.sock"
	if path != want {
		t.Fatalf("darwinBrokerSocketPath() = %q, want %q", path, want)
	}
	if strings.Count(path, nonce) != 1 {
		t.Fatalf("darwin broker socket path does not preserve nonce exactly once: %q", path)
	}
}

func TestDarwinBrokerSocketPathRejectsNoncesOutsideGeneratedDomain(t *testing.T) {
	for _, nonce := range []string{
		"",
		"0123456789abcdef",
		"0123456789abcdef0123456789abcde",
		"0123456789abcdef0123456789abcdef0",
		"0123456789abcdef0123456789abcdeg",
		"0123456789ABCDEF0123456789ABCDEF",
	} {
		if path, err := darwinBrokerSocketPath(nonce); err == nil {
			t.Fatalf("darwinBrokerSocketPath(%q) = %q, want error", nonce, path)
		}
	}
}
