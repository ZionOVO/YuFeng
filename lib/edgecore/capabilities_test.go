package edgecore

import (
	"testing"

	unitv1 "yufeng/proto/gen/unitv1"

	"yufeng/lib/kernel"
)

func TestProducerCapabilitiesMatchRuntimeLimits(t *testing.T) {
	capabilities, err := kernel.NormalizeProducerCapabilities(ProducerCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []unitv1.ProducerOutput{
		unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_ORDINARY_SAMPLE,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES,
	} {
		found := false
		for _, got := range capabilities.GetOutputs() {
			found = found || got == output
		}
		if !found {
			t.Fatalf("missing output %s", output)
		}
	}
	if capabilities.GetMaxEventBatch() != kernel.UploadBatchMax || capabilities.GetMaxInFlightRequests() != kernel.EdgeInFlight || capabilities.GetMaxSpoolBytes() != kernel.EdgeTelemetrySpoolBytes {
		t.Fatalf("capacity advertisement=%+v", capabilities)
	}
	for _, capability := range []string{"traffic-review-candidate/v1", "traffic-window/v1"} {
		found := false
		for _, got := range capabilities.GetModuleCapabilities() {
			found = found || got == capability
		}
		if !found {
			t.Fatalf("missing module capability %s: %v", capability, capabilities.GetModuleCapabilities())
		}
	}
}
