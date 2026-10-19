# 全部走 Docker 工具链:服务器部署与 CI 构建以容器为准,本机无需安装 Go。
# CI(check job)用 setup-go 跑同口径校验;生产镜像由 Dockerfile 的 go-builder 阶段构建。

GO_IMAGE     ?= golang:1.26
SQLC_IMAGE   ?= sqlc/sqlc:latest   # 预留:新增查询可接 sqlc codegen
# 本地网络 proxy.golang.org 不可达,默认走 goproxy.cn(公网 Go 模块镜像);
# 哈希由 go.sum 锁定、CI 以官方源二次校验,供应链不受影响。CI runner 可覆盖为官方直连。
GOPROXY      ?= https://goproxy.cn,https://proxy.golang.org,direct

# dev 模式需要的环境透传(仅在调用方设置了非空值时转发;容器内 cwd=/repo/server 会自行加载 /repo/.env)。
PORT         ?= 8787
GATEWAY_PORT ?= 8788
DEV_ENV := \
  $(if $(DATABASE_URL),-e DATABASE_URL="$(DATABASE_URL)") \
  $(if $(filter-out 0 false no,$(MEMORY_DB)),-e MEMORY_DB="$(MEMORY_DB)") \
  $(if $(PLATFORM_SECRET_KEY),-e PLATFORM_SECRET_KEY="$(PLATFORM_SECRET_KEY)") \
  $(if $(PUBLIC_API_ORIGIN),-e PUBLIC_API_ORIGIN="$(PUBLIC_API_ORIGIN)") \
  $(if $(PUBLIC_APP_ORIGIN),-e PUBLIC_APP_ORIGIN="$(PUBLIC_APP_ORIGIN)") \
  $(if $(GATEWAY_BASE_URL),-e GATEWAY_BASE_URL="$(GATEWAY_BASE_URL)") \
  $(if $(GATEWAY_API_TYPE),-e GATEWAY_API_TYPE="$(GATEWAY_API_TYPE)") \
  $(if $(RELEASE_BASE_URL),-e RELEASE_BASE_URL="$(RELEASE_BASE_URL)") \
  $(if $(COOKIE_DOMAIN),-e COOKIE_DOMAIN="$(COOKIE_DOMAIN)") \
  $(if $(CONSOLE_DIST_DIR),-e CONSOLE_DIST_DIR="$(CONSOLE_DIST_DIR)") \
  $(if $(PUBLIC_CONSOLE_ORIGIN),-e PUBLIC_CONSOLE_ORIGIN="$(PUBLIC_CONSOLE_ORIGIN)") \
  $(if $(API_TRUSTED_PROXY_IPS),-e API_TRUSTED_PROXY_IPS="$(API_TRUSTED_PROXY_IPS)") \
  $(if $(TRUST_CF_CONNECTING_IP),-e TRUST_CF_CONNECTING_IP="$(TRUST_CF_CONNECTING_IP)") \
  $(if $(CONSOLE_ALLOWED_HOSTS),-e CONSOLE_ALLOWED_HOSTS="$(CONSOLE_ALLOWED_HOSTS)") \
  $(if $(SETUP_ALLOWED_HOSTS),-e SETUP_ALLOWED_HOSTS="$(SETUP_ALLOWED_HOSTS)") \
  $(if $(TURNSTILE_SITE_KEY),-e TURNSTILE_SITE_KEY="$(TURNSTILE_SITE_KEY)") \
  $(if $(TURNSTILE_SECRET_KEY),-e TURNSTILE_SECRET_KEY="$(TURNSTILE_SECRET_KEY)") \
  $(if $(TURNSTILE_VERIFY_URL),-e TURNSTILE_VERIFY_URL="$(TURNSTILE_VERIFY_URL)") \
  $(if $(BOOTSTRAP_SECRET),-e BOOTSTRAP_SECRET="$(BOOTSTRAP_SECRET)") \
  $(if $(ALLOW_INSECURE_UPSTREAM_URLS),-e ALLOW_INSECURE_UPSTREAM_URLS="$(ALLOW_INSECURE_UPSTREAM_URLS)") \
  $(if $(UPSTREAM_ALLOW_PRIVATE_NETWORKS),-e UPSTREAM_ALLOW_PRIVATE_NETWORKS="$(UPSTREAM_ALLOW_PRIVATE_NETWORKS)") \
  $(if $(DEVICE_TOKEN_TTL_DAYS),-e DEVICE_TOKEN_TTL_DAYS="$(DEVICE_TOKEN_TTL_DAYS)") \
  $(if $(REDIS_URL),-e REDIS_URL="$(REDIS_URL)") \
  $(if $(UPSTREAM_BASE_URL),-e UPSTREAM_BASE_URL="$(UPSTREAM_BASE_URL)") \
  $(if $(UPSTREAM_API_KEY),-e UPSTREAM_API_KEY="$(UPSTREAM_API_KEY)") \
  $(if $(UPSTREAM_TIMEOUT_MS),-e UPSTREAM_TIMEOUT_MS="$(UPSTREAM_TIMEOUT_MS)") \
  $(if $(UPSTREAM_IDLE_TIMEOUT_MS),-e UPSTREAM_IDLE_TIMEOUT_MS="$(UPSTREAM_IDLE_TIMEOUT_MS)") \
  $(if $(GATEWAY_TRUSTED_PROXY_IPS),-e GATEWAY_TRUSTED_PROXY_IPS="$(GATEWAY_TRUSTED_PROXY_IPS)") \
  $(if $(PROVIDER_AUTO_DISABLE_THRESHOLD),-e PROVIDER_AUTO_DISABLE_THRESHOLD="$(PROVIDER_AUTO_DISABLE_THRESHOLD)")

