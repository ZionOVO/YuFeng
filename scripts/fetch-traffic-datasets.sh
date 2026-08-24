#!/bin/sh
# 拉取 Edge 邻近 ModelSide 打分与智能代理治理建议所用的公开 HTTP 材料。
# 用法与取舍见 docs/development/testing/model-scoring-and-agent-rule-datasets.md。
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
dest=${YF_TRAFFIC_DATASETS_DIR:-"$repo_root/testdata/traffic-datasets"}
mkdir -p "$dest"

need() {
	if ! command -v "$1" >/dev/null 2>&1; then
		echo "missing required command: $1" >&2
		exit 1
	fi
}

# sparse_clone URL DIR BRANCH PATH...
# BRANCH 为 - 时使用仓库默认头。已有克隆会重设稀疏路径后再浅取。
sparse_clone() {
	url=$1
	dir=$2
	branch=$3
	shift 3
	if [ -d "$dir/.git" ]; then
		git -C "$dir" sparse-checkout set "$@"
		if [ "$branch" = "-" ]; then
			git -C "$dir" fetch --depth 1 origin
			git -C "$dir" checkout --detach FETCH_HEAD >/dev/null 2>&1 || git -C "$dir" checkout FETCH_HEAD
		else
			git -C "$dir" fetch --depth 1 origin "$branch"
			git -C "$dir" checkout --detach "origin/$branch" >/dev/null 2>&1 || git -C "$dir" checkout "$branch"
		fi
		return 0
	fi
	rm -rf "$dir"
	if [ "$branch" = "-" ]; then
		git clone --depth 1 --filter=blob:none --sparse "$url" "$dir"
	else
		git clone --depth 1 --branch "$branch" --filter=blob:none --sparse "$url" "$dir"
	fi
	git -C "$dir" sparse-checkout set "$@"
}

need git
need curl
need python3

# 与 procedures/http-inspection-baseline/core-rule-set-manifest.json 的 git_commit 对齐。
crs_commit=aabf675fcfc5e489b424b844e6c9f1b39802df69

crs_sparse_paths="\
	tests/regression/tests/REQUEST-930-APPLICATION-ATTACK-LFI \
	tests/regression/tests/REQUEST-931-APPLICATION-ATTACK-RFI \
	tests/regression/tests/REQUEST-932-APPLICATION-ATTACK-RCE \
	tests/regression/tests/REQUEST-934-APPLICATION-ATTACK-GENERIC \
	tests/regression/tests/REQUEST-941-APPLICATION-ATTACK-XSS \
	tests/regression/tests/REQUEST-942-APPLICATION-ATTACK-SQLI \
	rules"

echo "fetching OWASP Core Rule Set v4.25.0 regression tests and rule files"
# cone 稀疏检出只接受目录；规则文件的 paranoia 标签在 rules/ 下，筛完只把装载面写入 pl1/。
# shellcheck disable=SC2086
sparse_clone https://github.com/coreruleset/coreruleset.git "$dest/crs-4.25.0" v4.25.0 \
	$crs_sparse_paths
got=$(git -C "$dest/crs-4.25.0" rev-parse HEAD)
if [ "$got" != "$crs_commit" ]; then
	echo "crs commit $got does not match frozen $crs_commit" >&2
	exit 1
fi

echo "filtering Core Rule Set YAML to paranoia 1"
python3 - "$dest/crs-4.25.0" <<'PY'
import re, sys
from pathlib import Path

root = Path(sys.argv[1])
rules_dir = root / "rules"
tests_dir = root / "tests" / "regression" / "tests"
pl1_dir = root / "pl1"

rule_pl = {}
for conf in sorted(rules_dir.glob("REQUEST-*.conf")):
    text = conf.read_text(encoding="utf-8", errors="replace")
    # 滑动窗口会把相邻规则的 paranoia 标签串到一起；按 SecRule/SecAction 块绑定。
    for block in re.split(r"(?=^SecRule |^SecAction )", text, flags=re.M):
        pl = re.search(r"paranoia-level/(\d+)", block)
        if not pl:
            continue
        level = int(pl.group(1))
        for rid in re.findall(r"id:(\d{6,})", block):
            rule_pl[rid] = min(level, rule_pl.get(rid, 99))

if not rule_pl:
    sys.exit("no paranoia-level tags found in Core Rule Set rule files")

keep = []
skip = []
unknown = []
for yaml_path in sorted(tests_dir.rglob("*.yaml")):
    text = yaml_path.read_text(encoding="utf-8", errors="replace")
    rid_match = re.search(r"^rule_id:\s*(\d+)", text, re.M)
    rel = yaml_path.relative_to(tests_dir)
    if not rid_match:
        unknown.append(str(rel))
        continue
    rid = rid_match.group(1)
    level = rule_pl.get(rid)
    if level is None:
        unknown.append("%s (rule %s)" % (rel, rid))
    elif level <= 1:
        keep.append(rel)
    else:
        skip.append(rel)

