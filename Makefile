.PHONY: build test test-development vet tidy generate demo-init run up compose-up compose-down compose-live compose-live-reset resilience-live security-live traffic-review-live performance-live backup-restore-live envoy-live delivery-evidence preflight-release-evidence archive-live-evidence

build:
	go build ./...

test:
	go test ./...
	$(MAKE) test-development

test-development:
	go test -tags yufeng_dev ./cmd/yufeng-brain ./cmd/yufeng-edge ./cmd/yfctl

vet:
	go vet ./...

tidy:
	go mod tidy

# 契约代码生成（需要 buf、protoc-gen-go 与 protoc-gen-connect-go 在 PATH）。
generate:
	cd proto && buf generate

# 纵切片演示：清理旧演示数据（含旧遥测，避免演示结果漂移）后重新生成。
demo-init:
	rm -rf .demo
	go run -tags yufeng_dev ./cmd/yfctl demo -out .demo

# 启动数据面纵切片：内置演示上游 + 本地制品 + NDJSON 遥测。
# 试一下：curl localhost:18080/api/items?page=2  → 200
#         curl localhost:18080/api/items?id=1+UNION+SELECT+pw  → 403
run:
	go run -tags yufeng_dev ./cmd/yufeng-edge -local-demo -addr :18080 -upstream builtin -artifacts .demo/artifacts -pubkey .demo/pubkey.hex -telemetry .demo/telemetry.ndjson -asset edge-demo

# 全链启动：brain + edge + 内置演示上游 + yfctl 发布。
up:
	./scripts/up.sh

# 控制面容器编排（需要 Docker Compose）：postgres + keys + signer + brain + jarvis + agentd。
compose-up:
	mkdir -p .tmp/compose-tls
	docker compose -f deploy/compose.yaml up -d --build
	@echo "waiting for brain admin /readyz"
	@i=0; \
	until curl -fsS http://127.0.0.1:19090/readyz >/dev/null 2>&1; do \
	  i=$$((i+1)); \
	  if [ $$i -ge 60 ]; then echo "brain not ready"; docker compose -f deploy/compose.yaml logs --tail=80; exit 1; fi; \
	  sleep 2; \
	done
	docker compose -f deploy/compose.yaml ps
	docker compose -f deploy/compose.yaml logs --tail=40

compose-down:
	docker compose -f deploy/compose.yaml down
	docker compose -f deploy/compose.yaml logs --tail=20 || true

# 人机交付活栈门禁：Brain 签发规格后由本脚本代表技术人员显式启动 Edge 与 ModelSide。
compose-live:
	./scripts/onboarding-live.sh live

# 显式删除本地 compose 测试卷后重跑六步引导，用于已执行过活栈门禁的开发机。
compose-live-reset:
	YUFENG_LIVE_RESET=1 ./scripts/onboarding-live.sh live

# 企业试点断网与回退演练；复用已完成引导的真实目标，不重建或清空环境。
resilience-live:
	./scripts/resilience-live.sh live

# 企业试点身份、秘密与数据泄漏负向演练；复用已完成引导的真实目标。
security-live:
	./scripts/security-live.sh live

# 企业试点真实流量审查闭环：五分钟统计窗、人工证据批准、中央短命调查与 Shadow。
traffic-review-live:
	./scripts/traffic-review-live.sh live

# 五种模型旁路状态下的每秒 2000 请求与第 99 百分位延迟验收。
performance-live:
	./scripts/performance-live.sh live

# 企业试点逻辑备份恢复：临时全新数据库逐行对比，原数据卷不删除。
backup-restore-live:
	./scripts/backup-restore-live.sh live

# Envoy 外部授权真实网关门禁：固定网关 + 真实处理器 + 回显应用。
envoy-live:
	./deploy/envoy/run-integration.sh

# 企业试点交付证据：默认跑无容器的完整静态门禁。
delivery-evidence:
	./scripts/delivery-evidence.sh static

# 对发布稳定分支的预期合并 Git 树归档完整本机静态预检。
preflight-release-evidence:
	./scripts/preflight-release-evidence.sh

# 对精确 develop 合并提交只补活栈，并与静态预检装配最终发布证据。
archive-live-evidence:
	test -n "$(YUFENG_CI_URL)"
	test -n "$(YUFENG_PREFLIGHT_MANIFEST)"
	test -n "$(YUFENG_PREFLIGHT_ARCHIVE)"
	test -n "$(YUFENG_PREFLIGHT_CHECKSUM)"
	./scripts/archive-live-evidence.sh \
		--ci-url "$(YUFENG_CI_URL)" \
		--preflight-manifest "$(YUFENG_PREFLIGHT_MANIFEST)" \
		--preflight-archive "$(YUFENG_PREFLIGHT_ARCHIVE)" \
		--preflight-checksum "$(YUFENG_PREFLIGHT_CHECKSUM)"
