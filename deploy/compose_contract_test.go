package deploy

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestControlPlaneComposeIsProductionOnly(t *testing.T) {
	raw := readCompose(t)
	if strings.Contains(raw, "demo-triage") {
		t.Fatal("compose stack must use production triage; demo-triage belongs to make up / demo repair loop")
	}
	if strings.Contains(raw, "/keys/dev.key.hex") && strings.Contains(raw, "-signing-key") {
		t.Fatal("brain must not mount a file private key; use signing socket")
	}
	if strings.Contains(raw, "-dev-insecure") {
		t.Fatal("compose production must not use plaintext -dev-insecure")
	}
	for _, forbidden := range []string{"yfctl demo", "dev.key.hex", "demo-rules.json", "FakeScorer", "FakeProvider", "dev-fixture"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("compose production must not contain %q", forbidden)
		}
	}
	if !strings.Contains(raw, "yfctl keys") || !strings.Contains(raw, "signing.key.hex") {
		t.Fatal("compose keys service must initialize the production signing key without demo artifacts")
	}
	for _, forbidden := range []string{"compose-traffic-change-me", "compose-agent-bootstrap", "compose-unit-bootstrap", "compose-worker-bootstrap", "compose-agentd-central-pubkey", "Admin12345"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("compose production must not contain default credential or placeholder %q", forbidden)
		}
	}
	for _, required := range []string{"db_password:", "traffic_db_password:", "admin_password:", "agent_bootstrap_token:", "unit_bootstrap_token:", "modelside_result_token:", "central_worker_bootstrap_token:"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("compose production must declare the read-only secret %q", required)
		}
	}
	for _, forbidden := range []string{"YUFENG_DB_PASSWORD:?", "YUFENG_TRAFFIC_DB_PASS:?", "YUFENG_ADMIN_PASS:?", "YUFENG_AGENT_BOOTSTRAP_TOKEN:?", "YUFENG_UNIT_BOOTSTRAP_TOKEN:?", "YUFENG_CENTRAL_WORKER_BOOTSTRAP_TOKEN:?"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("compose production must not inject plaintext credential %q", forbidden)
		}
	}
	if !strings.Contains(raw, "worker-public.pem") || !strings.Contains(raw, "-central-worker-public-key-file") {
		t.Fatal("central worker identity must use the public key derived from its client certificate")
	}
	if !strings.Contains(raw, "-tls-client-ca") || !strings.Contains(raw, "https://brain:9050") {
		t.Fatal("compose production must use mutual tls to brain")
	}
}

func TestControlPlaneComposeHasNoEdgeLifecycleAuthority(t *testing.T) {
	raw := readCompose(t)
	got := composeServiceNames(raw)
	want := []string{"postgres", "traffic-role", "keys", "signer", "brain", "jarvis", "agentd"}
	if !slices.Equal(got, want) && !sameSet(got, want) {
		t.Fatalf("services=%v want control-plane closed set %v", got, want)
	}
	for _, forbidden := range []string{
		"/var/run/docker.sock", "-dataplane-control-url",
		"-dataplane-control-token-file", "dataplane.token", "yufeng_control", "-edge-image", "ONBOARDING_DEPLOY", "-model-url",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("control-plane compose must not contain %q", forbidden)
		}
	}
	if hostPublishesPort(composeServiceBlock(raw, "brain"), "18080") || hostPublishesPort(composeServiceBlock(raw, "jarvis"), "18080") {
		t.Fatal("control plane must not publish the Edge traffic port")
	}
}

