#!/bin/sh
# 过滤后的 go test 门禁，不是 compose 全栈，也不是贾维斯活栈证明。
# 人机交付活栈门禁只认 scripts/onboarding-live.sh（make compose-live）；本脚本通过不得当作活栈证明。
# 正则修复循环演示另见 scripts/demo-repair-loop.sh，不得当作本脚本通过证据。
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

# 每条断言对应已交付函数上的表驱动 / 集成测试（跑两遍，结果必须一致）。
run_once() {
  echo "gate: no-policy CRS hit stays 200; policy before shape; canary unit key; body ABSENT; overload fail-closed"
  go test ./lib/edgecore ./lib/replay ./procedures/http-inspection-baseline -count=1 -timeout 180s \
    -run 'TestGateOrder|TestCRS|TestCanary|TestCoverage|TestParseDiff|TestExtAuthz|TestEvaluate|TestGeneration|TestReleaseProxyFailsClosed|TestReleaseProxyCRSHit|TestReleaseProxyPolicy'

  echo "gate: production triage / cluster / propose / typed model result / outbox / live unmitigated"
  go test ./lib/brain -count=1 -timeout 180s \
    -run 'TestClusterHundred|TestDualTokenGatewayTable|TestValidateResultAgainstSignedProfile|TestModelResultIngestionIsAtomicIdempotentAndBounded|TestOutboxDeliverRestart|TestWriteRPCRole|TestRegisterCannotHijack|TestEventGetHides|TestUnitRefreshLifecycle|TestCRSHitUploadsDetectedUnmitigated|TestEnsureBootstrapJarvis|TestEnsureBootstrapAdminSeedsGrantWrite|TestAutoPromoteBlocked|TestExpireIgnoresBareTTL|TestSkipAutoPromote|TestCreateRunHatchesYufengRun|TestCreateRunWorkerFailCompensates|TestGuardWindowAverage|TestProductionPolicyEnforceThenRetireHTTP|TestCompilePolicyIntent|TestReportStepRejectsSucceeded|TestToolGatewayDemoRepairAuthorization|TestRejectPublicModelURL'

  echo "gate: identity / run fail-closed"
  go test ./lib/kernel ./agents/runtime -count=1 -timeout 180s
}

echo "production end-to-end pass 1"
run_once
echo "production end-to-end pass 2"
run_once
echo "production end-to-end ok"
