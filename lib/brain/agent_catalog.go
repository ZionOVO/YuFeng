package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	agentskills "yufeng/agents/skills"
	agenttools "yufeng/agents/tools"
	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
	modelv1 "yufeng/proto/gen/modelv1"
	procedurev1 "yufeng/proto/gen/procedurev1"
	toolgatewayv1 "yufeng/proto/gen/toolgatewayv1"
	toolv1 "yufeng/proto/gen/toolv1"
)

type catalogArtifact struct {
	releaseID    string
	state        string
	retireReason string
	artifact     *artifactv1.Artifact
}

func firstPartyToolRegistry() *agenttools.Registry {
	safe := func(name string) agenttools.Implementation {
		return agenttools.Implementation{Name: name, Effect: toolv1.ToolEffect_TOOL_EFFECT_SAFE, Replay: toolv1.ToolReplay_TOOL_REPLAY_SAFE}
	}
	idempotent := func(name string) agenttools.Implementation {
		return agenttools.Implementation{Name: name, Effect: toolv1.ToolEffect_TOOL_EFFECT_IDEMPOTENT, Replay: toolv1.ToolReplay_TOOL_REPLAY_SAFE}
	}
	registry, err := agenttools.NewRegistry([]agenttools.Implementation{
		safe("ticket.get"), safe("event.get"), safe("event.list"), safe("cluster.get"), safe("triage.get"), safe("release.list"), safe("case.get"),
		idempotent("session.reply"), idempotent("triage.complete"), idempotent("govern.propose"), idempotent("govern.gate"),
		idempotent("govern.start_shadow"),
		idempotent("case.request_evidence"), idempotent("run.create"), idempotent("case.complete"),
		idempotent("worker.capacity.request"),
	})
	if err != nil {
		panic(err)
	}
	return registry
}