func TestControlPlaneComposeCredentialsAndConfinement(t *testing.T) {
	raw := readCompose(t)
	keys := composeServiceBlock(raw, "keys")
	trafficRole := composeServiceBlock(raw, "traffic-role")
	brain := composeServiceBlock(raw, "brain")
	jarvis := composeServiceBlock(raw, "jarvis")
	agentd := composeServiceBlock(raw, "agentd")
	if keys == "" || trafficRole == "" || brain == "" || jarvis == "" || agentd == "" {
		t.Fatalf("missing control-plane service block keys=%t traffic-role=%t brain=%t jarvis=%t agentd=%t", keys != "", trafficRole != "", brain != "", jarvis != "", agentd != "")
	}
	for _, restriction := range []string{"NOINHERIT", "NOSUPERUSER", "NOCREATEDB", "NOCREATEROLE", "NOREPLICATION", "NOBYPASSRLS"} {
		if !strings.Contains(trafficRole, restriction) {
			t.Errorf("traffic database role must contain %s", restriction)
		}
	}

	if !strings.Contains(brain, "-jarvis-agent-id=jarvis-1") {
		t.Fatal("brain command text must contain -jarvis-agent-id=jarvis-1")
	}
	for _, ban := range []string{"-demo-triage", "--demo-triage", "-dev-insecure", "-signing-key"} {
		if strings.Contains(brain, ban) {
			t.Fatalf("brain must not contain %s", ban)
		}
	}
	if !strings.Contains(brain, "YUFENG_ADMIN_USER") || !strings.Contains(brain, "admin_password") {
		t.Fatal("admin username and password file must configure the bootstrap administrator")
	}
	if !strings.Contains(brain, "-bootstrap-admin-user") || !strings.Contains(brain, "-bootstrap-admin-pass-file") {
		t.Fatal("brain must pass the bootstrap administrator password by file")
	}
	if !strings.Contains(brain, "-modelside-token-file") || !strings.Contains(brain, "/run/secrets/modelside_result_token") {
		t.Fatal("brain must read the dedicated ModelSide result credential from a mounted file")
	}
	if !strings.Contains(brain, "-traffic-dsn") || !strings.Contains(brain, "yufeng_traffic@") {
		t.Fatal("brain must isolate traffic ingestion through the restricted traffic role")
	}
	if !strings.Contains(brain, "-dsn-password-file") || !strings.Contains(brain, "-traffic-dsn-password-file") {
		t.Fatal("brain database passwords must be mounted files")
	}
	if !strings.Contains(brain, "-central-worker-id=agentd-central") || !strings.Contains(brain, "-central-worker-client-cert=/agentd-public/worker-client.crt") {
		t.Fatal("brain must seed the central worker against the mounted client certificate")
	}

	if !strings.Contains(jarvis, "-jarvis-agent-id=jarvis-1") {
		t.Fatal("jarvis command text must contain -jarvis-agent-id=jarvis-1")
	}
	if !strings.Contains(jarvis, "-bootstrap-token-file") || strings.Contains(jarvis, "-bootstrap-token\n") {
		t.Fatal("jarvis bootstrap credential must be a mounted file")
	}
	if strings.Contains(jarvis, "-model-url") || strings.Contains(jarvis, "FakeProvider") {
		t.Fatal("jarvis must not pass -model-url or FakeProvider")
	}
	if strings.Contains(jarvis, "YUFENG_MODEL_API_KEY") {
		t.Fatal("jarvis must not carry YUFENG_MODEL_API_KEY")
	}
	if strings.Contains(jarvis, "/var/run/docker.sock") {
		t.Fatal("jarvis must not mount docker.sock")
	}
	if strings.Contains(jarvis, "modelside_result_token") || strings.Contains(jarvis, "unit_bootstrap_token") {
		t.Fatal("jarvis must not receive Edge or ModelSide credentials")
	}
	if !strings.Contains(agentd, "yufeng-agentd") || !strings.Contains(agentd, "-worker=agentd-central") {
		t.Fatal("central agentd must use the run supervisor entrypoint and fixed identity")
	}
	if !strings.Contains(agentd, "-worker-bootstrap-token-file") {
		t.Fatal("central agentd bootstrap credential must be a mounted file")
	}
	if strings.Contains(agentd, "/var/run/docker.sock") || strings.Contains(agentd, "YUFENG_MODEL_API_KEY") || strings.Contains(agentd, "modelside_result_token") {
		t.Fatal("central agentd must not receive docker or model inference credentials")
	}
	if !strings.Contains(agentd, "no-new-privileges:true") || !strings.Contains(agentd, "cap_drop:") {
		t.Fatal("central agentd must drop capabilities and forbid privilege elevation")
	}
	if !strings.Contains(keys, "/source/source-hmac.key") || !strings.Contains(keys, "head -c 32 /dev/urandom") {
		t.Fatal("keys must generate the Edge-only source pseudonym key")
	}
	if strings.Contains(brain, "yufeng_source") || strings.Contains(jarvis, "yufeng_source") {
		t.Fatal("brain and jarvis must not mount the source pseudonym key")
	}
}

