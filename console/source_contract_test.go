package console_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 控制台源文件必须含引导六步、授予约束、提案意图、canOnAsset、会话与幂等键复用。
func TestConsoleSourcesCoverSetupGrantProposeSession(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Dir(file)

	setup, err := os.ReadFile(filepath.Join(root, "src", "pages", "setup", "SetupPage.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(setup)
	for _, phrase := range []string{"配置模型", "探测连通", "提交部署规格", "人工安装 Edge", "设置防御资产", "授权值守账户", "原生 Go 二进制", "Docker Compose", "入口姿态", "流量键", "真实上游地址", "Envoy", "createUser", "putGrant", "completeOnboarding", "createAsset"} {
		if !strings.Contains(s, phrase) {
			t.Errorf("SetupPage missing %q", phrase)
		}
	}
	if !strings.Contains(s, "不是单独点探针") {
		t.Fatal("setup must state that probing is not a sixth step")
	}
	if strings.Contains(s, "label=\"业务私钥\"") || strings.Contains(s, "label=\"业务证书\"") {
		t.Fatal("setup must not collect business tls material")
	}
	for _, forbidden := range []string{"等待贾维斯", "部署本机数据面", "deployDataplane"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("setup must not retain automatic Edge deployment phrase %q", forbidden)
		}
	}

	grants, err := os.ReadFile(filepath.Join(root, "src", "pages", "grants", "GrantsPage.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	g := string(grants)
	if !strings.Contains(g, "listAssets") || !strings.Contains(g, "u.userId !== user?.userId") {
		t.Fatal("grants page must list assets and exclude self")
	}
	if strings.Contains(g, "id: '*'") || strings.Contains(g, "通配绑定") {
		t.Fatal("grants page must not offer wildcard bindings")
	}

	app, err := os.ReadFile(filepath.Join(root, "src", "App.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(app), "path=\"/setup\"") || !strings.Contains(string(app), "RequireOnboardingComplete") {
		t.Fatal("app must isolate /setup and keep the main shell behind completed onboarding")
	}
	if !strings.Contains(string(app), "path=\"/model\"") || !strings.Contains(string(app), "ModelPage") {
		t.Fatal("app must register /model behind AdminOnly")
	}

	access, err := os.ReadFile(filepath.Join(root, "src", "api", "access.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(access), "export function canOnAsset") {
		t.Fatal("canOnAsset must exist")
	}

	agent, err := os.ReadFile(filepath.Join(root, "src", "pages", "agent", "AgentPage.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agent), "useJarvisSession") {
		t.Fatal("AgentPage must use the shared Jarvis session")
	}

	sess, err := os.ReadFile(filepath.Join(root, "src", "components", "chat", "JarvisSession.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(sess)
	for _, phrase := range []string{"createSession", "sendMessage", "pollMessages", "listMessages"} {
		if !strings.Contains(js, phrase) {
			t.Errorf("JarvisSession missing %q", phrase)
		}
	}

	connect, err := os.ReadFile(filepath.Join(root, "src", "api", "connect.ts"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(connect)
	if !strings.Contains(c, "idemKeys") || !strings.Contains(c, "Idempotency-Key") {
		t.Fatal("ConnectClient must reuse Idempotency-Key")
	}
	if !strings.Contains(c, "this.idemKeys.get(digest)") {
		t.Fatal("ConnectClient must look up the reused idempotency key by digest")
	}
	if !strings.Contains(c, "GetModelGateway") || !strings.Contains(c, "UpdateModelGateway") || !strings.Contains(c, "ProbeModelGateway") {
		t.Fatal("ConnectClient must call model gateway admin RPCs")
	}
	if !strings.Contains(c, "'PutDeploymentSpecification'") || strings.Contains(c, "'DeployDataplane'") {
		t.Fatal("ConnectClient must submit the typed manual Edge deployment specification only")
	}
}
