#!/bin/sh
# 客户部署资格诊断入口；static 检查本机源码，live 只复用已完成引导的真实目标。
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mode=${1:-static}

run_govulncheck() {
  go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
}

run_golangci_lint() {
  go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run ./...
}

static_evidence() {
  delivery_go_toolchain=${YUFENG_DELIVERY_GO_TOOLCHAIN:-go1.27.0}
  GOTOOLCHAIN=$delivery_go_toolchain
  export GOTOOLCHAIN

  # 完整竞态套件已编译并执行默认测试；这里不再先跑一遍普通测试。
  go test -race ./...
  go test -tags yufeng_dev ./cmd/yufeng-brain ./cmd/yufeng-edge ./cmd/yfctl

  go_files=$(git ls-files '*.go')
  unformatted=$(gofmt -l $go_files)
  if [ -n "$unformatted" ]; then
    echo "$unformatted" >&2
    echo "Go 源文件未格式化" >&2
    exit 1
  fi
  run_golangci_lint
  run_govulncheck

  command -v buf >/dev/null 2>&1 || {
    echo "buf 不可用" >&2
    exit 2
  }
  (cd proto && buf lint && buf format --diff --exit-code && buf generate)
  buf breaking proto --against '.git#branch=main,subdir=proto'
  git diff --exit-code -- proto/gen

  (
    cd console
    npm ci
    npm test
    npm run lint
    npm run typecheck
    npm run build
  )

  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/yufeng-edge ./cmd/yufeng-host
  CGO_ENABLED=0 GOOS=linux GOARCH=mips go build ./cmd/yufeng-edge ./cmd/yufeng-host
  for target in linux/amd64 linux/arm64 windows/amd64 darwin/amd64 darwin/arm64; do
    target_os=${target%/*}
    target_arch=${target#*/}
    CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch go build ./cmd/yufeng-jarvis ./cmd/yufeng-agentd ./cmd/yufeng-run
  done
  echo "deployment static diagnostics passed"
}

live_evidence() {
  command -v docker >/dev/null 2>&1 || {
    echo "Docker 不可用" >&2
    exit 2
  }
  docker info >/dev/null 2>&1 || {
    echo "Docker 未运行" >&2
    exit 2
  }
  sh scripts/onboarding-live.sh live
  sh scripts/security-live.sh live
  sh scripts/traffic-review-live.sh live
  sh scripts/resilience-live.sh live
  sh scripts/performance-live.sh live
  sh deploy/envoy/run-integration.sh
  sh scripts/backup-restore-live.sh live
  echo "deployment live qualification passed"
}

case "$mode" in
  static)
    static_evidence
    ;;
  live)
    live_evidence
    ;;
  *)
    echo "usage: $0 [static|live]" >&2
    exit 64
    ;;
esac