func TestControlPlaneComposeTLSPrivateKeysHaveSinglePurposeVisibility(t *testing.T) {
	raw := readCompose(t)
	keys := composeServiceBlock(raw, "keys")
	if strings.Contains(raw, "chmod -R a+rX") {
		t.Fatal("compose must not recursively expose transport private keys")
	}
	if !strings.Contains(keys, "yufeng_tls_authority:/authority") {
		t.Fatal("one-shot key initializer must own the private certificate authority volume")
	}
	for _, service := range []string{"signer", "brain", "jarvis", "agentd"} {
		block := composeServiceBlock(raw, service)
		if strings.Contains(block, "yufeng_tls_authority") || strings.Contains(block, "/authority") {
			t.Fatalf("%s must not receive certificate authority private material", service)
		}
	}
	checks := map[string]string{
		"brain":  "yufeng_tls_brain:/brain-tls:ro",
		"jarvis": "yufeng_tls_jarvis:/tls:ro",
		"agentd": "yufeng_tls_agentd:/tls:ro",
	}
	for service, volume := range checks {
		block := composeServiceBlock(raw, service)
		if !strings.Contains(block, volume) {
			t.Fatalf("%s must mount only its dedicated transport material", service)
		}
	}
	if strings.Contains(composeServiceBlock(raw, "jarvis"), "server.key") ||
		strings.Contains(composeServiceBlock(raw, "agentd"), "server.key") {
		t.Fatal("clients must not receive the Brain server private key")
	}
	if !strings.Contains(composeServiceBlock(raw, "signer"), "yufeng_tls_trust:/tls-trust:ro") {
		t.Fatal("signer must receive only the public client certificate authority bundle")
	}
}

func TestEdgeModelSideComposeIsAnExplicitManualDataPlane(t *testing.T) {
	raw, err := os.ReadFile("compose.edge-modelside.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	got := composeServiceNames(text)
	want := []string{"modelside", "edge"}
	if !slices.Equal(got, want) && !sameSet(got, want) {
		t.Fatalf("edge extension services=%v want %v", got, want)
	}
	for _, required := range []string{
		"deploy/edge.Dockerfile", "components/modelside/Dockerfile", "unix:///run/yufeng/modelside.sock",
		"YUFENG_BRAIN_URL", "YUFENG_EDGE_UNIT:?", "YUFENG_MODELSIDE_ID:?", "YUFENG_MODELSIDE_WEIGHTS_DIR:?",
		"unit_bootstrap_token", "modelside_result_token", "yufeng_modelside_socket", "no-new-privileges:true", "cap_drop:",
		"http://127.0.0.1:19092/ready",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("manual Edge/ModelSide compose missing %q", required)
		}
	}
	for _, forbidden := range []string{"/var/run/docker.sock", "-dev-insecure", "-dev-deterministic", "redis", "nats"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Errorf("manual Edge/ModelSide compose must not contain %q", forbidden)
		}
	}
	edge := composeServiceBlock(text, "edge")
	modelside := composeServiceBlock(text, "modelside")
	if strings.Contains(edge, "modelside_result_token") || strings.Contains(modelside, "unit_bootstrap_token") {
		t.Fatal("Edge and ModelSide credentials must have single-purpose visibility")
	}
	if !strings.Contains(edge, "yufeng_source:/source:ro") || strings.Contains(modelside, "yufeng_source") {
		t.Fatal("only Edge may receive the source pseudonym key")
	}
	if !strings.Contains(edge, "YUFENG_EDGE_LISTEN_PORT") || strings.Contains(modelside, "ports:") {
		t.Fatal("only the manually started Edge publishes the default traffic port")
	}
}

func TestNativeEdgeAndModelSideServicesRemainOperatorManaged(t *testing.T) {
	for _, path := range []string{"edge/yufeng-edge.service", "edge/install-linux.sh", "edge/README.md", "modelside/yufeng-modelside.service", "modelside/README.md"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("native delivery material %s: %v", path, err)
		}
	}
	edgeService, err := os.ReadFile("edge/yufeng-edge.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"/usr/local/bin/yufeng-edge", "-brain=${YUFENG_BRAIN_URL}", "-unit=${YUFENG_EDGE_UNIT}", "-bootstrap-token-file", "-modelside=${YUFENG_MODELSIDE_ENDPOINT}"} {
		if !strings.Contains(string(edgeService), required) {
			t.Errorf("native Edge service missing %q", required)
		}
	}
	for _, forbidden := range []string{"yufeng-brain", "yufeng-jarvis", "docker.sock", "yufeng-dataplane"} {
		if strings.Contains(string(edgeService), forbidden) {
			t.Errorf("native Edge service must not contain %q", forbidden)
		}
	}
	modelService, err := os.ReadFile("modelside/yufeng-modelside.service")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"/opt/yufeng/modelside/bin/yufeng-modelside", "--brain-token-file", "--weights", "--listen=${YUFENG_MODELSIDE_LISTEN}"} {
		if !strings.Contains(string(modelService), required) {
			t.Errorf("native ModelSide service missing %q", required)
		}
	}
}

