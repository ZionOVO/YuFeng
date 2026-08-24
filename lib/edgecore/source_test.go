package edgecore

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

func TestClientSourceResolverTrustsOnlyConfiguredProxyChain(t *testing.T) {
	resolver, err := NewClientSourceResolver([]string{"10.0.0.0/8", "2001:db8:1::/48"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		remote  string
		headers http.Header
		want    string
	}{
		{name: "direct", remote: "203.0.113.9:443", want: "203.0.113.9"},
		{name: "untrusted ignores spoofed header", remote: "203.0.113.9:443", headers: http.Header{"X-Forwarded-For": {"198.51.100.4"}}, want: "203.0.113.9"},
		{name: "single trusted proxy", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4"}}, want: "198.51.100.4"},
		{name: "right to left proxy chain", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4, 10.1.0.7"}}, want: "198.51.100.4"},
		{name: "multiple header lines", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4", "10.1.0.7"}}, want: "198.51.100.4"},
		{name: "nearest untrusted proxy wins", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4, 192.0.2.7"}}, want: "192.0.2.7"},
		{name: "malformed chain falls back", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4, unknown"}}, want: "10.0.0.4"},
		{name: "address with port falls back", remote: "10.0.0.4:8080", headers: http.Header{"X-Forwarded-For": {"198.51.100.4:1234"}}, want: "10.0.0.4"},
		{name: "trusted ipv6 proxy", remote: "[2001:db8:1::7]:8080", headers: http.Header{"X-Forwarded-For": {"2001:db8:2::9"}}, want: "2001:db8:2::9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolver.Resolve(tt.remote, tt.headers)
			if got.String() != tt.want {
				t.Fatalf("source=%q want %q", got, tt.want)
			}
		})
	}
}

func TestListenPlanRequiresNormalizedTrustedProxyCIDRs(t *testing.T) {
	plan := &artifactv1.UnitListenPlan{
		UnitId: "unit-a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "site-a", Version: 1, ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
		ClientSource:       &artifactv1.ClientSourcePolicy{TrustedProxyCidrs: []string{"10.1.2.3/8"}},
		ModelIngressWindow: kernel.DefaultModelIngressWindow(),
	}
	if err := ValidateUnitListenPlan(plan); err == nil {
		t.Fatal("signed listen plan must not carry non-canonical trusted proxy cidrs")
	}
	plan.ClientSource.TrustedProxyCidrs = []string{"10.0.0.0/8"}
	if err := ValidateUnitListenPlan(plan); err != nil {
		t.Fatal(err)
	}
}

func TestListenPlanRequiresExplicitModelIngressWindow(t *testing.T) {
	plan := &artifactv1.UnitListenPlan{
		UnitId: "unit-a", Posture: commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
		TrafficKey: "site-a", Version: 1, ListenAddress: ":18080", UpstreamUrl: "http://app:8080",
	}
	if err := ValidateUnitListenPlan(plan); err == nil {
		t.Fatal("signed listen plan must contain an explicit model ingress window")
	}
	plan.ModelIngressWindow = kernel.DefaultModelIngressWindow()
	if err := ValidateUnitListenPlan(plan); err != nil {
		t.Fatal(err)
	}
}

func TestSourcePseudonymizerIsStableAndScopeSeparated(t *testing.T) {
	addr := netip.MustParseAddr("198.51.100.4")
	key := make([]byte, 32)
	first, err := NewSourcePseudonymizer(key)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSourcePseudonymizer(append([]byte(nil), key...))
	if err != nil {
		t.Fatal(err)
	}
	otherKey := make([]byte, 32)
	otherKey[0] = 1
	second, err := NewSourcePseudonymizer(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	got := first.Pseudonym(addr)
	if got == "" || got != restarted.Pseudonym(addr) {
		t.Fatal("same deployment key and address must be stable")
	}
	if got == second.Pseudonym(addr) {
		t.Fatal("different deployment keys must not be linkable")
	}
	if got == first.Pseudonym(netip.MustParseAddr("198.51.100.5")) {
		t.Fatal("different addresses must not share a pseudonym")
	}
}

func TestTrafficEventPseudonymizesClientAddress(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	pseudonymizer, err := NewSourcePseudonymizer(key)
	if err != nil {
		t.Fatal(err)
	}
	const rawAddress = "198.51.100.42"
	event := TrafficEvent("unit-a", "asset-a", "request-a", Request{
		Method: "GET", Path: "/", ClientAddress: netip.MustParseAddr(rawAddress),
	}, Decision{Action: ActionAllow}, pseudonymizer)
	if !strings.HasPrefix(event.GetHttp().GetSrcPseudonym(), "h1.") {
		t.Fatalf("pseudonym=%q", event.GetHttp().GetSrcPseudonym())
	}
	raw, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), rawAddress) || strings.Contains(event.String(), rawAddress) {
		t.Fatal("raw client address leaked into event projection")
	}
}

func TestTrafficEventExcludesRequestSecrets(t *testing.T) {
	const (
		querySecret  = "query-secret-value"
		authSecret   = "business-authorization-secret"
		cookieSecret = "business-cookie-secret"
		bodySecret   = "-----BEGIN PRIVATE KEY-----business-private-key-----END PRIVATE KEY-----"
	)
	event := TrafficEvent("unit-a", "asset-a", "request-a", Request{
		Method: "POST", Path: "/api/items", Query: "id=" + querySecret,
		Headers: map[string]string{"Authorization": authSecret, "Cookie": cookieSecret},
		Body:    []byte(bodySecret),
	}, Decision{Action: ActionAllow}, SourcePseudonymizer{})
	raw, err := protojson.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{querySecret, authSecret, cookieSecret, bodySecret} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("request secret leaked into event: %q", secret)
		}
	}
	if got := event.GetHttp().GetQueryRedacted(); got != "id=" {
		t.Fatalf("query projection=%q", got)
	}
}

func TestIngressAdaptersAttachTrustedClientSource(t *testing.T) {
	resolver, err := NewClientSourceResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := netip.MustParseAddr("198.51.100.42")

	t.Run("reverse proxy", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		defer upstream.Close()
		upstreamURL, err := url.Parse(upstream.URL)
		if err != nil {
			t.Fatal(err)
		}
		proxy := NewReleaseProxy(NewReleaseSet(), nil, upstreamURL, "asset-a")
		proxy.SetClientSourceResolver(resolver)
		var got netip.Addr
		proxy.SetObserver(func(req Request, _ Decision, _ string) { got = req.ClientAddress })

		req := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
		req.RemoteAddr = "10.0.0.4:8080"
		req.Header.Set(forwardedForHeader, want.String())
		resp := httptest.NewRecorder()
		proxy.ServeHTTP(resp, req)
		if resp.Code != http.StatusNoContent || got != want {
			t.Fatalf("status=%d source=%v want %v", resp.Code, got, want)
		}
	})

	t.Run("external authorization", func(t *testing.T) {
		var got netip.Addr
		ext := NewExtAuthz("asset-a", func(_ CanonicalView, req Request) Action {
			got = req.ClientAddress
			return ActionAllow
		})
		ext.SetClientSourceResolver(resolver)
		req := httptest.NewRequest(http.MethodGet, "http://edge.test/", nil)
		req.RemoteAddr = "10.0.0.4:8080"
		req.Header.Set(forwardedForHeader, want.String())
		resp := httptest.NewRecorder()
		ext.ServeHTTP(resp, req)
		if resp.Code != http.StatusOK || got != want {
			t.Fatalf("status=%d source=%v want %v", resp.Code, got, want)
		}
	})
}
