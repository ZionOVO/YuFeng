package edgecore

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"yufeng/lib/kernel"

	commonv1 "yufeng/proto/gen/commonv1"
)

// 决策纯函数的表驱动测试——这张表就是"发布状态 × 检测结论 → 最终动作"的行为说明书。
func TestDecide(t *testing.T) {
	block := Verdict{Action: ActionBlock, RuleID: "sql-union"}
	tests := []struct {
		name    string
		mode    commonv1.ReleaseMode
		verdict []Verdict
		want    Action
	}{
		{"影子模式命中转观察（只记录不拦截）", commonv1.ReleaseMode_RELEASE_MODE_SHADOW, []Verdict{block}, ActionObserve},
		{"影子模式无命中放行", commonv1.ReleaseMode_RELEASE_MODE_SHADOW, []Verdict{{Action: ActionAllow}}, ActionAllow},
		{"未指定状态不静默拦截（配置错误只观察）", commonv1.ReleaseMode_RELEASE_MODE_UNSPECIFIED, []Verdict{block}, ActionObserve},
		{"全量生效时拦截", commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, []Verdict{block}, ActionBlock},
		{"全量生效无命中放行", commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, []Verdict{{Action: ActionAllow}}, ActionAllow},
		{"小比例生效时拦截（分桶在基座阶段接入）", commonv1.ReleaseMode_RELEASE_MODE_CANARY, []Verdict{block}, ActionBlock},
		{"只有观察结论不拦截", commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, []Verdict{{Action: ActionObserve}}, ActionAllow},
		{"无结论放行", commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, nil, ActionAllow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.mode, Result{Verdicts: tt.verdict}); got != tt.want {
				t.Fatalf("Decide(%v, %v) = %v, want %v", tt.mode, tt.verdict, got, tt.want)
			}
		})
	}
}

func TestRuleDetector(t *testing.T) {
	tests := []struct {
		name string
		rule Rule
		req  Request
		want Action
	}{
		{"查询串命中联合注入", Rule{ID: "sql", Pattern: `(?i)union\s+select`}, Request{Query: "id=1+UNION+SELECT+password"}, ActionBlock},
		{"路径命中穿越", Rule{ID: "trav", Pattern: `\.\./etc/passwd`, Target: "path"}, Request{Path: "/download/../../etc/passwd"}, ActionBlock},
		{"限定 path 时查询串不命中", Rule{ID: "trav", Pattern: `\.\./etc/passwd`, Target: "path"}, Request{Query: "f=../../etc/passwd"}, ActionAllow},
		{"请求体命中脚本注入", Rule{ID: "xss", Pattern: `(?i)<script`}, Request{Body: []byte(`<script>alert(1)</script>`)}, ActionBlock},
		{"正常请求放行", Rule{ID: "sql", Pattern: `(?i)union\s+select`}, Request{Path: "/api/items", Query: "page=2"}, ActionAllow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := NewRuleDetector("test", []Rule{tt.rule})
			if err != nil {
				t.Fatal(err)
			}
			v, err := d.Evaluate(context.Background(), tt.req)
			if err != nil {
				t.Fatal(err)
			}
			if v.Action != tt.want {
				t.Fatalf("Evaluate(%+v) 动作 = %v, want %v（verdict %+v）", tt.req, v.Action, tt.want, v)
			}
		})
	}
}

// 作用范围：路径前缀外的请求不进内部检测（前缀匹配是请求期收窄）。
func TestScopedDetectorPrefix(t *testing.T) {
	inner, err := NewRuleDetector("scoped", []Rule{{ID: "sql", Pattern: `(?i)union\s+select`}})
	if err != nil {
		t.Fatal(err)
	}
	d := NewScoped(inner, "/api/")
	ctx := context.Background()

	if v, _ := d.Evaluate(ctx, Request{Path: "/api/items", Query: "id=1 union select"}); v.Action != ActionBlock {
		t.Fatalf("前缀内应命中: %+v", v)
	}
	if v, _ := d.Evaluate(ctx, Request{Path: "/comment", Query: "id=1 union select"}); v.Action != ActionAllow {
		t.Fatalf("前缀外应放行: %+v", v)
	}
	// 空前缀 = 不收窄，返回原检测器语义
	if _, ok := NewScoped(inner, "").(*ScopedDetector); ok {
		t.Fatal("空前缀不应包装")
	}
}

