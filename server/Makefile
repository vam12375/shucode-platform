# 全部走 Docker 工具链:服务器部署与 CI 构建以容器为准,本机无需安装 Go。
# CI(check job)用 setup-go 跑同口径校验;生产镜像由 Dockerfile 的 go-builder 阶段构建。

GO_IMAGE     ?= golang:1.26
SQLC_IMAGE   ?= sqlc/sqlc:latest   # M1 接入 codegen 时固定具体版本
# 本地网络 proxy.golang.org 不可达,默认走 goproxy.cn(公网 Go 模块镜像);
# 哈希由 go.sum 锁定、CI 以官方源二次校验,供应链不受影响。CI runner 可覆盖为官方直连。
GOPROXY      ?= https://goproxy.cn,https://proxy.golang.org,direct
DOCKER_RUN   := docker run --rm -t -e GOPROXY="$(GOPROXY)" \
                  -v "$(CURDIR)/..":/repo -w /repo/server \
                  -v mendao-go-mod-cache:/go/pkg/mod \
                  -v mendao-go-build-cache:/root/.cache/go-build
DOCKER_WORUN := docker run --rm -t -e GOPROXY="$(GOPROXY)" -e CGO_ENABLED=1 \
                  -v "$(CURDIR)/..":/repo -w /repo/server \
                  -v mendao-go-mod-cache:/go/pkg/mod \
                  -v mendao-go-build-cache:/root/.cache/go-build

.PHONY: lint test build run-api run-gateway tidy sqlc clean

lint:
	$(DOCKER_RUN) $(GO_IMAGE) sh -c 'test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; } && go vet ./...'

test:
	$(DOCKER_WORUN) $(GO_IMAGE) go test -race ./...

build:
	$(DOCKER_RUN) -e CGO_ENABLED=0 $(GO_IMAGE) \
		go build -trimpath -ldflags "-s -w" -o bin/ ./cmd/api ./cmd/gateway ./cmd/migrate ./cmd/seed

run-api:
	$(DOCKER_WORUN) $(GO_IMAGE) go run ./cmd/api

run-gateway:
	$(DOCKER_WORUN) $(GO_IMAGE) go run ./cmd/gateway

tidy:
	$(DOCKER_RUN) $(GO_IMAGE) go mod tidy

sqlc:
	docker run --rm -t -v "$(CURDIR)/..":/repo -w /repo/server $(SQLC_IMAGE) generate

clean:
	rm -rf bin
	docker volume rm mendao-go-build-cache mendao-go-mod-cache 2>/dev/null || true
