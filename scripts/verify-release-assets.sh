#!/usr/bin/env bash
# 复核已经存在的软件发布制品集；本脚本不执行任何构建或资产覆盖。
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

release_dir=${1:-dist/release}
if [[ "$release_dir" != /* ]]; then
  release_dir="$repo_root/$release_dir"
fi
version=$(tr -d '[:space:]' < VERSION)
source_commit=${RELEASE_SOURCE_COMMIT:-$(git rev-parse HEAD)}
workflow_arguments=()
if [[ -n "${RELEASE_WORKFLOW_RUN:-}" ]]; then
  workflow_arguments=(--workflow-run "$RELEASE_WORKFLOW_RUN")
fi

python3 scripts/release-artifacts.py verify \
  --directory "$release_dir" \
  --version "$version" \
  --source-commit "$source_commit" \
  "${workflow_arguments[@]}"

verify_root=$(mktemp -d "${TMPDIR:-/tmp}/yufeng-release-verify.XXXXXX")
trap 'rm -rf "$verify_root"' EXIT

platforms=(linux-amd64 linux-arm64 linux-mips windows-amd64 darwin-amd64 darwin-arm64 console modelside-python deployment)
for platform in "${platforms[@]}"; do
  mkdir -p "$verify_root/$platform"
  tar -xzf "$release_dir/yufeng-$version-$platform.tar.gz" -C "$verify_root/$platform"
done

verify_go_binary() {
  local binary=$1
  go version -m "$binary" >/dev/null
}

linux_commands=(yfctl yufeng-agentd yufeng-brain yufeng-dataplane yufeng-edge yufeng-host yufeng-jarvis yufeng-run)
for command in "${linux_commands[@]}"; do
  binary="$verify_root/linux-amd64/linux-amd64/bin/$command"
  verify_go_binary "$binary"
  "$binary" -h >/dev/null 2>&1
done
for command in yufeng-edge yufeng-host yufeng-jarvis yufeng-agentd yufeng-run; do
  verify_go_binary "$verify_root/linux-arm64/linux-arm64/bin/$command"
done
for command in yufeng-edge yufeng-host; do
  verify_go_binary "$verify_root/linux-mips/linux-mips/bin/$command"
done
for command in yufeng-jarvis yufeng-agentd yufeng-run; do
  verify_go_binary "$verify_root/windows-amd64/windows-amd64/bin/$command.exe"
  verify_go_binary "$verify_root/darwin-amd64/darwin-amd64/bin/$command"
  verify_go_binary "$verify_root/darwin-arm64/darwin-arm64/bin/$command"
done

test -s "$verify_root/console/console/index.html"
wheel_count=$(find "$verify_root/modelside-python/modelside-python/wheels" -maxdepth 1 -type f -name 'yufeng_modelside-*.whl' | wc -l | tr -d '[:space:]')
test "$wheel_count" = "1"
wheel=$(find "$verify_root/modelside-python/modelside-python/wheels" -maxdepth 1 -type f -name 'yufeng_modelside-*.whl')
python3 -m zipfile -t "$wheel" >/dev/null
test -s "$verify_root/deployment/deployment/compose.yaml"
test -s "$verify_root/deployment/deployment/compose.edge-modelside.yaml"
test -s "$verify_root/deployment/deployment/secrets/README.md"
if find "$verify_root/deployment/deployment/secrets" -mindepth 1 -maxdepth 1 ! -name README.md | grep -q .; then
  echo "deployment archive contains secret material" >&2
  exit 1
fi

edge_image="yufeng-edge:$version"
modelside_image="yufeng-modelside:$version"
docker load -i "$release_dir/yufeng-$version-edge-image-linux-amd64.tar.gz" >/dev/null
docker load -i "$release_dir/yufeng-$version-modelside-image-linux-amd64.tar.gz" >/dev/null
for image in "$edge_image" "$modelside_image"; do
  test "$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.version" }}')" = "$version"
  test "$(docker image inspect "$image" --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}')" = "$source_commit"
done
docker run --rm "$edge_image" -h >/dev/null
docker run --rm "$modelside_image" --help >/dev/null

echo "verified release artifact set: $release_dir"
