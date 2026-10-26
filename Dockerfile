# 运行时镜像（console 产物并入 api 镜像，由 API 以 /console 托管）。
#   api / gateway：Go 静态二进制（server/cmd/*）
#   tools：migrate / seed 一次性任务
# console 仍由 Node 构建(前端仓库未变),后端构建与运行均为 Go。

FROM node:24-alpine@sha256:44fce696f38ba8c987c4db89954051000bbd05ffd8d8659ba15ed4a8602e937f AS base
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
WORKDIR /app
RUN corepack enable && chown node:node /app
USER node
COPY --chown=node:node pnpm-workspace.yaml package.json pnpm-lock.yaml ./
COPY --chown=node:node apps/console/package.json apps/console/package.json
COPY --chown=node:node packages/contracts/package.json packages/contracts/package.json
RUN pnpm install --frozen-lockfile

# 单独构建控制台，产物（apps/console/dist）并入 api 镜像。
# 版本信息由 CI 传入；本地构建未传时控制台回退到生效的 Release revision。
FROM base AS console-build
ARG APP_VERSION=""
ARG APP_SUBJECT=""
ENV VITE_APP_VERSION=$APP_VERSION
ENV VITE_APP_SUBJECT=$APP_SUBJECT
COPY --chown=node:node . .
RUN pnpm --filter @shucode-platform/console build

# Go 后端：静态编译四个二进制（linux/amd64，生产镜像无 CGO 依赖）。
FROM golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS go-builder
WORKDIR /src
ENV CGO_ENABLED=0
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN go build -trimpath -ldflags "-s -w" -o /out/api ./cmd/api \
 && go build -trimpath -ldflags "-s -w" -o /out/gateway ./cmd/gateway \
 && go build -trimpath -ldflags "-s -w" -o /out/migrate ./cmd/migrate \
 && go build -trimpath -ldflags "-s -w" -o /out/seed ./cmd/seed

FROM alpine:3.21@sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507 AS runtime
RUN apk add --no-cache ca-certificates curl ffmpeg font-noto-cjk \
 && addgroup -S app \
 && adduser -S -G app app \
 && mkdir -p /app/data/pending-settlements /app/data/audit-outbox /app/data/media \
 && chown -R app:app /app/data
WORKDIR /app
# 运行时一律非 root；应用只读代码与迁移文件，临时文件由 compose 的 tmpfs 提供。
USER app

FROM runtime AS api
COPY --from=go-builder /out/api /app/api
COPY --from=console-build /app/apps/console/dist /app/apps/console/dist
COPY server/migrations /app/migrations
COPY server/seeds /app/seeds
ENV CONSOLE_DIST_DIR=/app/apps/console/dist
EXPOSE 8787
CMD ["/app/api"]

FROM runtime AS gateway
COPY --from=go-builder /out/gateway /app/gateway
EXPOSE 8788
CMD ["/app/gateway"]

FROM runtime AS tools
COPY --from=go-builder /out/migrate /app/migrate
COPY --from=go-builder /out/seed /app/seed
COPY server/migrations /app/migrations
COPY server/seeds /app/seeds
