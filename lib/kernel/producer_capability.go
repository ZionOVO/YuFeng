package kernel

import (
	"errors"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"

	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"
)

const producerProjectionVersionLimit = 16

// NormalizeProducerCapabilities 校验并返回去重升序的生产能力副本。
func NormalizeProducerCapabilities(in *unitv1.ProducerCapabilities) (*unitv1.ProducerCapabilities, error) {
	if in == nil {
		return nil, nil
	}
	out := proto.Clone(in).(*unitv1.ProducerCapabilities)
	outputs := make(map[unitv1.ProducerOutput]struct{}, len(out.Outputs))
	for _, value := range out.Outputs {
		if value < unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT || value > unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES {
			return nil, errors.New("producer capabilities contain invalid output")
		}
		outputs[value] = struct{}{}
	}
	out.Outputs = out.Outputs[:0]
	for value := range outputs {
		out.Outputs = append(out.Outputs, value)
	}
	slices.Sort(out.Outputs)

	versions := make(map[string]struct{}, len(out.ProjectionVersions))
	for _, value := range out.ProjectionVersions {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 {
			return nil, errors.New("producer capabilities contain invalid projection version")
		}
		versions[value] = struct{}{}
	}
	if len(versions) > producerProjectionVersionLimit {
		return nil, errors.New("producer capabilities contain too many projection versions")
	}
	out.ProjectionVersions = out.ProjectionVersions[:0]
	for value := range versions {
		out.ProjectionVersions = append(out.ProjectionVersions, value)
	}
	slices.Sort(out.ProjectionVersions)

	postures := make(map[commonv1.IngressPosture]struct{}, len(out.Postures))
	for _, value := range out.Postures {
		if value < commonv1.IngressPosture_INGRESS_POSTURE_REVERSE_PROXY || value > commonv1.IngressPosture_INGRESS_POSTURE_MIRROR_OBSERVE {
			return nil, errors.New("producer capabilities contain invalid posture")
		}
		postures[value] = struct{}{}
	}
	out.Postures = out.Postures[:0]
	for value := range postures {
		out.Postures = append(out.Postures, value)
	}
	slices.Sort(out.Postures)

	sensors := make(map[unitv1.SensorType]struct{}, len(out.Sensors))
	for _, value := range out.Sensors {
		if value < unitv1.SensorType_SENSOR_TYPE_HTTP || value > unitv1.SensorType_SENSOR_TYPE_CORAZA {
			return nil, errors.New("producer capabilities contain invalid sensor")
		}
		sensors[value] = struct{}{}
	}
	out.Sensors = out.Sensors[:0]
	for value := range sensors {
		out.Sensors = append(out.Sensors, value)
	}
	slices.Sort(out.Sensors)

	moduleCapabilities := make(map[string]struct{}, len(out.ModuleCapabilities))
	for _, value := range out.ModuleCapabilities {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 {
			return nil, errors.New("producer capabilities contain invalid module capability")
		}
		moduleCapabilities[value] = struct{}{}
	}
	if len(moduleCapabilities) > producerProjectionVersionLimit {
		return nil, errors.New("producer capabilities contain too many module capabilities")
	}
	out.ModuleCapabilities = out.ModuleCapabilities[:0]
	for value := range moduleCapabilities {
		out.ModuleCapabilities = append(out.ModuleCapabilities, value)
	}
	slices.Sort(out.ModuleCapabilities)
	if len(out.Outputs) == 0 || len(out.ProjectionVersions) == 0 || len(out.Postures) == 0 || len(out.Sensors) == 0 {
		return nil, errors.New("producer capabilities are incomplete")
	}
	if out.MaxEventBatch == 0 || out.MaxInFlightRequests == 0 || out.MaxSpoolBytes == 0 || out.MaxEvidenceEntries == 0 {
		return nil, errors.New("producer capability capacities must be positive")
	}
	return out, nil
}

// NormalizeProducerHealth 校验并返回去重升序的生产健康副本。
func NormalizeProducerHealth(in *unitv1.ProducerHealth) (*unitv1.ProducerHealth, error) {
	if in == nil {
		return nil, nil
	}
	out := proto.Clone(in).(*unitv1.ProducerHealth)
	versions := make(map[string]struct{}, len(out.HealthyProjectionVersions))
	for _, value := range out.HealthyProjectionVersions {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 64 {
			return nil, errors.New("producer health contains invalid projection version")
		}
		versions[value] = struct{}{}
	}
	if len(versions) > producerProjectionVersionLimit {
		return nil, errors.New("producer health contains too many projection versions")
	}
	out.HealthyProjectionVersions = out.HealthyProjectionVersions[:0]
	for value := range versions {
		out.HealthyProjectionVersions = append(out.HealthyProjectionVersions, value)
	}
	slices.Sort(out.HealthyProjectionVersions)
	return out, nil
}
