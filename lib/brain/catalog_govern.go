package brain

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentskills "yufeng/agents/skills"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	authv1 "yufeng/proto/gen/authv1"
	commonv1 "yufeng/proto/gen/commonv1"
	governv1 "yufeng/proto/gen/governv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

const catalogTTLMax = 365 * 24 * time.Hour

// ProposeCatalogArtifact 建立已校验但尚未签名的工具或技能目录草稿。
func (s *GovernServer) ProposeCatalogArtifact(ctx context.Context, req *connect.Request[governv1.ProposeCatalogArtifactRequest]) (*connect.Response[governv1.ProposeCatalogArtifactResponse], error) {
	user, err := catalogRequestUser(ctx, s, req)
	if err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.GetUserId(), "ProposeCatalogArtifact", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var response governv1.ProposeCatalogArtifactResponse
		if err := protojson.Unmarshal(cached, &response); err != nil {
			return nil, err
		}
		return connect.NewResponse(&response), nil
	}
	payload, err := s.normalizeCatalogPayload(ctx, req.Msg.GetKind(), req.Msg.GetPayloadSchema(), req.Msg.GetPayload())
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ttl := req.Msg.GetTtl().AsDuration()
	if ttl < 5*time.Minute || ttl > catalogTTLMax {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("catalog ttl must be between 300s and 31536000s"))
	}
	releaseID, err := newID("rel")
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	artifact := &artifactv1.Artifact{
		Kind: req.Msg.GetKind(), Payload: payload, PayloadSchema: req.Msg.GetPayloadSchema(),
		Ttl: durationpb.New(ttl), CreatedAt: timestamppb.Now(), CreatedBy: user.GetUserId(),
	}
	if _, err := kernel.NewDraft(releaseID, artifact, user.GetUserId()); err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	raw, err := protojson.Marshal(artifact)
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	response := &governv1.ProposeCatalogArtifactResponse{ReleaseId: releaseID, State: commonv1.ReleaseState_RELEASE_STATE_DRAFT, Artifact: artifact}
	err = withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO releases(release_id, state, artifact, ttl_seconds, created_by)
			VALUES($1,'draft',$2::jsonb,$3,$4)`, releaseID, string(raw), int64(ttl.Seconds()), user.GetUserId()); err != nil {
			return err
		}
		if err := appendAuditTx(ctx, tx, "user", user.GetUserId(), "catalog.propose", "release", releaseID, map[string]any{
			"kind": req.Msg.GetKind().String(), "payload_schema": req.Msg.GetPayloadSchema(),
		}); err != nil {
			return err
		}
		return storeIdempotentProtoDB(ctx, tx, idemScope(user.GetUserId(), ""), idem, digest, response)
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// SignCatalogArtifact 校验目录草稿并经制品签名根推进到 signed。
func (s *GovernServer) SignCatalogArtifact(ctx context.Context, req *connect.Request[governv1.SignCatalogArtifactRequest]) (*connect.Response[governv1.SignCatalogArtifactResponse], error) {
	user, err := catalogRequestUser(ctx, s, req)
	if err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.GetUserId(), "SignCatalogArtifact", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var response governv1.SignCatalogArtifactResponse
		if err := protojson.Unmarshal(cached, &response); err != nil {
			return nil, err
		}
		return connect.NewResponse(&response), nil
	}
	draft, err := loadDraft(ctx, s.pool, req.Msg.GetReleaseId())
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	payload, err := s.normalizeCatalogPayload(ctx, draft.Envelope.GetKind(), draft.Envelope.GetPayloadSchema(), draft.Envelope.GetPayload())
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	envelope := proto.Clone(draft.Envelope).(*artifactv1.Artifact)
	envelope.Payload = payload
	if s.artifactSigner != nil {
		err = kernel.SignArtifactWithSigner(envelope, s.artifactSigner)
	} else {
		err = kernel.SignArtifact(envelope, s.signingKey)
	}
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	if envelope.GetKind() == artifactv1.Kind_KIND_SKILL {
		if _, err := agentskills.Validate(envelope); err != nil {
			s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	signed := &kernel.Signed{ID: draft.ID, Envelope: envelope}
	var response *governv1.SignCatalogArtifactResponse
	err = commitReleaseChange(ctx, s.pool, releaseWrite{
		rel: signed, actorType: "user", actorID: user.GetUserId(), action: "catalog.sign",
		details: map[string]any{"artifact_id": envelope.GetId(), "kind": envelope.GetKind().String()},
		complete: func(tx pgx.Tx) error {
			view, err := loadReleaseView(ctx, tx, draft.ID)
			if err != nil {
				return err
			}
			response = &governv1.SignCatalogArtifactResponse{Release: releaseProto(view)}
			return storeIdempotentProtoDB(ctx, tx, idemScope(user.GetUserId(), draft.ID), idem, digest, response)
		},
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// ActivateCatalogArtifact 把 signed 目录制品显式推进到可见状态。
func (s *GovernServer) ActivateCatalogArtifact(ctx context.Context, req *connect.Request[governv1.ActivateCatalogArtifactRequest]) (*connect.Response[governv1.ActivateCatalogArtifactResponse], error) {
	user, err := catalogRequestUser(ctx, s, req)
	if err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.GetUserId(), "ActivateCatalogArtifact", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var response governv1.ActivateCatalogArtifactResponse
		if err := protojson.Unmarshal(cached, &response); err != nil {
			return nil, err
		}
		return connect.NewResponse(&response), nil
	}
	release, err := loadRelease(ctx, s.pool, req.Msg.GetReleaseId())
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	signed, ok := release.(*kernel.Signed)
	if !ok || !isCatalogKind(signed.Envelope.GetKind()) {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, gateConflict(release.State())
	}
	if err := kernel.VerifyArtifact(signed.Envelope, s.catalogPublicKey()); err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("catalog artifact signature is invalid"))
	}
	shadow := signed.StartShadow()
	var response *governv1.ActivateCatalogArtifactResponse
	err = commitReleaseChange(ctx, s.pool, releaseWrite{
		rel: shadow, actorType: "user", actorID: user.GetUserId(), action: "catalog.activate",
		details: map[string]any{"artifact_id": shadow.Envelope.GetId(), "kind": shadow.Envelope.GetKind().String()},
		complete: func(tx pgx.Tx) error {
			view, err := loadReleaseView(ctx, tx, shadow.ID)
			if err != nil {
				return err
			}
			response = &governv1.ActivateCatalogArtifactResponse{Release: releaseProto(view)}
			return storeIdempotentProtoDB(ctx, tx, idemScope(user.GetUserId(), shadow.ID), idem, digest, response)
		},
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(response), nil
}

// RevokeCatalogArtifact 把已激活目录制品推进到 retired。
func (s *GovernServer) RevokeCatalogArtifact(ctx context.Context, req *connect.Request[governv1.RevokeCatalogArtifactRequest]) (*connect.Response[governv1.RevokeCatalogArtifactResponse], error) {
	user, err := catalogRequestUser(ctx, s, req)
	if err != nil {
		return nil, err
	}
	idem, digest, cached, err := s.beginWriteIdem(ctx, user.GetUserId(), "RevokeCatalogArtifact", req.Header(), req.Msg)
	if err != nil {
		return nil, err
	}
	if cached != nil {
		var response governv1.RevokeCatalogArtifactResponse
		if err := protojson.Unmarshal(cached, &response); err != nil {
			return nil, err
		}
		return connect.NewResponse(&response), nil
	}
	release, err := loadRelease(ctx, s.pool, req.Msg.GetReleaseId())
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	if !isCatalogKind(release.Artifact().GetKind()) {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("release is not a catalog artifact"))
	}
	active, err := kernel.ActiveOf(release)
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, gateConflict(release.State())
	}
	retired, err := kernel.RetireActive(active, commonv1.RetireReason_RETIRE_REASON_MANUAL)
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	var response *governv1.RevokeCatalogArtifactResponse
	err = commitReleaseChange(ctx, s.pool, releaseWrite{
		rel: retired, retired: true, reason: commonv1.RetireReason_RETIRE_REASON_MANUAL,
		actorType: "user", actorID: user.GetUserId(), action: "catalog.revoke",
		details: map[string]any{"artifact_id": retired.Envelope.GetId(), "kind": retired.Envelope.GetKind().String()},
		complete: func(tx pgx.Tx) error {
			view, err := loadReleaseView(ctx, tx, retired.ID)
			if err != nil {
				return err
			}
			response = &governv1.RevokeCatalogArtifactResponse{Release: releaseProto(view)}
			return storeIdempotentProtoDB(ctx, tx, idemScope(user.GetUserId(), retired.ID), idem, digest, response)
		},
	})
	if err != nil {
		s.abortWriteIdem(ctx, user.GetUserId(), idem, req.Msg)
		return nil, err
	}
	return connect.NewResponse(response), nil
}

func (s *GovernServer) normalizeCatalogPayload(ctx context.Context, kind artifactv1.Kind, schema string, payload []byte) ([]byte, error) {
	if kind == artifactv1.Kind_KIND_TOOL_DESCRIPTOR && schema == "tool/v1" {
		var descriptor toolv1.ToolDescriptor
		if err := protojson.Unmarshal(payload, &descriptor); err != nil {
			return nil, err
		}
		gateway := &ToolGatewayServer{pool: s.pool, implementations: firstPartyToolRegistry(), artifactPub: s.catalogPublicKey()}
		if err := gateway.validateToolDescriptor(ctx, &descriptor); err != nil {
			return nil, err
		}
		return protojson.Marshal(&descriptor)
	}
	if kind == artifactv1.Kind_KIND_SKILL && schema == "skill/v1" {
		var manifest toolv1.SkillManifest
		if err := protojson.Unmarshal(payload, &manifest); err != nil {
			return nil, err
		}
		manifest.PublisherKeyId = s.catalogKeyID()
		if err := agentskills.ValidateManifest(&manifest, manifest.GetPublisherKeyId()); err != nil {
			return nil, err
		}
		return protojson.Marshal(&manifest)
	}
	return nil, errors.New("catalog kind and payload schema do not match")
}

func (s *GovernServer) catalogPublicKey() ed25519.PublicKey {
	if s.artifactSigner != nil {
		return s.artifactSigner.Public()
	}
	return s.signingKey.Public().(ed25519.PublicKey)
}

func (s *GovernServer) catalogKeyID() string {
	if s.artifactSigner != nil {
		return s.artifactSigner.KeyID()
	}
	return kernel.KeyID(s.catalogPublicKey())
}

func isCatalogKind(kind artifactv1.Kind) bool {
	return kind == artifactv1.Kind_KIND_TOOL_DESCRIPTOR || kind == artifactv1.Kind_KIND_SKILL
}

func catalogRequestUser[T any](ctx context.Context, s *GovernServer, req *connect.Request[T]) (*authv1.User, error) {
	if err := requireCompletedOnboarding(ctx, s.pool); err != nil {
		return nil, err
	}
	user, err := requireUser(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	if user.GetRole() != commonv1.UserRole_USER_ROLE_ADMIN {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("catalog management requires admin"))
	}
	if err := authorizeWrite(ctx, s.pool, user, "catalog.manage", "", "", s.demoTriage); err != nil {
		return nil, err
	}
	return user, nil
}
