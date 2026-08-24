package brain

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/encoding/protojson"

	modulev1 "yufeng/proto/gen/modulev1"
	"yufeng/proto/gen/modulev1/modulev1connect"
	unitv1 "yufeng/proto/gen/unitv1"
)

const moduleCapabilityFreshness = 2 * time.Minute

type defenseModuleRegistration interface {
	descriptor() *modulev1.DefenseModule
}

type trafficInterceptionModule struct{}

func (trafficInterceptionModule) descriptor() *modulev1.DefenseModule {
	return &modulev1.DefenseModule{
		ModuleId: "traffic-interception", DisplayName: "流量拦截", Version: "1",
		RequiredProducerCapabilities: []string{"traffic-window/v1", "traffic-review-candidate/v1"},
		CaseActivitySchemas:          []string{"traffic-review/v1"},
		Surfaces: []modulev1.ModuleSurface{
			modulev1.ModuleSurface_MODULE_SURFACE_ASSET_BADGE,
			modulev1.ModuleSurface_MODULE_SURFACE_CASE_CARD,
			modulev1.ModuleSurface_MODULE_SURFACE_CASE_WORKSPACE,
			modulev1.ModuleSurface_MODULE_SURFACE_STATISTICS,
		},
	}
}

func compiledDefenseModules() []defenseModuleRegistration {
	return []defenseModuleRegistration{trafficInterceptionModule{}}
}

// ModuleCatalogServer 返回编译进二进制的防御模块目录。
type ModuleCatalogServer struct{ pool *pgxpool.Pool }

// NewModuleCatalogServer 构造模块目录服务。
func NewModuleCatalogServer(pool *pgxpool.Pool) *ModuleCatalogServer {
	return &ModuleCatalogServer{pool: pool}
}

// Handler 返回 Connect 服务端处理器。
func (s *ModuleCatalogServer) Handler() (string, http.Handler) {
	return modulev1connect.NewModuleCatalogServiceHandler(s, handlerOptions()...)
}

// ListModules 返回编译期注册且按当前 Edge 生产能力标记激活状态的模块。
// 服务端不会下发代码或任意布局。
func (s *ModuleCatalogServer) ListModules(ctx context.Context, req *connect.Request[modulev1.ListModulesRequest]) (*connect.Response[modulev1.ListModulesResponse], error) {
	if _, err := requireUser(ctx, s.pool, req); err != nil {
		return nil, err
	}
	modules := make([]*modulev1.DefenseModule, 0, len(compiledDefenseModules()))
	for _, registration := range compiledDefenseModules() {
		descriptor := registration.descriptor()
		active, err := s.edgeSupportsModule(ctx, descriptor.GetRequiredProducerCapabilities())
		if err != nil {
			return nil, err
		}
		descriptor.Active = active
		modules = append(modules, descriptor)
	}
	return connect.NewResponse(&modulev1.ListModulesResponse{Modules: modules}), nil
}

func (s *ModuleCatalogServer) edgeSupportsModule(ctx context.Context, required []string) (bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT producer_capabilities FROM units
		WHERE kind='edge' AND last_heartbeat_at IS NOT NULL AND last_heartbeat_at>$1`, time.Now().Add(-moduleCapabilityFreshness))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var capabilities unitv1.ProducerCapabilities
		if err := protojson.Unmarshal(raw, &capabilities); err != nil {
			return false, fmt.Errorf("decode edge producer capabilities: %w", err)
		}
		if producerCapabilitiesCover(capabilities.GetModuleCapabilities(), required) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, nil
}

func producerCapabilitiesCover(advertised, required []string) bool {
	for _, capability := range required {
		if !slices.Contains(advertised, capability) {
			return false
		}
	}
	return true
}