if unknown:
    sys.exit("unmapped Core Rule Set YAML: " + ", ".join(unknown[:8]))

if pl1_dir.exists():
    for old in pl1_dir.rglob("*"):
        if old.is_file():
            old.unlink()
for rel in keep:
    dest = pl1_dir / rel
    dest.parent.mkdir(parents=True, exist_ok=True)
    dest.write_bytes((tests_dir / rel).read_bytes())

keep_list = root / "pl1-files.txt"
keep_list.write_text("".join(str(rel) + "\n" for rel in keep), encoding="utf-8")
print("paranoia-1 yaml %d (skipped %d at paranoia 2+)" % (len(keep), len(skip)))
PY

echo "fetching HttpParamsDataset payload_full.csv"
mkdir -p "$dest/http-params"
curl -fsSL -o "$dest/http-params/payload_full.csv" \
	https://raw.githubusercontent.com/Morzeux/HttpParamsDataset/master/payload_full.csv

echo "fetching CSIC 2010 original HTTP texts"
sparse_clone https://github.com/msudol/Web-Application-Attack-Datasets.git "$dest/csic-2010-src" - \
	OriginalDataSets/csic_2010
mkdir -p "$dest/csic-2010"
for f in normalTrafficTraining.txt normalTrafficTest.txt anomalousTrafficTest.txt; do
	src="$dest/csic-2010-src/OriginalDataSets/csic_2010/$f"
	if [ ! -f "$src" ]; then
		echo "csic file missing: $src" >&2
		exit 1
	fi
	cp "$src" "$dest/csic-2010/$f"
done
rm -rf "$dest/csic-2010-src"

echo "fetching WAFFLED public bypass samples, grammars, and raw request relay"
sparse_clone https://github.com/sa-akhavani/waffled.git "$dest/waffled" - \
	bypass-database fuzzer-grammar http-request-relay

echo "fetching GoTestWAF testcase generators (not replayable HTTP)"
sparse_clone https://github.com/wallarm/gotestwaf.git "$dest/gotestwaf-src" - testcases
mkdir -p "$dest/gotestwaf-cases"
rm -rf "$dest/gotestwaf-cases/testcases"
mkdir -p "$dest/gotestwaf-cases/testcases"
# 只要可展开成 HTTP 的生成器；去掉原生 gRPC 与 128 KiB 体。
for sub in owasp community false-pos; do
	if [ -d "$dest/gotestwaf-src/testcases/$sub" ]; then
		mkdir -p "$dest/gotestwaf-cases/testcases/$sub"
		for f in "$dest/gotestwaf-src/testcases/$sub"/*.yml; do
			[ -f "$f" ] || continue
			base=$(basename "$f")
			case "$base" in
			community-128kb-*) continue ;;
			esac
			cp "$f" "$dest/gotestwaf-cases/testcases/$sub/$base"
		done
	fi
done
rm -rf "$dest/gotestwaf-src"

python3 - "$dest" "$crs_commit" <<'PY'
import csv, sys
from pathlib import Path

dest = Path(sys.argv[1])
crs_commit = sys.argv[2]
lines = []
lines.append("commit_crs " + crs_commit)
pl1 = list((dest / "crs-4.25.0" / "pl1").rglob("*.yaml"))
all_yaml = list((dest / "crs-4.25.0" / "tests" / "regression" / "tests").rglob("*.yaml"))
lines.append("crs_yaml_loaded_dirs %d" % len(all_yaml))
lines.append("crs_yaml_paranoia_1 %d" % len(pl1))
hp = dest / "http-params" / "payload_full.csv"
with hp.open(newline="", encoding="utf-8") as fh:
    n = sum(1 for _ in csv.DictReader(fh))
lines.append("httpparams_rows %d" % n)
for name in ("normalTrafficTraining.txt", "normalTrafficTest.txt", "anomalousTrafficTest.txt"):
    path = dest / "csic-2010" / name
    count = 0
    with path.open(encoding="utf-8", errors="replace") as fh:
        for line in fh:
            if line.startswith(("GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ")):
                count += 1
    lines.append("csic_%s %d" % (name, count))
raw = list((dest / "waffled" / "bypass-database").rglob("*.raw"))
lines.append("waffled_raw_samples %d" % len(raw))
gt = list((dest / "gotestwaf-cases").rglob("*.yml"))
lines.append("gotestwaf_generator_yml %d" % len(gt))
lines.append("traffic_review_must_stay_off 1")
text = "\n".join(lines) + "\n"
(dest / "MANIFEST.txt").write_text(text, encoding="utf-8")
sys.stdout.write(text)
PY

echo "ok $dest"
