package brain

import (
	"testing"

	modelv1 "yufeng/proto/gen/modelv1"
)

func TestDeriveModelGatewayStatus(t *testing.T) {
	cases := []struct {
		name       string
		configured bool
		total, ok  int64
		want       modelv1.ModelGatewayStatus
	}{
		{name: "no slot", want: modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_UNCONFIGURED},
		{name: "ready", configured: true, want: modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_READY},
		{name: "live", configured: true, total: 4, ok: 4, want: modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_LIVE},
		{name: "degraded", configured: true, total: 4, ok: 3, want: modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_DEGRADED},
		{name: "down", configured: true, total: 4, ok: 0, want: modelv1.ModelGatewayStatus_MODEL_GATEWAY_STATUS_DOWN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveModelGatewayStatus(tc.configured, tc.total, tc.ok)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestModelHostOfAndProviderCount(t *testing.T) {
	if got := modelHostOf("https://api.x.ai/v1"); got != "api.x.ai" {
		t.Fatalf("host=%s", got)
	}
	if got := modelHostOf("https://API.OpenAI.com/v1"); got != "api.openai.com" {
		t.Fatalf("host=%s", got)
	}
	if got := modelHostOf("not a url"); got != "" {
		t.Fatalf("bad url host=%s", got)
	}
	win := gatewayWindow{Providers: []gatewayProviderRow{{Host: "api.openai.com", Total: 2}}}
	if n := providerCountOf(win, "https://api.x.ai/v1"); n != 2 {
		t.Fatalf("provider_count=%d want 2 (window host + current)", n)
	}
	if n := providerCountOf(gatewayWindow{}, "https://api.x.ai/v1"); n != 1 {
		t.Fatalf("configured empty window want 1, got %d", n)
	}
	if n := providerCountOf(gatewayWindow{}, ""); n != 0 {
		t.Fatalf("empty want 0, got %d", n)
	}
}