DOCKER_BASE := docker run --rm -t \
  -v "$(CURDIR)/..":/repo -w /repo/server \
  -v mendao-go-mod-cache:/go/pkg/mod \
  -v mendao-go-build-cache:/root/.cache/go-build

DOCKER_RUN   := $(DOCKER_BASE) -e GOPROXY="$(GOPROXY)"
DOCKER_WORUN := $(DOCKER_BASE) -e GOPROXY="$(GOPROXY)" -e CGO_ENABLED=1

.PHONY: lint test vuln build run-api run-gateway migrate seed tidy sqlc clean

lint:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; } && go vet ./...'

test:
	$(DOCKER_WORUN) $(GO_IMAGE) go test -race ./...

# 依赖漏洞扫描：固定 govulncheck 版本，避免 CI 每次拉取 latest 漂移。
vuln:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c 'go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...'

build:
	$(DOCKER_RUN) -e CGO_ENABLED=0 $(GO_IMAGE) \
		go build -trimpath -ldflags "-s -w" -o bin/ ./cmd/api ./cmd/gateway ./cmd/migrate ./cmd/seed

# 本地开发:容器内 go run,端口映射回宿主(与 pnpm dev 配合)。
run-api:
	$(DOCKER_RUN) $(DEV_ENV) -e PORT="$(PORT)" -p "$(PORT):$(PORT)" $(GO_IMAGE) go run ./cmd/api

run-gateway:
	$(DOCKER_RUN) $(DEV_ENV) -e PORT="$(PORT)" -e GATEWAY_PORT="$(GATEWAY_PORT)" \
		-p "$(GATEWAY_PORT):$(GATEWAY_PORT)" $(GO_IMAGE) go run ./cmd/gateway

migrate:
	$(DOCKER_RUN) $(DEV_ENV) $(GO_IMAGE) go run ./cmd/migrate -dir migrations

seed:
	$(DOCKER_RUN) $(DEV_ENV) $(GO_IMAGE) go run ./cmd/seed -dir migrations -seed-file seeds/shucode-builtin.seed.json

tidy:
	$(DOCKER_RUN) $(GO_IMAGE) go mod tidy

sqlc:
	docker run --rm -t -v "$(CURDIR)/..":/repo -w /repo/server $(SQLC_IMAGE) generate

clean:
	rm -rf bin
	docker volume rm mendao-go-build-cache mendao-go-mod-cache 2>/dev/null || true
