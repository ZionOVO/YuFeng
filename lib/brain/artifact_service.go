package brain

import (
	"context"
	"encoding/base64"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	artifactv1 "yufeng/proto/gen/artifactv1"
	"yufeng/proto/gen/artifactv1/artifactv1connect"
	commonv1 "yufeng/proto/gen/commonv1"

	"yufeng/lib/observability"
)

// releaseFeedPageSize 是增量拉取的单页上限；HasMore 与之联动。
const releaseFeedPageSize = 200

// generationPageRows 是 ListGenerations 单次最多返回的信封数；与字节预算一起限制追赶响应。
const generationPageRows = 32

// unitListenPlanPageRows 是单元监听计划单次追赶的最大信封数。
const unitListenPlanPageRows = 32

// ArtifactServer 是单元制品下发服务。
type ArtifactServer struct {
	pool *pgxpool.Pool
}

// NewArtifactServer 构造制品下发服务。
func NewArtifactServer(pool *pgxpool.Pool) *ArtifactServer { return &ArtifactServer{pool: pool} }

// Handler 返回 Connect 服务端处理器。
func (s *ArtifactServer) Handler() (string, http.Handler) {
	return artifactv1connect.NewArtifactServiceHandler(s, handlerOptions()...)
}

// ListReleases 支持全量快照与游标增量。
func (s *ArtifactServer) ListReleases(ctx context.Context, req *connect.Request[artifactv1.ListReleasesRequest]) (*connect.Response[artifactv1.ListReleasesResponse], error) {
	started := time.Now()
	defer func() {
		observability.Default().Set(observability.MetricReleaseSyncDelay, time.Since(started).Seconds())
	}()
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	resp := &artifactv1.ListReleasesResponse{}
	budget := ClampArtifactBytes(req.Msg.GetMaxBytes())
	_ = budget
	if req.Msg.FullSnapshot || req.Msg.Cursor == "" || isSnapshotCursor(req.Msg.Cursor) {
		resp.Snapshot = true
		offset := 0
		if off, ok := decodeSnapshotCursor(req.Msg.Cursor); ok {
			offset = off
		}
		rows, err := s.pool.Query(ctx, `SELECT r.release_id, r.artifact, r.state, r.canary_percent, ua.asset_id, r.updated_at
		FROM releases r
		JOIN release_assets ra ON ra.release_id = r.release_id
		JOIN unit_assets ua ON ua.asset_id = ra.asset_id
		WHERE ua.unit_id=$1 AND r.state IN ('shadow','canary','enforce')
		ORDER BY r.updated_at, r.release_id`, unitID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		used := 0
		skipped := 0
		for rows.Next() {
			item, err := scanReleaseItem(rows)
			if err != nil {
				return nil, err
			}
			if skipped < offset {
				skipped++
				continue
			}
			raw, err := protojson.Marshal(item)
			if err != nil {
				return nil, err
			}
			if used+len(raw) > budget && len(resp.Items) > 0 {
				resp.HasMore = true
				break
			}
			used += len(raw)
			resp.Items = append(resp.Items, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if resp.HasMore {
			resp.NextCursor = encodeSnapshotCursor(offset + len(resp.Items))
		} else {
			// 整份快照已取完：游标锚定该单元当前最大 seq，之后走增量。
			var maxSeq int64
			if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq),0) FROM release_feed WHERE unit_id=$1`, unitID).Scan(&maxSeq); err != nil {
				return nil, err
			}
			resp.NextCursor = encodeCursor(maxSeq)
		}
		if gen, err := latestUnitGeneration(ctx, s.pool, unitID); err != nil {
			return nil, err
		} else {
			resp.Generation = gen
		}
		return connect.NewResponse(resp), nil
	}
	seq, ok := decodeCursor(req.Msg.Cursor)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid cursor"))
	}
	rows, err := s.pool.Query(ctx, `SELECT release_id, artifact, mode, canary_percent, asset_id, changed_at, seq
	FROM release_feed WHERE unit_id=$1 AND seq>$2 ORDER BY seq LIMIT `+strconv.Itoa(releaseFeedPageSize), unitID, seq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// lastSeq 必须取本批最后一行的真实 seq：release_feed 的 seq 是全单元共享的
	// bigserial，本单元的行之间有空洞，"起始 seq + 行数"会跳过未推送条目。
	lastSeq := seq
	used := 0
	for rows.Next() {
		var id, raw, mode string
		var assetID string
		var percent int32
		var at time.Time
		var rowSeq int64
		if err := rows.Scan(&id, &raw, &mode, &percent, &assetID, &at, &rowSeq); err != nil {
			return nil, err
		}
		if used+len(raw) > budget && len(resp.Items) > 0 {
			resp.HasMore = true
			break
		}
		used += len(raw)
		lastSeq = rowSeq
		var a artifactv1.Artifact
		if err := protojson.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		item := &artifactv1.ReleaseItem{ReleaseId: id, Artifact: &a, AssetId: assetID, Mode: releaseModeEnum(mode), CanaryPercent: percent}
		if mode == "retired" {
			item.Retired = true
			item.RetireReason = commonv1.RetireReason_RETIRE_REASON_UNSPECIFIED
		}
		resp.Items = append(resp.Items, item)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(resp.Items) > 0 {
		resp.NextCursor = encodeCursor(lastSeq)
		resp.HasMore = len(resp.Items) == releaseFeedPageSize
	} else {
		resp.NextCursor = req.Msg.Cursor
	}
	if gen, err := latestUnitGeneration(ctx, s.pool, unitID); err != nil {
		return nil, err
	} else {
		resp.Generation = gen
	}
	return connect.NewResponse(resp), nil
}

func latestUnitGeneration(ctx context.Context, pool *pgxpool.Pool, unitID string) (*artifactv1.AssetGeneration, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT g.envelope FROM asset_generations g
		JOIN unit_assets ua ON ua.asset_id=g.asset_id
		WHERE ua.unit_id=$1 AND g.signed
		ORDER BY g.generation_seq DESC LIMIT 1`, unitID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g artifactv1.AssetGeneration
	if err := protojson.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	if err := ensureGenerationProducerCompatibility(ctx, pool, unitID, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// ListGenerations 按序号追赶已签名资产世代信封，单次响应受字节预算约束。
func (s *ArtifactServer) ListGenerations(ctx context.Context, req *connect.Request[artifactv1.ListGenerationsRequest]) (*connect.Response[artifactv1.ListGenerationsResponse], error) {
	unitID, err := requireUnit(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	assetID := strings.TrimSpace(req.Msg.GetAssetId())
	if assetID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("asset_id is required"))
	}
	var bound int
	err = s.pool.QueryRow(ctx, `SELECT 1 FROM unit_assets WHERE unit_id=$1 AND asset_id=$2`, unitID, assetID).Scan(&bound)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("asset is not bound to this unit"))
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT envelope FROM asset_generations WHERE asset_id=$1 AND generation_seq>$2 AND signed ORDER BY generation_seq LIMIT $3`,
		assetID, req.Msg.GetSinceSeq(), generationPageRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	budget := ClampArtifactBytes(req.Msg.GetMaxBytes())
	resp := &artifactv1.ListGenerationsResponse{}
	used := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var g artifactv1.AssetGeneration
		if err := protojson.Unmarshal(raw, &g); err != nil {
			return nil, err
		}
		if err := ensureGenerationProducerCompatibility(ctx, s.pool, unitID, &g); err != nil {
			return nil, err
		}
		encoded, err := protojson.Marshal(&g)
		if err != nil {
			return nil, err
		}
		if len(resp.Generations) >= generationPageRows {
			resp.HasMore = true
			break
		}
		if used+len(encoded) > budget && len(resp.Generations) > 0 {
			resp.HasMore = true
			break
		}
		used += len(encoded)
		resp.Generations = append(resp.Generations, &g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// ListUnitListenPlans 按版本返回当前身份单元的已签名监听计划。
func (s *ArtifactServer) ListUnitListenPlans(ctx context.Context, req *connect.Request[artifactv1.ListUnitListenPlansRequest]) (*connect.Response[artifactv1.ListUnitListenPlansResponse], error) {
	unitID, err := requireUnitRPC(ctx, s.pool, req)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(req.Msg.GetUnitId())
	if target == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unit_id is required"))
	}
	if target != unitID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("unit listen plan target is outside identity scope"))
	}
	if req.Msg.GetSinceVersion() > math.MaxInt64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("since_version is too large"))
	}
	rows, err := s.pool.Query(ctx, `SELECT envelope FROM unit_listen_plans
		WHERE unit_id=$1 AND version>$2 AND signed
		ORDER BY version LIMIT $3`, unitID, int64(req.Msg.GetSinceVersion()), unitListenPlanPageRows+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	resp := &artifactv1.ListUnitListenPlansResponse{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(resp.Plans) == unitListenPlanPageRows {
			resp.HasMore = true
			break
		}
		var plan artifactv1.UnitListenPlan
		if err := protojson.Unmarshal(raw, &plan); err != nil {
			return nil, err
		}
		if plan.GetUnitId() != unitID || plan.GetVersion() <= req.Msg.GetSinceVersion() {
			return nil, connect.NewError(connect.CodeInternal, errors.New("stored unit listen plan scope is invalid"))
		}
		resp.Plans = append(resp.Plans, &plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func scanReleaseItem(row pgx.Row) (*artifactv1.ReleaseItem, error) {
	var id string
	var raw []byte
	var state string
	var percent int32
	var assetID string
	var updatedAt time.Time
	if err := row.Scan(&id, &raw, &state, &percent, &assetID, &updatedAt); err != nil {
		return nil, err
	}
	var a artifactv1.Artifact
	if err := protojson.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	return &artifactv1.ReleaseItem{
		ReleaseId: id, Artifact: &a, AssetId: assetID,
		Mode: releaseModeEnum(state), CanaryPercent: percent, ChangedAt: timestamppb.New(updatedAt),
	}, nil
}

func releaseModeEnum(s string) commonv1.ReleaseMode {
	switch s {
	case "shadow":
		return commonv1.ReleaseMode_RELEASE_MODE_SHADOW
	case "canary":
		return commonv1.ReleaseMode_RELEASE_MODE_CANARY
	case "enforce":
		return commonv1.ReleaseMode_RELEASE_MODE_ENFORCE
	default:
		return commonv1.ReleaseMode_RELEASE_MODE_UNSPECIFIED
	}
}

// decodeCursor 把游标还原为 feed 序号；格式非法返回 ok=false。
func decodeCursor(s string) (int64, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}

// encodeCursor 把 feed 序号编码为不透明游标。
func encodeCursor(n int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(n, 10)))
}

const snapshotCursorPrefix = "s:"

func encodeSnapshotCursor(offset int) string {
	return snapshotCursorPrefix + strconv.Itoa(offset)
}

func isSnapshotCursor(s string) bool {
	return strings.HasPrefix(s, snapshotCursorPrefix)
}

func decodeSnapshotCursor(s string) (int, bool) {
	if !isSnapshotCursor(s) {
		return 0, s == ""
	}
	n, err := strconv.Atoi(strings.TrimPrefix(s, snapshotCursorPrefix))
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
