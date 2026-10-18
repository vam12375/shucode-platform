# api / gateway 运行时镜像（console 产物并入 api 镜像，由 API 以 /console 托管）。
#   api / gateway：常驻服务（tsx 直跑 TypeScript，与本地 dev 一致）
#   tools：migrate / seed 等一次性任务
# 构建与编排见 docker-compose.yml（--profile stack），本地热重载开发仍用 pnpm dev。

FROM node:24-alpine AS base
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
WORKDIR /app
RUN corepack enable && chown node:node /app
USER node
COPY --chown=node:node pnpm-workspace.yaml package.json pnpm-lock.yaml ./
COPY --chown=node:node apps/api/package.json apps/api/package.json
COPY --chown=node:node apps/gateway/package.json apps/gateway/package.json
COPY --chown=node:node apps/console/package.json apps/console/package.json
COPY --chown=node:node packages/contracts/package.json packages/contracts/package.json
COPY --chown=node:node packages/db/package.json packages/db/package.json
RUN pnpm install --frozen-lockfile

# 单独构建控制台，产物（apps/console/dist）并入 api 镜像。
FROM base AS console-build
COPY --chown=node:node . .
RUN pnpm --filter @shucode-platform/console build

FROM base AS tools
COPY --chown=node:node . .

FROM base AS api
COPY --chown=node:node . .
COPY --from=console-build --chown=node:node /app/apps/console/dist apps/console/dist
ENV CONSOLE_DIST_DIR=/app/apps/console/dist
EXPOSE 8787
CMD ["pnpm", "--filter", "@shucode-platform/api", "start"]

FROM base AS gateway
COPY --chown=node:node . .
EXPOSE 8788
CMD ["pnpm", "--filter", "@shucode-platform/gateway", "start"]

# Go 后端工具链构建阶段(M4 切换时由 api/gateway/tools target 改为引用这里):
# 静态二进制,与服务器/CI 的部署口径一致。M0 与 M1 过渡期仅预验证编译,尚无人引用。
FROM golang:1.26-alpine AS go-builder
WORKDIR /src
ENV CGO_ENABLED=0
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN go build -trimpath -ldflags "-s -w" -o /out/ ./cmd/api ./cmd/gateway ./cmd/migrate ./cmd/seed
