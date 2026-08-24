package brain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"
	commonv1 "yufeng/proto/gen/commonv1"
	unitv1 "yufeng/proto/gen/unitv1"

	"yufeng/lib/kernel"
)

const requiredEventProjectionVersion = "event/v1"

func ensureGenerationProducerCompatibility(ctx context.Context, pool *pgxpool.Pool, unitID string, generation *artifactv1.AssetGeneration) error {
	if generation == nil {
		return nil
	}
	capabilities, err := loadProducerCapabilities(ctx, pool, unitID)
	if err != nil {
		return err
	}
	posture, err := currentUnitPosture(ctx, pool, unitID)
	if err != nil {
		return err
	}
	return checkGenerationProducerCompatibility(capabilities, generation, posture)
}

func checkGenerationProducerCompatibility(capabilities *unitv1.ProducerCapabilities, generation *artifactv1.AssetGeneration, posture commonv1.IngressPosture) error {
	if capabilities == nil {
		return producerCapabilityMismatch("advertisement is missing")
	}
	for _, output := range []unitv1.ProducerOutput{
		unitv1.ProducerOutput_PRODUCER_OUTPUT_CRITICAL_EVENT,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_ORDINARY_SAMPLE,
		unitv1.ProducerOutput_PRODUCER_OUTPUT_TICKET_FEATURES,
	} {
		if !slices.Contains(capabilities.GetOutputs(), output) {
			return producerCapabilityMismatch("missing output " + output.String())
		}
	}
	if !slices.Contains(capabilities.GetProjectionVersions(), requiredEventProjectionVersion) {
		return producerCapabilityMismatch("missing projection " + requiredEventProjectionVersion)
	}
	if !slices.Contains(capabilities.GetSensors(), unitv1.SensorType_SENSOR_TYPE_HTTP) {
		return producerCapabilityMismatch("missing http sensor")
	}
	if generationNeedsCoraza(generation) && !slices.Contains(capabilities.GetSensors(), unitv1.SensorType_SENSOR_TYPE_CORAZA) {
		return producerCapabilityMismatch("missing coraza sensor")
	}
	if posture != commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED && !slices.Contains(capabilities.GetPostures(), posture) {
		return producerCapabilityMismatch("missing posture " + posture.String())
	}
	return nil
}

func loadProducerCapabilities(ctx context.Context, pool *pgxpool.Pool, unitID string) (*unitv1.ProducerCapabilities, error) {
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT producer_capabilities FROM units WHERE unit_id=$1`, unitID).Scan(&raw); err != nil {
		return nil, err
	}
	var capabilities unitv1.ProducerCapabilities
	if err := protojson.Unmarshal(raw, &capabilities); err != nil {
		return nil, producerCapabilityMismatch("stored advertisement is invalid")
	}
	normalized, err := kernel.NormalizeProducerCapabilities(&capabilities)
	if err != nil {
		return nil, producerCapabilityMismatch("advertisement is missing or invalid")
	}
	return normalized, nil
}

func generationNeedsCoraza(generation *artifactv1.AssetGeneration) bool {
	for _, member := range generation.GetMembers() {
		artifact := member.GetArtifact()
		if artifact.GetKind() != artifactv1.Kind_KIND_DETECTOR_MANIFEST {
			continue
		}
		var manifest artifactv1.DetectorManifest
		if protojson.Unmarshal(artifact.GetPayload(), &manifest) == nil && manifest.GetDetectorId() == "crs" {
			return true
		}
	}
	return false
}

func currentUnitPosture(ctx context.Context, pool *pgxpool.Pool, unitID string) (commonv1.IngressPosture, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT envelope FROM unit_listen_plans WHERE unit_id=$1 AND signed ORDER BY version DESC LIMIT 1`, unitID).Scan(&raw)
	if err == nil {
		var plan artifactv1.UnitListenPlan
		if protojson.Unmarshal(raw, &plan) == nil {
			return plan.GetPosture(), nil
		}
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED, err
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT posture FROM units WHERE unit_id=$1`, unitID).Scan(&stored); err != nil {
		return commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED, err
	}
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED, nil
	}
	value, ok := commonv1.IngressPosture_value[stored]
	if !ok {
		return commonv1.IngressPosture_INGRESS_POSTURE_UNSPECIFIED, producerCapabilityMismatch("stored posture is invalid")
	}
	return commonv1.IngressPosture(value), nil
}

func producerCapabilityMismatch(reason string) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("producer_capability_mismatch: %s", reason))
}
