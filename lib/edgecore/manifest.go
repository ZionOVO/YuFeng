package edgecore

import (
	"crypto/ed25519"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

// SignDetectorManifest 签发一份核心规则集清单，供世代选装。
func SignDetectorManifest(priv ed25519.PrivateKey) (*artifactv1.Artifact, error) {
	return SignNamedDetectorManifest(priv, "crs")
}

// SignNamedDetectorManifest 签发指定标识的检测器清单。
func SignNamedDetectorManifest(priv ed25519.PrivateKey, detectorID string) (*artifactv1.Artifact, error) {
	if detectorID == "" {
		detectorID = "crs"
	}
	man := &artifactv1.DetectorManifest{
		DetectorId:    detectorID,
		Version:       kernel.CRSVersion,
		TarballSha256: kernel.CRSTarballSHA256,
		GoModule:      kernel.CRSGoModule,
		Paranoia:      kernel.CRSParanoia,
	}
	if detectorID != "crs" {
		man.Version = "test"
		man.TarballSha256 = "test"
		man.GoModule = "test"
	}
	payload, err := protojson.Marshal(man)
	if err != nil {
		return nil, err
	}
	a := &artifactv1.Artifact{
		Kind:          artifactv1.Kind_KIND_DETECTOR_MANIFEST,
		Payload:       payload,
		PayloadSchema: DetectorManifestSchema,
		Ttl:           durationpb.New(defaultReleaseTTL),
		CreatedAt:     timestamppb.Now(),
		CreatedBy:     "system",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		return nil, fmt.Errorf("sign detector manifest: %w", err)
	}
	return a, nil
}

// SignTaxonomyMapper 签发分类映射器。
func SignTaxonomyMapper(priv ed25519.PrivateKey, mapper *artifactv1.TaxonomyMapper) (*artifactv1.Artifact, error) {
	if mapper == nil {
		mapper = DefaultTaxonomyMapper()
	}
	payload, err := protojson.Marshal(mapper)
	if err != nil {
		return nil, err
	}
	a := &artifactv1.Artifact{
		Kind:          artifactv1.Kind_KIND_TAXONOMY_MAPPER,
		Payload:       payload,
		PayloadSchema: TaxonomyMapperSchema,
		Ttl:           durationpb.New(defaultReleaseTTL),
		CreatedAt:     timestamppb.Now(),
		CreatedBy:     "system",
	}
	if err := kernel.SignArtifact(a, priv); err != nil {
		return nil, fmt.Errorf("sign taxonomy mapper: %w", err)
	}
	return a, nil
}

// InstallSignedCRS 把核心规则集清单装进发布集（测试与演示装配）。
func InstallSignedCRS(set *ReleaseSet, pub ed25519.PublicKey, priv ed25519.PrivateKey) error {
	a, err := SignDetectorManifest(priv)
	if err != nil {
		return err
	}
	return set.Apply(&artifactv1.ReleaseItem{ReleaseId: "rel-core-rule-set-manifest", Artifact: a, Mode: 0}, pub)
}
