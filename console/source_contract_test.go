package console_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 控制台源文件必须覆盖控制面引导、人工 Edge 接入、授予约束、会话与幂等键复用。
func TestConsoleSourcesCoverSetupEnrollmentGrantAndSession(t *testing.T) {
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
	for _, phrase := range []string{"配置模型网关", "探测连通性", "确认贾维斯在线", "进入主控制台", "jarvisOnline", "completeOnboarding"} {
		if !strings.Contains(s, phrase) {
			t.Errorf("SetupPage missing %q", phrase)
		}
	}
	if strings.Contains(s, "label=\"业务私钥\"") || strings.Contains(s, "label=\"业务证书\"") {
		t.Fatal("setup must not collect business tls material")
	}
	for _, forbidden := range []string{"提交部署规格", "人工安装 Edge", "设置防御资产", "授权值守账户", "putDeploymentSpecification", "createAsset", "createUser", "putGrant", "deployDataplane"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("setup must not retain data-plane or account onboarding path %q", forbidden)
		}
	}

	enrollment, err := os.ReadFile(filepath.Join(root, "src", "pages", "assets", "EdgeEnrollmentCard.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	e := string(enrollment)
	for _, phrase := range []string{"人工 Edge 接入", "putEdgeEnrollment", "入口姿态", "监听地址", "反向代理上游", "流量键", "可信代理网段", "所需文件", "人工安装命令", "期望监听计划", "实际监听计划", "ModelSide"} {
		if !strings.Contains(e, phrase) {
			t.Errorf("EdgeEnrollmentCard missing %q", phrase)
		}
	}
	for _, forbidden := range []string{"docker compose", "systemctl restart", "bootstrap_token", "private_key"} {
		if strings.Contains(e, forbidden) {
			t.Errorf("EdgeEnrollmentCard must not execute lifecycle actions or expose secrets: %q", forbidden)
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
	if !strings.Contains(c, "'PutEdgeEnrollment'") || strings.Contains(c, "'PutDeploymentSpecification'") || strings.Contains(c, "'DeployDataplane'") {
		t.Fatal("ConnectClient must use AssetService.PutEdgeEnrollment and omit retired deployment methods")
	}
}
