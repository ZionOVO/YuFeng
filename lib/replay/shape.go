package replay

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	artifactv1 "yufeng/proto/gen/artifactv1"

	"yufeng/lib/edgecore"
	"yufeng/lib/kernel"
)

// RunShape 用同一规范视图回放正向请求形状，只核算形状声明覆盖的路由。
func RunShape(ctx context.Context, artifact *artifactv1.Artifact, corpus []Case) (*artifactv1.ReplayReport, error) {
	if artifact == nil || artifact.GetPayloadSchema() != shapeSchema {
		return nil, fmt.Errorf("replay shape requires %s", shapeSchema)
	}
	var source artifactv1.ShapeSource
	if err := protojson.Unmarshal(artifact.GetPayload(), &source); err != nil {
		return nil, fmt.Errorf("shape payload: %w", err)
	}
	if err := edgecore.ValidateShapeSource(&source); err != nil {
		return nil, err
	}
	report := &artifactv1.ReplayReport{CorpusRef: "builtin:l1-shape-v1"}
	for _, sample := range corpus {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !shapeCovers(&source, sample.Request.Path) || !shapeAssetCovers(artifact.GetScope(), sample.Request.AssetID) {
			continue
		}
		view := edgecore.Canonicalize(sample.Request.Method, sample.Request.Path, sample.Request.Query, sample.Request.Headers, sample.Request.Body, edgecore.DefaultInspectionProfile())
		account(report, sample, edgecore.ShapeViolates(&source, sample.Request, view))
	}
	report.Passed = kernel.GatePassed(report)
	return report, nil
}

// shapeCovers 判断请求路径是否落在形状声明覆盖的路由内。
func shapeCovers(source *artifactv1.ShapeSource, path string) bool {
	if route := source.GetRouteTemplate(); route != "" {
		want := splitReplayPath(route)
		got := splitReplayPath(path)
		if len(want) != len(got) {
			return false
		}
		for index := range want {
			if strings.HasPrefix(want[index], "{") && strings.HasSuffix(want[index], "}") {
				if got[index] == "" {
					return false
				}
				continue
			}
			if want[index] != got[index] {
				return false
			}
		}
		return true
	}
	return strings.HasPrefix(path, source.GetPathPrefix())
}

// shapeAssetCovers 判断制品作用域是否覆盖指定资产。
func shapeAssetCovers(scope *artifactv1.Scope, assetID string) bool {
	if scope == nil || len(scope.GetAssetIds()) == 0 {
		return true
	}
	for _, allowed := range scope.GetAssetIds() {
		if allowed == assetID {
			return true
		}
	}
	return false
}

// splitReplayPath 把请求路径整理成供路由模板逐段比较的片段。
func splitReplayPath(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}