func (s *ToolGatewayServer) listPublishedTools(ctx context.Context) ([]*toolgatewayv1.ToolDescriptorItem, error) {
	entries, err := s.activeCatalogArtifacts(ctx, artifactv1.Kind_KIND_TOOL_DESCRIPTOR)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]*toolgatewayv1.ToolDescriptorItem, 0, len(entries))
	for _, entry := range entries {
		descriptor, err := s.toolDescriptor(ctx, entry)
		if err != nil || seen[descriptor.GetName()] {
			continue
		}
		seen[descriptor.GetName()] = true
		out = append(out, &toolgatewayv1.ToolDescriptorItem{
			Name: descriptor.GetName(), Description: descriptor.GetDescription(), Permissions: descriptor.GetPermissions(),
			Version: descriptor.GetVersion(), SchemaDigest: schemaDigest(descriptor.GetInputSchema()),
			Effect: descriptor.GetEffect(), Replay: descriptor.GetReplay(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

func (s *ToolGatewayServer) lookupPublishedTool(ctx context.Context, name string) (*toolv1.ToolDescriptor, error) {
	entry, descriptor, err := s.lookupActiveTool(ctx, name, "")
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}
	return descriptor, nil
}

func (s *ToolGatewayServer) lookupActiveTool(ctx context.Context, name, version string) (*catalogArtifact, *toolv1.ToolDescriptor, error) {
	entries, err := s.activeCatalogArtifacts(ctx, artifactv1.Kind_KIND_TOOL_DESCRIPTOR)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		var descriptor toolv1.ToolDescriptor
		if protojson.Unmarshal(entry.artifact.GetPayload(), &descriptor) != nil || descriptor.GetName() != name {
			continue
		}
		if version != "" && descriptor.GetVersion() != version {
			continue
		}
		if err := s.validateToolDescriptor(ctx, &descriptor); err != nil {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return entry, &descriptor, nil
	}
	return nil, nil, nil
}

func (s *ToolGatewayServer) toolDescriptor(ctx context.Context, entry *catalogArtifact) (*toolv1.ToolDescriptor, error) {
	var descriptor toolv1.ToolDescriptor
	if err := protojson.Unmarshal(entry.artifact.GetPayload(), &descriptor); err != nil {
		return nil, err
	}
	if err := s.validateToolDescriptor(ctx, &descriptor); err != nil {
		return nil, err
	}
	return &descriptor, nil
}

func (s *ToolGatewayServer) validateToolDescriptor(ctx context.Context, descriptor *toolv1.ToolDescriptor) error {
	if descriptor == nil || strings.TrimSpace(descriptor.GetName()) == "" || strings.TrimSpace(descriptor.GetVersion()) == "" {
		return errors.New("tool descriptor name and version are required")
	}
	if !json.Valid(descriptor.GetInputSchema()) {
		return errors.New("tool descriptor input schema is invalid json")
	}
	if descriptor.GetEffect() == toolv1.ToolEffect_TOOL_EFFECT_UNSPECIFIED || descriptor.GetReplay() == toolv1.ToolReplay_TOOL_REPLAY_UNSPECIFIED {
		return errors.New("tool descriptor execution semantics are required")
	}
	if primitive := descriptor.GetBinding().GetPrimitive(); primitive != "" {
		implementation, ok := s.implementations.Lookup(primitive)
		if !ok {
			return errors.New("unknown tool implementation")
		}
		if descriptor.GetEffect() != implementation.Effect || descriptor.GetReplay() != implementation.Replay {
			return errors.New("tool descriptor execution semantics do not match implementation")
		}
		return nil
	}
	if procedure := descriptor.GetBinding().GetProcedure(); procedure != "" {
		active, err := s.activeProcedure(ctx, procedure)
		if err != nil {
			return err
		}
		if !active {
			return errors.New("tool procedure is not active")
		}
		return nil
	}
	return errors.New("tool descriptor binding is required")
}

func (s *ToolGatewayServer) activeProcedure(ctx context.Context, artifactID string) (bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT artifact FROM releases
		WHERE state IN ('shadow','canary','enforce') AND (artifact_id=$1 OR artifact->>'id'=$1)
		ORDER BY updated_at DESC LIMIT 1`, artifactID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	artifact, err := s.verifyCatalogArtifact(raw)
	if err != nil || artifact.GetKind() != artifactv1.Kind_KIND_PROCEDURE || artifact.GetPayloadSchema() != "procedure/v1" {
		return false, nil
	}
	var procedure procedurev1.Procedure
	if protojson.Unmarshal(artifact.GetPayload(), &procedure) != nil || strings.TrimSpace(procedure.GetId()) == "" ||
		strings.TrimSpace(procedure.GetVersion()) == "" || strings.TrimSpace(procedure.GetStepsSchema()) == "" || !json.Valid(procedure.GetStepsJson()) {
		return false, nil
	}
	return true, nil
}

func (s *ToolGatewayServer) activeCatalogArtifacts(ctx context.Context, kind artifactv1.Kind) ([]*catalogArtifact, error) {
	rows, err := s.pool.Query(ctx, `SELECT release_id, state, retire_reason, artifact FROM releases
		WHERE state IN ('shadow','canary','enforce') ORDER BY updated_at DESC, release_id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*catalogArtifact, 0)
	for rows.Next() {
		var entry catalogArtifact
		var raw []byte
		if err := rows.Scan(&entry.releaseID, &entry.state, &entry.retireReason, &raw); err != nil {
			return nil, err
		}
		artifact, err := s.verifyCatalogArtifact(raw)
		if err != nil || artifact.GetKind() != kind {
			continue
		}
		entry.artifact = artifact
		out = append(out, &entry)
	}
	return out, rows.Err()
}

func (s *ToolGatewayServer) verifyCatalogArtifact(raw []byte) (*artifactv1.Artifact, error) {
	var artifact artifactv1.Artifact
	if err := protojson.Unmarshal(raw, &artifact); err != nil {
		return nil, err
	}
	if err := kernel.VerifyArtifact(&artifact, s.artifactPub); err != nil {
		return nil, err
	}
	return &artifact, nil
}

// DescribeTool 返回完整工具描述并把 Schema 摘要钉死到当前 Turn。
func (s *ToolGatewayServer) DescribeTool(ctx context.Context, req *connect.Request[toolgatewayv1.DescribeToolRequest]) (*connect.Response[toolgatewayv1.DescribeToolResponse], error) {
	claims, err := s.authenticateToolHeaders(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if !claimsAllows(claims.Tools, req.Msg.GetToolName()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("tool is not allowed"))
	}
	if s.demoTriage {
		implementation, ok := s.implementations.Lookup(req.Msg.GetToolName())
		if !ok {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("tool is not found"))
		}
		descriptor := &toolv1.ToolDescriptor{Name: req.Msg.GetToolName(), Version: "1.0.0", InputSchema: []byte(`{}`), Effect: implementation.Effect, Replay: implementation.Replay}
		return connect.NewResponse(&toolgatewayv1.DescribeToolResponse{Tool: descriptor}), nil
	}
	turnID, err := s.requireCapabilityTurn(ctx, claims, req.Msg.GetTurnId())
	if err != nil {
		return nil, err
	}
	entry, descriptor, err := s.pinnedOrActiveTool(ctx, turnID, req.Msg.GetToolName(), req.Msg.GetToolVersion(), req.Msg.GetSchemaDigest())
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("tool descriptor is not active"))
	}
	if err := s.pinToolSchema(ctx, turnID, entry.artifact.GetId(), descriptor); err != nil {
		return nil, err
	}
	return connect.NewResponse(&toolgatewayv1.DescribeToolResponse{Tool: descriptor, ArtifactId: entry.artifact.GetId()}), nil
}