func TestContainerImagesSeparateControlPlaneEdgeAndModelSide(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "VITE_API_MODE") {
		t.Fatal("image console build must not select a simulated runtime mode")
	}
	if !strings.Contains(text, "/usr/share/yufeng/console") {
		t.Fatal("image must install console dist for /app hosting")
	}
	for _, forbidden := range []string{"./cmd/yufeng-edge", "./cmd/yufeng-dataplane", "/out/yufeng-edge", "/out/yufeng-dataplane"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("control-plane image must not package %q", forbidden)
		}
	}
	edge, err := os.ReadFile("edge.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edge), "./cmd/yufeng-edge") || strings.Contains(string(edge), "yufeng-brain") {
		t.Fatal("Edge image must contain only the native Edge entrypoint")
	}
	for _, required := range []string{"org.opencontainers.image.title=\"yufeng-edge\"", "org.opencontainers.image.version", "org.opencontainers.image.revision"} {
		if !strings.Contains(string(edge), required) {
			t.Errorf("Edge image missing release identity %q", required)
		}
	}
	modelside, err := os.ReadFile("../components/modelside/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(modelside), `ENTRYPOINT ["yufeng-modelside"]`) {
		t.Fatal("ModelSide image must use the standalone Python entrypoint")
	}
	for _, required := range []string{"org.opencontainers.image.title=\"yufeng-modelside\"", "org.opencontainers.image.version", "org.opencontainers.image.revision"} {
		if !strings.Contains(string(modelside), required) {
			t.Errorf("ModelSide image missing release identity %q", required)
		}
	}
}

func TestAllGoImagesUseRepositoryToolchain(t *testing.T) {
	for _, path := range []string{
		"Dockerfile",
		"edge.Dockerfile",
		"testdata/Dockerfile",
		"testdata/performance-load.Dockerfile",
		"envoy/testdata/Dockerfile",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "FROM golang:1.27.0-alpine AS build") {
			t.Errorf("%s must build with Go 1.27.0", path)
		}
	}
}

func TestPerformanceLoadImageCopiesGeneratedContractClosure(t *testing.T) {
	raw, err := os.ReadFile("testdata/performance-load.Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "COPY proto/gen ./proto/gen") {
		t.Fatal("performance load image must copy the complete generated contract closure required by lib/kernel")
	}
}

func TestDockerBuildContextExcludesLocalSecretsAndOutputs(t *testing.T) {
	raw, err := os.ReadFile("../.dockerignore")
	if err != nil {
		t.Fatal(err)
	}
	patterns := strings.Fields(string(raw))
	for _, want := range []string{".git", ".env", ".tmp", "console/node_modules", "console/dist", "research"} {
		if !slices.Contains(patterns, want) {
			t.Errorf("docker build context must exclude %q", want)
		}
	}
}

func TestRealTargetFixtureIsOutsideProductionCompose(t *testing.T) {
	base := readCompose(t)
	if strings.Contains(base, "testapp-a") || strings.Contains(base, "testapp-b") {
		t.Fatal("production compose must not ship the test upstream as a service")
	}
	extension, err := os.ReadFile("compose.test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(extension)
	for _, want := range []string{"testapp-a:", "testapp-b:", "deploy/testdata/Dockerfile"} {
		if !strings.Contains(text, want) {
			t.Errorf("test compose missing %q", want)
		}
	}
	source, err := os.ReadFile("testdata/upstream/main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RawQuery", "r.Header.Clone()", "X-Upstream-Name", `strings.TrimPrefix(r.URL.Path, "/status/")`, `r.URL.Path == "/upgrade"`, "101 Switching Protocols"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("test upstream missing %q", want)
		}
	}
}

