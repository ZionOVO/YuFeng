package brain

import (
	"fmt"
	"testing"
	"time"

	assetv1 "yufeng/proto/gen/assetv1"
	commonv1 "yufeng/proto/gen/commonv1"
)

// fakeRow 把内存值喂给 scanXxx 系列，免数据库即可测行扫描逻辑。
type fakeRow struct{ cols []any }

func (f fakeRow) Scan(dest ...any) error {
	if len(dest) != len(f.cols) {
		return fmt.Errorf("fakeRow: want %d destinations, got %d", len(f.cols), len(dest))
	}
	for i, c := range f.cols {
		switch d := dest[i].(type) {
		case *string:
			*d = c.(string)
		case *int64:
			*d = c.(int64)
		case *time.Time:
			*d = c.(time.Time)
		default:
			return fmt.Errorf("fakeRow: unsupported destination %T", dest[i])
		}
	}
	return nil
}

// 回归：transports 曾反序列化到临时对象后整体丢弃，读路径永远为空。
// 写入列由 assetColumns 产出，scanAsset 必须完整还原。
func TestScanAssetRoundTrip(t *testing.T) {
	in := &assetv1.Asset{
		Id:          "asset-1",
		DisplayName: "演示资产",
		AccessMode:  commonv1.AccessMode_ACCESS_MODE_REMOTE,
		Criticality: assetv1.Criticality_CRITICALITY_P1,
		MaxAutoTier: commonv1.Tier_TIER_L2_RUNTIME,
		Labels:      map[string]string{"env": "prod"},
		Transports: []*assetv1.Transport{
			{Kind: assetv1.Transport_KIND_SSH, Endpoint: "ssh://10.0.0.1:22"},
			{Kind: assetv1.Transport_KIND_LOCAL, Endpoint: "unix:///run/yufeng.sock"},
		},
		Capabilities: &assetv1.CapabilityMatrix{KernelVersion: "6.1.0", BpfLsm: true},
	}
	transports, capabilities, labels, err := assetColumns(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := scanAsset(fakeRow{cols: []any{in.Id, in.DisplayName, "remote", transports, capabilities, "p1", "L2", labels, int64(1), time.Unix(1, 0).UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Transports) != 2 {
		t.Fatalf("transports 未还原: %+v", out.Transports)
	}
	if out.Transports[0].Kind != assetv1.Transport_KIND_SSH || out.Transports[0].Endpoint != "ssh://10.0.0.1:22" {
		t.Fatalf("transports[0] 内容异常: %+v", out.Transports[0])
	}
	if out.Labels["env"] != "prod" {
		t.Fatalf("labels 未还原: %v", out.Labels)
	}
	if out.Capabilities == nil || out.Capabilities.KernelVersion != "6.1.0" || !out.Capabilities.BpfLsm {
		t.Fatalf("capabilities 未还原: %+v", out.Capabilities)
	}
}

func TestScanAssetRejectsCorruptTransports(t *testing.T) {
	zero := time.Time{}
	if _, err := scanAsset(fakeRow{cols: []any{"a-1", "n", "network", "not-json", "{}", "p2", "L1", "{}", int64(1), zero}}); err == nil {
		t.Fatal("损坏的 transports 列必须报错，不得静默清空")
	}
}
