package kernel

import (
	"slices"
	"testing"
)

func TestNormalizeTrustedProxyCIDRsCanonicalizesAndRejectsInvalidInput(t *testing.T) {
	got, err := NormalizeTrustedProxyCIDRs([]string{" 10.1.2.3/8 ", "2001:db8:1::1/32", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "2001:db8::/32"}
	if !slices.Equal(got, want) {
		t.Fatalf("cidrs=%v want %v", got, want)
	}
	for _, invalid := range [][]string{{"10.0.0.0"}, {"999.0.0.0/8"}, {"2001:db8::/129"}} {
		if _, err := NormalizeTrustedProxyCIDRs(invalid); err == nil {
			t.Fatalf("invalid cidrs accepted: %v", invalid)
		}
	}
}

func TestDataplaneDesiredSpecDigestIncludesTrustedProxyPolicy(t *testing.T) {
	base := DataplaneDesiredSpec{
		Posture: DataplanePostureReverseProxy, TrafficKey: "site-a",
		ListenAddress: ":18080", UpstreamURL: "http://app:8080",
	}
	plain, err := base.CalculateDigest("unit-a")
	if err != nil {
		t.Fatal(err)
	}
	base.TrustedProxyCIDRs = []string{"10.0.0.0/8"}
	trusted, err := base.CalculateDigest("unit-a")
	if err != nil {
		t.Fatal(err)
	}
	if plain == trusted {
		t.Fatal("trusted proxy policy must participate in desired spec digest")
	}
}
