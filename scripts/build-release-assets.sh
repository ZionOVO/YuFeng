#!/usr/bin/env bash
# 从当前精确提交构建一次固定的软件发布制品集；输出目录必须尚不存在。
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_root"

output=${1:-dist/release}
if [[ "$output" != /* ]]; then
  output="$repo_root/$output"
fi
if [[ -e "$output" ]]; then
  echo "release output already exists: $output" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain=v1 --untracked-files=all)" ]]; then
  echo "release build requires a clean worktree" >&2
  exit 1
fi

version=$(tr -d '[:space:]' < VERSION)
if [[ ! "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION is not a semantic version: $version" >&2
  exit 1
fi
plain_version=${version#v}
console_version=$(node -p "require('./console/package.json').version")
lock_versions=$(node -p "[require('./console/package-lock.json').version, require('./console/package-lock.json').packages[''].version].join(',')")
modelside_version=$(python3 -c 'import tomllib; print(tomllib.load(open("components/modelside/pyproject.toml", "rb"))["project"]["version"])')
modelside_runtime_version=$(python3 -c 'import sys; sys.path.insert(0, "components/modelside"); import yufeng_modelside; print(yufeng_modelside.__version__)')
for declared_version in "$console_version" "$modelside_version" "$modelside_runtime_version"; do
  if [[ "$declared_version" != "$plain_version" ]]; then
    echo "release version sources disagree: VERSION=$version component=$declared_version" >&2
    exit 1
  fi
done
if [[ "$lock_versions" != "$plain_version,$plain_version" ]]; then
  echo "release package lock versions disagree: VERSION=$version lock=$lock_versions" >&2
  exit 1
fi

source_commit=$(git rev-parse HEAD)
if [[ -n "${GITHUB_SHA:-}" && "$source_commit" != "$GITHUB_SHA" ]]; then
  echo "release checkout does not match GITHUB_SHA" >&2
  exit 1
fi
workflow_run=${GITHUB_RUN_ID:-local-$source_commit}
generated_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
built_at=$(git show -s --format=%cI "$source_commit")

work_root=$(mktemp -d "${TMPDIR:-/tmp}/yufeng-release.XXXXXX")
trap 'rm -rf "$work_root"' EXIT
stage="$work_root/stage"
mkdir -p "$stage" "$output"

build_binary() {
  local target_os=$1
  local target_arch=$2
  local command=$3
  local destination=$4
  local linker_flags=()
  case "$command" in
    yufeng-brain)
      linker_flags=(-ldflags "-X main.version=$version -X main.sha=$source_commit -X main.builtAt=$built_at")
      ;;
    yufeng-agentd)
      linker_flags=(-ldflags "-X main.version=$version")
      ;;
  esac
  CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
    go build -buildvcs=true -trimpath "${linker_flags[@]}" -o "$destination" "./cmd/$command"
}

for platform in linux-amd64 linux-arm64 linux-mips windows-amd64 darwin-amd64 darwin-arm64; do
  mkdir -p "$stage/$platform/bin"
done

linux_commands=(yfctl yufeng-agentd yufeng-brain yufeng-dataplane yufeng-edge yufeng-host yufeng-jarvis yufeng-run)
for command in "${linux_commands[@]}"; do
  build_binary linux amd64 "$command" "$stage/linux-amd64/bin/$command"
done
for command in yufeng-edge yufeng-host yufeng-jarvis yufeng-agentd yufeng-run; do
  build_binary linux arm64 "$command" "$stage/linux-arm64/bin/$command"
done
for command in yufeng-edge yufeng-host; do
  build_binary linux mips "$command" "$stage/linux-mips/bin/$command"
done
for command in yufeng-jarvis yufeng-agentd yufeng-run; do
  build_binary windows amd64 "$command" "$stage/windows-amd64/bin/$command.exe"
  build_binary darwin amd64 "$command" "$stage/darwin-amd64/bin/$command"
  build_binary darwin arm64 "$command" "$stage/darwin-arm64/bin/$command"
done

for platform in linux-amd64 linux-arm64 windows-amd64 darwin-amd64 darwin-arm64; do
  mkdir -p "$stage/$platform/agentd"
  cp deploy/agentd/README.md "$stage/$platform/agentd/"
done
cp deploy/agentd/install-linux.sh "$stage/linux-amd64/agentd/"
cp deploy/agentd/install-linux.sh "$stage/linux-arm64/agentd/"
cp deploy/agentd/install-macos.sh "$stage/darwin-amd64/agentd/"
cp deploy/agentd/install-macos.sh "$stage/darwin-arm64/agentd/"
cp deploy/agentd/Install-Windows.ps1 "$stage/windows-amd64/agentd/"
chmod +x "$stage/linux-amd64/agentd/install-linux.sh" "$stage/linux-arm64/agentd/install-linux.sh" \
  "$stage/darwin-amd64/agentd/install-macos.sh" "$stage/darwin-arm64/agentd/install-macos.sh"

for platform in linux-amd64 linux-arm64 linux-mips; do
  mkdir -p "$stage/$platform/edge"
  cp deploy/edge/README.md deploy/edge/edge.env.example deploy/edge/yufeng-edge.service \
    deploy/edge/install-linux.sh "$stage/$platform/edge/"
  chmod +x "$stage/$platform/edge/install-linux.sh"
done

(
  cd console
  npm ci
  npm run build
)
cp -R console/dist "$stage/console"

mkdir -p "$stage/modelside-python/wheels" "$stage/modelside-python/service"
python3 -m pip wheel --no-deps --wheel-dir "$stage/modelside-python/wheels" components/modelside
cp components/modelside/README.md "$stage/modelside-python/"
cp deploy/modelside/README.md deploy/modelside/modelside.env.example deploy/modelside/yufeng-modelside.service \
  "$stage/modelside-python/service/"

mkdir -p "$stage/deployment/edge" "$stage/deployment/modelside" "$stage/deployment/secrets"
cp deploy/compose.yaml deploy/compose.edge-modelside.yaml deploy/.env.example deploy/README.md "$stage/deployment/"
cp deploy/edge/README.md deploy/edge/edge.env.example deploy/edge/install-linux.sh deploy/edge/yufeng-edge.service \
  "$stage/deployment/edge/"
cp deploy/modelside/README.md deploy/modelside/modelside.env.example deploy/modelside/yufeng-modelside.service \
  "$stage/deployment/modelside/"
cp deploy/secrets/README.md "$stage/deployment/secrets/"

for platform in linux-amd64 linux-arm64 linux-mips windows-amd64 darwin-amd64 darwin-arm64 console modelside-python deployment; do
  COPYFILE_DISABLE=1 tar -czf "$output/yufeng-$version-$platform.tar.gz" -C "$stage" "$platform"
done

edge_image="yufeng-edge:$version"
modelside_image="yufeng-modelside:$version"
docker build --platform linux/amd64 \
  --build-arg VERSION="$version" --build-arg SHA="$source_commit" --build-arg BUILT_AT="$built_at" \
  -f deploy/edge.Dockerfile -t "$edge_image" .
docker build --platform linux/amd64 \
  --build-arg VERSION="$version" --build-arg SHA="$source_commit" --build-arg BUILT_AT="$built_at" \
  -f components/modelside/Dockerfile -t "$modelside_image" .
docker save "$edge_image" | gzip -n > "$output/yufeng-$version-edge-image-linux-amd64.tar.gz"
docker save "$modelside_image" | gzip -n > "$output/yufeng-$version-modelside-image-linux-amd64.tar.gz"

python3 scripts/release-artifacts.py seal \
  --directory "$output" \
  --version "$version" \
  --source-commit "$source_commit" \
  --workflow-run "$workflow_run" \
  --generated-at "$generated_at"
echo "sealed release artifact set: $output"
