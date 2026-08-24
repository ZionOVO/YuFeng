package brain

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"yufeng/lib/edgecore"
	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

func TestGenerationProducerCompatibility(t *testing.T) {
	generation := &artifactv1.AssetGeneration{Members: []*artifactv1.ReleaseItem{{
		Artifact: &artifactv1.Artifact{
			Kind:    artifactv1.Kind_KIND_DETECTOR_MANIFEST,
			Payload: []byte(`{"detectorId":"crs"}`),
		},
	}}}
	posture := commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY
	tests := []struct {
		name       string
		capability func() *unitv1.ProducerCapabilities
		wantError  bool
	}{
		{name: "compatible", capability: edgecore.ProducerCapabilities},
		{name: "missing advertisement", capability: func() *unitv1.ProducerCapabilities { return nil }, wantError: true},
		{name: "missing ticket output", capability: func() *unitv1.ProducerCapabilities {
			capability := proto.Clone(edgecore.ProducerCapabilities()).(*unitv1.ProducerCapabilities)
			capability.Outputs = capability.Outputs[:2]
			return capability
		}, wantError: true},
		{name: "missing projection", capability: func() *unitv1.ProducerCapabilities {
			capability := proto.Clone(edgecore.ProducerCapabilities()).(*unitv1.ProducerCapabilities)
			capability.ProjectionVersions = nil
			return capability
		}, wantError: true},
		{name: "missing coraza", capability: func() *unitv1.ProducerCapabilities {
			capability := proto.Clone(edgecore.ProducerCapabilities()).(*unitv1.ProducerCapabilities)
			capability.Sensors = []unitv1.SensorType{unitv1.SensorType_SENSOR_TYPE_HTTP}
			return capability
		}, wantError: true},
		{name: "missing posture", capability: func() *unitv1.ProducerCapabilities {
			capability := proto.Clone(edgecore.ProducerCapabilities()).(*unitv1.ProducerCapabilities)
			capability.Postures = []commonv1.IngressPosture{commonv1.IngressPosture_INGRESS_POSTURE_EXT_AUTHZ}
			return capability
		}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGenerationProducerCompatibility(tt.capability(), generation, posture)
			if (err != nil) != tt.wantError {
				t.Fatalf("error=%v wantError=%v", err, tt.wantError)
			}
		})
	}
}

func TestUnitProjectionPreservesTapHealth(t *testing.T) {
	for value, want := range map[string]commonv1.UnitHealth{
		"tap_silent": commonv1.UnitHealth_UNIT_HEALTH_TAP_SILENT,
		"tap_skew":   commonv1.UnitHealth_UNIT_HEALTH_TAP_SKEW,
	} {
		if got := unitHealthEnum(value); got != want {
			t.Fatalf("unitHealthEnum(%q)=%s want %s", value, got, want)
		}
	}
}