func (s *ToolGatewayServer) pinnedOrActiveTool(ctx context.Context, turnID, name, version, digest string) (*catalogArtifact, *toolv1.ToolDescriptor, error) {
	var pinnedVersion, artifactID, pinnedDigest string
	err := s.pool.QueryRow(ctx, `SELECT version, artifact_id, schema_digest FROM agent_turn_tool_schemas
		WHERE turn_id=$1 AND tool_name=$2`, turnID, name).Scan(&pinnedVersion, &artifactID, &pinnedDigest)
	if err == nil {
		if pinnedVersion != version || pinnedDigest != digest {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("tool schema pin does not match request"))
		}
		entry, err := s.catalogArtifactByID(ctx, artifactID)
		if err != nil {
			return nil, nil, err
		}
		if entry == nil || !catalogPinUsable(entry) {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pinned tool descriptor was revoked"))
		}
		descriptor, err := s.toolDescriptor(ctx, entry)
		return entry, descriptor, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	entry, descriptor, err := s.lookupActiveTool(ctx, name, version)
	if err != nil || entry == nil {
		return entry, descriptor, err
	}
	if schemaDigest(descriptor.GetInputSchema()) != digest {
		return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("tool schema digest changed"))
	}
	return entry, descriptor, nil
}

func (s *ToolGatewayServer) pinToolSchema(ctx context.Context, turnID, artifactID string, descriptor *toolv1.ToolDescriptor) error {
	digest := schemaDigest(descriptor.GetInputSchema())
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_turn_tool_schemas(turn_id, tool_name, version, artifact_id, schema_digest)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(turn_id,tool_name) DO NOTHING`, turnID, descriptor.GetName(), descriptor.GetVersion(), artifactID, digest); err != nil {
			return err
		}
		var storedVersion, storedArtifactID, storedDigest string
		if err := tx.QueryRow(ctx, `SELECT version, artifact_id, schema_digest FROM agent_turn_tool_schemas
			WHERE turn_id=$1 AND tool_name=$2 FOR UPDATE`, turnID, descriptor.GetName()).Scan(&storedVersion, &storedArtifactID, &storedDigest); err != nil {
			return err
		}
		if storedVersion != descriptor.GetVersion() || storedArtifactID != artifactID || storedDigest != digest {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("tool schema was concurrently pinned to another version"))
		}
		return nil
	})
}

// ListSkills 返回当前令牌可发现的已激活技能摘要。
func (s *ToolGatewayServer) ListSkills(ctx context.Context, req *connect.Request[toolgatewayv1.ListSkillsRequest]) (*connect.Response[toolgatewayv1.ListSkillsResponse], error) {
	claims, err := s.authenticateToolHeaders(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if !claimsAllows(claims.Tools, "skill.list") {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("skill.list is not allowed"))
	}
	entries, err := s.activeCatalogArtifacts(ctx, artifactv1.Kind_KIND_SKILL)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]*toolgatewayv1.SkillSummary, 0, len(entries))
	for _, entry := range entries {
		manifest, err := agentskills.Validate(entry.artifact)
		if err != nil || seen[manifest.GetSkillId()] || !skillBindingAllows(claims.Bindings, manifest.GetSkillId()) || !agentskills.CompatibleRole(claims.Role, manifest.GetCompatibleRoles()) {
			continue
		}
		seen[manifest.GetSkillId()] = true
		out = append(out, &toolgatewayv1.SkillSummary{SkillId: manifest.GetSkillId(), Version: manifest.GetVersion(), Name: manifest.GetName(), Description: manifest.GetDescription(), ContentDigest: manifest.GetContentDigest()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetSkillId() < out[j].GetSkillId() })
	return connect.NewResponse(&toolgatewayv1.ListSkillsResponse{Skills: out}), nil
}

// LoadSkill 渐进返回正文与资源，并把版本和摘要钉死到当前 Turn。
func (s *ToolGatewayServer) LoadSkill(ctx context.Context, req *connect.Request[toolgatewayv1.LoadSkillRequest]) (*connect.Response[toolgatewayv1.LoadSkillResponse], error) {
	claims, err := s.authenticateToolHeaders(ctx, req.Header())
	if err != nil {
		return nil, err
	}
	if !claimsAllows(claims.Tools, "skill.load") || !skillBindingAllows(claims.Bindings, req.Msg.GetSkillId()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("skill load is not allowed"))
	}
	turnID, err := s.requireCapabilityTurn(ctx, claims, req.Msg.GetTurnId())
	if err != nil {
		return nil, err
	}
	entry, manifest, err := s.pinnedOrActiveSkill(ctx, turnID, req.Msg.GetSkillId(), req.Msg.GetVersion(), req.Msg.GetContentDigest())
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("skill is not active"))
	}
	if !agentskills.CompatibleRole(claims.Role, manifest.GetCompatibleRoles()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("skill is not compatible with role"))
	}
	for _, required := range manifest.GetRequiredTools() {
		if !claimsAllows(claims.Tools, required) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("required tool %s is not allowed", required))
		}
	}
	effective := agentskills.EffectiveTools(manifest, func(name string) bool { return claimsAllows(claims.Tools, name) })
	if err := s.pinSkill(ctx, turnID, entry.artifact.GetId(), manifest); err != nil {
		return nil, err
	}
	return connect.NewResponse(&toolgatewayv1.LoadSkillResponse{Manifest: manifest, ArtifactId: entry.artifact.GetId(), EffectiveTools: effective}), nil
}

func (s *ToolGatewayServer) pinnedOrActiveSkill(ctx context.Context, turnID, skillID, version, digest string) (*catalogArtifact, *toolv1.SkillManifest, error) {
	var pinnedVersion, artifactID, pinnedDigest string
	err := s.pool.QueryRow(ctx, `SELECT version, artifact_id, content_digest FROM agent_turn_skills
		WHERE turn_id=$1 AND skill_id=$2`, turnID, skillID).Scan(&pinnedVersion, &artifactID, &pinnedDigest)
	if err == nil {
		if pinnedVersion != version || pinnedDigest != digest {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("skill pin does not match request"))
		}
		entry, err := s.catalogArtifactByID(ctx, artifactID)
		if err != nil {
			return nil, nil, err
		}
		if entry == nil || !catalogPinUsable(entry) {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("pinned skill was revoked"))
		}
		manifest, err := agentskills.Validate(entry.artifact)
		return entry, manifest, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	entries, err := s.activeCatalogArtifacts(ctx, artifactv1.Kind_KIND_SKILL)
	if err != nil {
		return nil, nil, err
	}
	for _, entry := range entries {
		manifest, manifestErr := agentskills.Validate(entry.artifact)
		if manifestErr != nil || manifest.GetSkillId() != skillID || manifest.GetVersion() != version {
			continue
		}
		if manifest.GetContentDigest() != digest {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("skill content digest changed"))
		}
		return entry, manifest, nil
	}
	return nil, nil, nil
}

func (s *ToolGatewayServer) pinSkill(ctx context.Context, turnID, artifactID string, manifest *toolv1.SkillManifest) error {
	ref := agentskills.Reference(manifest.GetSkillId(), manifest.GetVersion(), manifest.GetContentDigest())
	return withTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_turn_skills(turn_id, skill_id, version, artifact_id, content_digest, skill_ref)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(turn_id,skill_id) DO NOTHING`, turnID, manifest.GetSkillId(), manifest.GetVersion(), artifactID, manifest.GetContentDigest(), ref); err != nil {
			return err
		}
		var storedVersion, storedArtifactID, storedDigest string
		if err := tx.QueryRow(ctx, `SELECT version, artifact_id, content_digest FROM agent_turn_skills
			WHERE turn_id=$1 AND skill_id=$2 FOR UPDATE`, turnID, manifest.GetSkillId()).Scan(&storedVersion, &storedArtifactID, &storedDigest); err != nil {
			return err
		}
		if storedVersion != manifest.GetVersion() || storedArtifactID != artifactID || storedDigest != manifest.GetContentDigest() {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("skill was concurrently pinned to another version"))
		}
		return nil
	})
}

