package kernel

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

func TestNormalizeProducerCapabilitiesCanonicalizesAndRejectsIncompleteInput(t *testing.T) {
	input := &unitv1.ProducerCapabilities{
		Outputs: []unitv1.ProducerOutput{
			unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES,
			unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
			unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
		},
		ProjectionVersions:  []string{" event/v1 ", "event/v1"},
		ModuleCapabilities:  []string{" traffic-window/v1 ", "traffic-review-candidate/v1", "traffic-window/v1"},
		Postures:            []commonv1.IngressPosture{commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ},
		Sensors:             []unitv1.SensorType{unitv1.SensorType_SENSOR_TYPE_HTTP},
		MaxEventBatch:       1,
		MaxInFlightRequests: 1,
		MaxSpoolBytes:       1,
		MaxEvidenceEntries:  1,
	}
	got, err := NormalizeProducerCapabilities(input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.GetOutputs(), []unitv1.ProducerOutput{
		unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES,
	}) || !slices.Equal(got.GetProjectionVersions(), []string{"event/v1"}) ||
		!slices.Equal(got.GetModuleCapabilities(), []string{"traffic-review-candidate/v1", "traffic-window/v1"}) {
		t.Fatalf("capabilities were not canonicalized: %+v", got)
	}
	input.Sensors = nil
	if _, err := NormalizeProducerCapabilities(input); err == nil {
		t.Fatal("incomplete capability advertisement must fail")
	}
}

func TestNormalizeProducerCapabilitiesRequiresModelIngressHardLimit(t *testing.T) {
	input := &unitv1.ProducerCapabilities{
		Outputs:            []unitv1.ProducerOutput{unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT},
		ProjectionVersions: []string{"event/v1"},
		Postures:           []commonv1.IngressPosture{commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY},
		Sensors:            []unitv1.SensorType{unitv1.SensorType_SENSOR_TYPE_HTTP},
		LocalAsyncBypass:   true, MaxEventBatch: 1, MaxInFlightRequests: 1, MaxSpoolBytes: 1, MaxEvidenceEntries: 1,
		MaxModelIngressBatchItems: ModelIngressBatchMaxItems,
	}
	if _, err := NormalizeProducerCapabilities(input); err == nil {
		t.Fatal("local async bypass without a hard limit must fail")
	}
	input.ModelIngressHardLimit = DefaultModelIngressHardLimit()
	if _, err := NormalizeProducerCapabilities(input); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeProducerHealthRequiresDropBreakdownAndDegradationReason(t *testing.T) {
	health := &unitv1.ProducerHealth{
		HealthyProjectionVersions: []string{"event/v1"},
		DroppedLocalBypassItems:   2,
		EffectiveModelIngressWindow: &artifactv1.ModelIngressWindow{
			MaxItems: 1, MaxRetainedBytes: ModelIngressAbsoluteMinBytes, MaxQueueAge: durationpb.New(time.Second),
		},
		ModelIngressWindowState: unitv1.ModelIngressWindowState_MODEL_INGRESS_WINDOW_STATE_DEGRADED,
		ModelIngressDegradationReasons: []unitv1.ModelIngressDegradationReason{
			unitv1.ModelIngressDegradationReason_MODEL_INGRESS_DEGRADATION_REASON_MAX_ITEMS,
		},
		ModelIngressDrops: &unitv1.ModelIngressDropCounters{EvictedOldest: 1, TransportFailed: 1},
	}
	if _, err := NormalizeProducerHealth(health); err != nil {
		t.Fatal(err)
	}
	health.ModelIngressDrops.TransportFailed = 0
	if _, err := NormalizeProducerHealth(health); err == nil {
		t.Fatal("drop breakdown must equal aggregate")
	}
	health.ModelIngressDrops.TransportFailed = 1
	health.ModelIngressDegradationReasons = nil
	if _, err := NormalizeProducerHealth(health); err == nil {
		t.Fatal("degraded state without a reason must fail")
	}
}

func TestNormalizeProducerCapabilitiesRejectsInvalidModuleCapabilities(t *testing.T) {
	base := &unitv1.ProducerCapabilities{
		Outputs:             []unitv1.ProducerOutput{unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT},
		ProjectionVersions:  []string{"event/v1"},
		Postures:            []commonv1.IngressPosture{commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ},
		Sensors:             []unitv1.SensorType{unitv1.SensorType_SENSOR_TYPE_HTTP},
		MaxEventBatch:       1,
		MaxInFlightRequests: 1,
		MaxSpoolBytes:       1,
		MaxEvidenceEntries:  1,
	}
	tests := []struct {
		name  string
		value []string
	}{
		{name: "blank", value: []string{" "}},
		{name: "too long", value: []string{strings.Repeat("x", 65)}},
		{name: "too many", value: func() []string {
			values := make([]string, producerProjectionVersionLimit+1)
			for index := range values {
				values[index] = "module-" + string(rune('a'+index))
			}
			return values
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := proto.Clone(base).(*unitv1.ProducerCapabilities)
			input.ModuleCapabilities = test.value
			if _, err := NormalizeProducerCapabilities(input); err == nil {
				t.Fatal("invalid module capability advertisement must fail")
			}
		})
	}
}