func TestReverseProxyReferencePreservesProtocolAndRollback(t *testing.T) {
	raw, err := os.ReadFile("reverse-proxy/nginx-site.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"listen 443 ssl", "proxy_http_version 1.1", "proxy_set_header Host $host",
		"proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for",
		"proxy_set_header Upgrade $http_upgrade",
		"proxy_set_header Connection $yufeng_connection_upgrade",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("reverse proxy reference missing %q", want)
		}
	}
	runbook, err := os.ReadFile("reverse-proxy/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"恢复入口提交前的回源池配置", "不调用中台", "nginx -t && nginx -s reload"} {
		if !strings.Contains(string(runbook), want) {
			t.Errorf("reverse proxy rollback missing %q", want)
		}
	}
}

func TestEnvoyReferenceFreezesAuthorizationBoundary(t *testing.T) {
	raw, err := os.ReadFile("envoy/envoy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"envoy.filters.http.ext_authz",
		"timeout: 0.1s",
		"failure_mode_allow: false",
		"code: ServiceUnavailable",
		"max_request_bytes: 65536",
		"allow_partial_message: true",
		"exact: content-type",
		"exact: x-forwarded-for",
		"exact: x-forwarded-proto",
		"exact: x-request-id",
		"exact: authorization",
		"exact: cookie",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Envoy reference missing %q", want)
		}
	}
	if strings.Index(text, "envoy.filters.http.ext_authz") > strings.Index(text, "envoy.filters.http.router") {
		t.Fatal("external authorization filter must run before router")
	}
	for _, forbidden := range []string{"transport_socket:", "tls_certificate", "private_key"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Envoy reference must not make yufeng a TLS terminator: found %q", forbidden)
		}
	}

	compose, err := os.ReadFile("envoy/compose.integration.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "envoyproxy/envoy:v1.38.0") {
		t.Fatal("Envoy integration image must be pinned")
	}
	runbook, err := os.ReadFile("envoy/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"恢复接入前的 Envoy 监听器配置", "不调用中台", "make envoy-live"} {
		if !strings.Contains(string(runbook), want) {
			t.Errorf("Envoy runbook missing %q", want)
		}
	}
}

func readCompose(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func composeServiceNames(raw string) []string {
	lines := strings.Split(raw, "\n")
	in := false
	var names []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if !in {
			if trim == "services:" {
				in = true
			}
			continue
		}
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(trim, ":") {
			names = append(names, strings.TrimSuffix(trim, ":"))
		}
	}
	return names
}

func composeServiceBlock(raw, name string) string {
	needle := "\n  " + name + ":\n"
	idx := strings.Index(raw, needle)
	if idx < 0 {
		if strings.HasPrefix(raw, "  "+name+":\n") {
			idx = 0
			needle = "  " + name + ":\n"
		} else {
			return ""
		}
	}
	rest := raw[idx+len(needle):]
	next := len(rest)
	for _, other := range composeServiceNames(raw) {
		if other == name {
			continue
		}
		mark := "\n  " + other + ":\n"
		if j := strings.Index(rest, mark); j >= 0 && j < next {
			next = j
		}
	}
	if j := strings.Index(rest, "\nvolumes:\n"); j >= 0 && j < next {
		next = j
	}
	return needle + rest[:next]
}

func hostPublishesPort(block, port string) bool {
	for _, line := range strings.Split(block, "\n") {
		trim := strings.TrimSpace(line)
		if !strings.Contains(trim, port) {
			continue
		}
		if strings.Contains(trim, "\""+port+":") || strings.Contains(trim, "'"+port+":") || strings.Contains(trim, "- "+port+":") {
			return true
		}
		if strings.Contains(trim, ":"+port+"\"") || strings.Contains(trim, ":"+port+"'") {
			// container-only mapping like "127.0.0.1:19091:19091" still publishes
			if strings.Count(trim, port) >= 1 && strings.Contains(trim, ":") {
				left := trim
				left = strings.Trim(left, "- ")
				left = strings.Trim(left, `"'`)
				parts := strings.Split(left, ":")
				if len(parts) >= 2 && (parts[0] == port || (len(parts) >= 3 && parts[1] == port) || (len(parts) == 2 && parts[0] == port)) {
					return true
				}
			}
		}
	}
	return false
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	cp := append([]string(nil), got...)
	slices.Sort(cp)
	w := append([]string(nil), want...)
	slices.Sort(w)
	return slices.Equal(cp, w)
}