func (s *ToolGatewayServer) requireCapabilityTurn(ctx context.Context, claims kernel.Claims, requested string) (string, error) {
	coordinates, err := toolAuditCoordinates(ctx, s.pool, claims)
	if err != nil {
		return "", err
	}
	if requested == "" || coordinates.TurnID == "" || requested != coordinates.TurnID {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("capability does not cover turn"))
	}
	return requested, nil
}

func (s *ToolGatewayServer) catalogArtifactByID(ctx context.Context, artifactID string) (*catalogArtifact, error) {
	var entry catalogArtifact
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT release_id, state, retire_reason, artifact FROM releases
		WHERE artifact_id=$1 OR artifact->>'id'=$1 ORDER BY updated_at DESC LIMIT 1`, artifactID).
		Scan(&entry.releaseID, &entry.state, &entry.retireReason, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry.artifact, err = s.verifyCatalogArtifact(raw)
	return &entry, err
}

func catalogPinUsable(entry *catalogArtifact) bool {
	if entry == nil {
		return false
	}
	if entry.state == "shadow" || entry.state == "canary" || entry.state == "enforce" {
		return true
	}
	return entry.state == "retired" && entry.retireReason == "superseded"
}

func skillBindingAllows(bindings []string, skillID string) bool {
	want := "skill:" + skillID
	for _, binding := range bindings {
		if binding == "*" || binding == "skill:*" || binding == want {
			return true
		}
	}
	return false
}

func (s *ToolGatewayServer) filterSkillTools(ctx context.Context, claims kernel.Claims, items []*toolgatewayv1.ToolDescriptorItem) ([]*toolgatewayv1.ToolDescriptorItem, error) {
	allowed, constrained, err := s.loadedSkillTools(ctx, claims)
	if err != nil || !constrained {
		return items, err
	}
	out := make([]*toolgatewayv1.ToolDescriptorItem, 0, len(items))
	for _, item := range items {
		if item != nil && allowed[item.GetName()] {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *ToolGatewayServer) authorizeSkillTool(ctx context.Context, claims kernel.Claims, name string) error {
	allowed, constrained, err := s.loadedSkillTools(ctx, claims)
	if err != nil {
		return err
	}
	if constrained && !allowed[name] {
		return connect.NewError(connect.CodePermissionDenied, errors.New("tool is outside loaded skill subset"))
	}
	return nil
}

func (s *ToolGatewayServer) loadedSkillTools(ctx context.Context, claims kernel.Claims) (map[string]bool, bool, error) {
	coordinates, err := toolAuditCoordinates(ctx, s.pool, claims)
	if err != nil || coordinates.TurnID == "" {
		return nil, false, err
	}
	rows, err := s.pool.Query(ctx, `SELECT r.state, r.retire_reason, r.artifact FROM agent_turn_skills p
		JOIN releases r ON r.artifact_id=p.artifact_id OR r.artifact->>'id'=p.artifact_id WHERE p.turn_id=$1`, coordinates.TurnID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	allowed := make(map[string]bool)
	constrained := false
	for rows.Next() {
		var state, retireReason string
		var raw []byte
		if err := rows.Scan(&state, &retireReason, &raw); err != nil {
			return nil, false, err
		}
		entry := &catalogArtifact{state: state, retireReason: retireReason}
		entry.artifact, err = s.verifyCatalogArtifact(raw)
		if err != nil || !catalogPinUsable(entry) {
			return nil, false, connect.NewError(connect.CodeFailedPrecondition, errors.New("loaded skill was revoked"))
		}
		manifest, err := agentskills.Validate(entry.artifact)
		if err != nil {
			return nil, false, connect.NewError(connect.CodeFailedPrecondition, errors.New("loaded skill is invalid"))
		}
		constrained = true
		for _, name := range agentskills.EffectiveTools(manifest, func(name string) bool { return claimsAllows(claims.Tools, name) }) {
			allowed[name] = true
		}
	}
	return allowed, constrained, rows.Err()
}

func (s *ToolGatewayServer) validateCatalogPins(ctx context.Context, db dbTX, turnID string, manifest *modelv1.ContextManifest) error {
	if manifest == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("context manifest is required"))
	}
	wantedSkills := stringSet(manifest.GetSkillRefs())
	rows, err := db.Query(ctx, `SELECT p.skill_ref, r.state, r.retire_reason, r.artifact
		FROM agent_turn_skills p JOIN releases r ON r.artifact_id=p.artifact_id OR r.artifact->>'id'=p.artifact_id
		WHERE p.turn_id=$1`, turnID)
	if err != nil {
		return err
	}
	loadedSkills := make(map[string]bool)
	for rows.Next() {
		var ref, state, retireReason string
		var raw []byte
		if err := rows.Scan(&ref, &state, &retireReason, &raw); err != nil {
			rows.Close()
			return err
		}
		entry := &catalogArtifact{state: state, retireReason: retireReason}
		entry.artifact, err = s.verifyCatalogArtifact(raw)
		if err != nil || !catalogPinUsable(entry) {
			rows.Close()
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("loaded skill was revoked"))
		}
		loadedSkills[ref] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if !sameStringSet(wantedSkills, loadedSkills) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("context skill refs do not match turn pins"))
	}
	wantedSchemas := stringSet(manifest.GetLoadedSchemaDigests())
	schemaRows, err := db.Query(ctx, `SELECT schema_digest FROM agent_turn_tool_schemas WHERE turn_id=$1`, turnID)
	if err != nil {
		return err
	}
	loadedSchemas := make(map[string]bool)
	for schemaRows.Next() {
		var digest string
		if err := schemaRows.Scan(&digest); err != nil {
			schemaRows.Close()
			return err
		}
		loadedSchemas[digest] = true
	}
	schemaRows.Close()
	if err := schemaRows.Err(); err != nil {
		return err
	}
	if !sameStringSet(wantedSchemas, loadedSchemas) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("context schema digests do not match turn pins"))
	}
	return nil
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, value := range values {
		if value != "" {
			out[value] = true
		}
	}
	return out
}

func sameStringSet(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if !right[value] {
			return false
		}
	}
	return true
}
