package edgecore

import (
	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"

	"yufeng/lib/kernel"
)

const eventProjectionVersion = "event/v1"

// ProducerCapabilities 返回当前边缘二进制客观具备的生产能力。
func ProducerCapabilities() *unitv1.ProducerCapabilities {
	return &unitv1.ProducerCapabilities{
		Outputs: []unitv1.ProducerOutput{
			unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
			unitv1.ProducerOutput_PRODUCER_OUTPUT_ORDINARY_SAMPLE,
			unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES,
		},
		ProjectionVersions: []string{eventProjectionVersion},
		Postures: []commonv1.IngressPosture{
			commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY,
			commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ,
			commonv1.IngressPosture_INGRESS_POSTURE_TAP_ALERT,
			commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE,
		},
		Sensors:             []unitv1.SensorType{unitv1.SensorType_SENSOR_TYPE_HTTP, unitv1.SensorType_SENSOR_TYPE_CORAZA},
		LocalEvidenceRing:   true,
		LocalAsyncBypass:    true,
		MaxEventBatch:       kernel.UploadBatchMax,
		MaxInFlightRequests: kernel.EdgeInFlight,
		MaxSpoolBytes:       kernel.EdgeTelemetrySpoolBytes,
		MaxEvidenceEntries:  kernel.EvidenceRingMaxEntries,
		ModuleCapabilities:  []string{"traffic-review-candidate/v1", "traffic-window/v1"},
	}
}
