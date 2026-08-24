package edgecore

import (
	"fmt"
	"sync"

	"yufeng/lib/kernel"
	artifactv1 "yufeng/proto/gen/artifactv1"
)

// InspectorFactory 按已签名清单构造同步眼睛。
type InspectorFactory func(manifest *artifactv1.DetectorManifest) (Inspector, error)

var (
	inspectorMu        sync.RWMutex
	inspectorFactories = map[string]InspectorFactory{}
	inspectorCacheMu   sync.Mutex
	inspectorCache     = map[string]Inspector{}
)

// RegisterInspector 在编译期登记一只同步眼睛。重复标识覆盖。
func RegisterInspector(id string, f InspectorFactory) {
	if id == "" || f == nil {
		return
	}
	inspectorMu.Lock()
	inspectorFactories[id] = f
	inspectorMu.Unlock()
}

// LookupInspector 查找登记的工厂。
func LookupInspector(id string) (InspectorFactory, bool) {
	inspectorMu.RLock()
	defer inspectorMu.RUnlock()
	f, ok := inspectorFactories[id]
	return f, ok
}

// CompileInspector 按清单选装眼睛。未登记的标识报错，不回落到进程单例。
func CompileInspector(manifest *artifactv1.DetectorManifest) (Inspector, error) {
	if manifest == nil || manifest.DetectorId == "" {
		return nil, fmt.Errorf("detector manifest is incomplete")
	}
	f, ok := LookupInspector(manifest.DetectorId)
	if !ok {
		return nil, fmt.Errorf("inspector %s is not registered", manifest.DetectorId)
	}
	key := manifest.DetectorId + "@" + manifest.Version + "@" + manifest.TarballSha256
	inspectorCacheMu.Lock()
	defer inspectorCacheMu.Unlock()
	if ins, hit := inspectorCache[key]; hit {
		return ins, nil
	}
	ins, err := f(manifest)
	if err != nil {
		return nil, err
	}
	inspectorCache[key] = ins
	return ins, nil
}

func init() {
	RegisterInspector("crs", compileCRS)
}

func compileCRS(manifest *artifactv1.DetectorManifest) (Inspector, error) {
	if manifest != nil {
		if manifest.Version != "" && manifest.Version != kernel.CRSVersion {
			return nil, fmt.Errorf("unsupported crs version %s", manifest.Version)
		}
		if manifest.TarballSha256 != "" && manifest.TarballSha256 != kernel.CRSTarballSHA256 {
			return nil, fmt.Errorf("crs digest mismatch")
		}
	}
	return NewCorazaDetector()
}