// 装载期必须拒绝的带病规则集——任何一条放过去都是数据面带病运行。
func TestNewRuleDetectorRejectsBadRules(t *testing.T) {
	tests := []struct {
		name  string
		rules []Rule
	}{
		{"空规则集", nil},
		{"空规则 id", []Rule{{ID: "", Pattern: "x"}}},
		{"重复规则 id", []Rule{{ID: "dup", Pattern: "x"}, {ID: "dup", Pattern: "y"}}},
		{"空正则（空模式匹配一切 = 全量拒绝）", []Rule{{ID: "r", Pattern: ""}}},
		{"未知目标（拼错不该静默扩大匹配面）", []Rule{{ID: "r", Pattern: "x", Target: "headers"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewRuleDetector("test", tt.rules); err == nil {
				t.Fatal("带病规则集竟然装载成功")
			}
		})
	}
}

// 端到端：签名制品 → 代理 → 拦截与放行 → 遥测落盘。这就是"回放测试 = 直接调用"的最小形态。
func TestProxyEndToEnd(t *testing.T) {
	pub, priv, err := newKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	rules := []Rule{{ID: "sql-union", Pattern: `(?i)union\s+select`}}
	a := newSignedArtifact(t, priv, rules)

	up := httptest.NewServer(newEchoHandler())
	defer up.Close()
	upURL := mustParseURL(t, up.URL)

	tel := &bytes.Buffer{}
	eng := NewEngine(mustDetector(t, a))
	p := NewProxy(eng, commonv1.ReleaseMode_RELEASE_MODE_ENFORCE, NewTelemetry(tel), upURL, "test-asset")
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	// 正常请求 → 200，上游响应
	if code, _ := doReq(t, proxy.URL, "/api/items?page=2"); code != 200 {
		t.Fatalf("正常请求 status = %d, want 200", code)
	}
	// 攻击请求 → 403
	if code, _ := doReq(t, proxy.URL, "/api/items?id=1+UNION+SELECT+pw"); code != 403 {
		t.Fatalf("攻击请求 status = %d, want 403", code)
	}
	// 遥测两行，第二行含拦截结论
	lines := splitLines(tel.String())
	if len(lines) != 2 {
		t.Fatalf("遥测行数 = %d, want 2:\n%s", len(lines), tel.String())
	}
	if !bytes.Contains(lines[1], []byte(`"blocked"`)) && !bytes.Contains(lines[1], []byte("VERDICT_BLOCK")) && !bytes.Contains(lines[1], []byte("block")) {
		t.Fatalf("第二行遥测缺拦截结论:\n%s", lines[1])
	}
	// 检测结论字段完整：检测器 id、置信度、修复层级、资产 id
	for _, want := range [][]byte{[]byte(`"detectorId"`), []byte(`"confidence":1`), []byte(`TIER_L1_TRAFFIC`), []byte(`"assetId":"test-asset"`)} {
		if !bytes.Contains(lines[1], want) {
			t.Fatalf("第二行遥测缺 %s:\n%s", want, lines[1])
		}
	}
	// 事件 id 为 32 字符十六进制（128 位随机），不是时间戳拼接
	if !regexpHexID.Match(lines[1]) {
		t.Fatalf("事件 id 不是 128 位十六进制:\n%s", lines[1])
	}

	// 篡改载荷后验签必须失败（id 与信封哈希不符即拒绝）
	a.Payload = []byte(`[{"id":"evil"}]`)
	if err := kernel.VerifyArtifact(a, pub); err == nil {
		t.Fatal("篡改后的制品验签竟然通过")
	}
}

// 影子模式端到端：攻击请求放行但遥测记录观察结论。
func TestProxyShadowMode(t *testing.T) {
	_, priv, _ := newKeyPair()
	a := newSignedArtifact(t, priv, []Rule{{ID: "sql", Pattern: `(?i)union\s+select`}})
	up := httptest.NewServer(newEchoHandler())
	defer up.Close()
	tel := &bytes.Buffer{}
	p := NewProxy(NewEngine(mustDetector(t, a)), commonv1.ReleaseMode_RELEASE_MODE_SHADOW,
		NewTelemetry(tel), mustParseURL(t, up.URL), "test-asset")
	proxy := httptest.NewServer(p)
	defer proxy.Close()

	if code, _ := doReq(t, proxy.URL, "/?id=1+union+select+1"); code != 200 {
		t.Fatalf("影子模式 status = %d, want 200（只记录不拦截）", code)
	}
	if !bytes.Contains(tel.Bytes(), []byte("VERDICT_OBSERVE")) && !bytes.Contains(tel.Bytes(), []byte("observe")) {
		t.Fatalf("遥测应含观察结论:\n%s", tel.String())
	}
}
