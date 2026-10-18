# shucode-platform（Mendao 平台）

Shucode 客户端的多租户 SaaS 平台：平台账号登录、管理员控制台、模型目录与**团队共享额度**（credit）、内置配置（Built-in Release）发布、模型网关。后端为 Go（Go 1.26 + Gin + pgx），控制台为 React + Vite。

客户端接入方案与验收场景见 [`specs/multi-tenant-saas.md`](specs/multi-tenant-saas.md)（从 Shucode 客户端仓库 `specs/platform/multi-tenant-saas.md` 镜像，权威源与同步规则见文件头）。

## 组成

| 目录 | 说明 |
| --- | --- |
| `server/cmd/api` | 控制面：匿名客户端配置、平台设备登录流、用户级配置下发、管理控制台 API 与 `/console` 静态托管、SSE 配置事件 |
| `server/cmd/gateway` | 模型网关：平台凭据鉴权 → 团队额度准入（预留）→ 透传上游（Anthropic messages / OpenAI chat / OpenAI responses）→ 按 usage 计量结算 |
| `server/internal/store` | 存储端口 + pgx(Postgres) 与内存双实现（`conformance` 测试保证同语义） |
| `server/internal/{crypto,contracts,pricing,credits}` | 与 TS 契约逐位对齐的凭据/校验/计价镜像层（fixtures 对齐测试） |
| `server/internal/{ratelimit,events}` | 登录限流与配置事件总线（进程内 / Redis 双实现） |
| `server/migrations`、`server/seeds` | 数据库迁移与种子内置配置 |
| `apps/console` | 管理控制台（Vite + React）：「计量台」视觉语言，见 `design/design.md` |
| `packages/contracts` | console 消费的信用点换算（`credits.ts`）；跨端 schema 的可执行镜像在 `server/internal/contracts` |

```text
终端用户客户端                          平台
欢迎页「平台账号登录」 ──浏览器──→  api /app/platform/login（登录+授权页）
   ↑ zcode://platform/callback        │
   轮询领取设备凭据（mdp_*）  ←─────────┘（consumeLoginFlow 条件消费，只签发一次）
周期拉取 user-config（≈1h）←────────  /api/v1/client/user-config（Bearer 设备凭据）
模型请求（gateway provider）──→ gw /v1/chat/completions ──→ 上游供应商
匿名拉取内置配置（≈1h）    ←────────  /api/v1/client/configs + Release JSON（CDN/对象存储）
```

## 本地启动

工具链全部由 Docker 提供（Postgres、Redis、Go 编译/运行），本机只需 Node 24 + pnpm 10 与 Docker Desktop：

```bash
mise install          # Node 24 / pnpm 10
pnpm install
pnpm dev              # 一条命令：Postgres/Redis → 迁移 → 种子 → api + gateway + console
```

`pnpm dev` 按顺序执行，全部可重复（已就绪的步骤自动跳过）：

1. 读取根目录 `.env`；缺失时从 `.env.example` 生成并填入随机 `PLATFORM_SECRET_KEY`
2. `DATABASE_URL` 指向本机且端口未监听时 `docker compose up -d postgres redis`
3. 应用迁移（`make -C server migrate`）、载入种子（管理员 + 内置配置 + 默认模型目录）
4. 启动 api / gateway / console。api 与 gateway 在 `golang:1.26` 容器内 `go run`（端口映射回宿主，DSN 自动改写 `host.docker.internal`）；console 由 vite 在宿主机跑。首次编译约数十秒，命中构建缓存卷后数秒

结束时打印访问地址：控制台 `http://localhost:5177/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788`。
管理员默认 `admin@mendao.chat / mendao-admin`（本地默认口令，可在 `.env` 设置 `SEED_ADMIN_PASSWORD` 覆盖；已有管理员时不会重置）。

不用数据库的体验模式（进程内存储，内置演示数据）：

```bash
pnpm dev:memory       # 跳过 Postgres/迁移/种子
```

演示账号：`admin@mendao.chat / mendao-admin`（控制台，管理员）、`member@mendao.chat / mendao-member`（成员）。演示团队额度 1000 credit。

单独调试：`pnpm dev:api` / `pnpm dev:gateway` / `pnpm dev:console`；手工执行各步骤：

```bash
docker compose up -d postgres redis
cp .env.example .env            # 填 DATABASE_URL、PLATFORM_SECRET_KEY 等
pnpm migrate                    # 应用 server/migrations/*.sql
SEED_ADMIN_PASSWORD=... pnpm seed
pnpm dev:api
pnpm dev:gateway
pnpm dev:console
```

## Docker 一键启动（与服务器部署同一套编排）

```bash
cp .env.example .env    # 至少填 PLATFORM_SECRET_KEY；正式部署再填域名与上游
pnpm docker:up          # = docker compose up -d --build
```

```text
postgres + redis（healthcheck 就绪）
  → migrate（一次性，应用 server/migrations/*.sql）
  → seed（一次性，管理员 + 内置配置 + 默认模型目录）
  → api + gateway（常驻，unless-stopped）
```

镜像由根目录 `Dockerfile` 多阶段构建：Node 构建 console 产物 → Go 静态编译 api/gateway/migrate/seed → alpine 运行时（含 ca-certificates/curl）。console 产物并入 api 镜像以 `/console` 托管；compose 为 api 注入 `REDIS_URL`（共享限流与配置事件总线）。

| 项 | 说明 |
| --- | --- |
| 访问地址 | 控制台 `http://localhost:8787/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788` |
| 服务器部署 | 把 `.env` 的 `PUBLIC_API_ORIGIN` / `PUBLIC_APP_ORIGIN` / `GATEWAY_BASE_URL` / `COOKIE_DOMAIN` / `UPSTREAM_API_KEY` 换成正式值，服务器上执行同一条 `pnpm docker:up`；反向代理（HTTPS）指向 8787 / 8788 |
| 宿主机端口 | `API_HOST_PORT` / `GATEWAY_HOST_PORT` / `POSTGRES_HOST_PORT` 可改映射端口 |
| 只起数据库 | `docker compose up -d postgres redis`；整条 `docker compose up -d` 会按依赖先跑迁移与种子 |
| 与本地 dev 并存 | 两者默认都占 8787 / 8788，不要同时用；需要并存时给 stack 改宿主端口映射 |
| 停止 | `pnpm docker:down`（停 api / gateway / redis 与一次性容器，保留 Postgres 数据卷） |
| 日志 | `pnpm docker:logs`；`docker compose ps -a` 查看健康状态（migrate / seed 显示已退出属正常） |

种子说明：`SEED_ADMIN_PASSWORD` 为空且管理员不存在时只打印提示（不创建管理员），内置配置与模型目录仍会载入并保持幂等。

用量中的客户端 IP 默认只取 TCP 对端，忽略所有转发头；端口直连无需额外配置。使用可信反代时，才在 `.env` 设置 `GATEWAY_TRUSTED_PROXY_IPS` 为网关实际看到的代理精确 IP（逗号分隔，支持 IPv4/IPv6，不支持 CIDR/通配符）。仅这些对端提供的 `X-Forwarded-For` 会按链从右向左解析，遇到首个非可信地址即停止；没有 XFF 时才接受经校验的 `X-Real-IP`。反代须覆盖转发头或在 XFF 追加实际对端地址，并限制其入口访问；不要仅因地址属于私网或 Docker 网桥就信任它。

旧的手工部署方式仍可用：`pnpm build`（console 产物在 `apps/console/dist`），设置 `CONSOLE_DIST_DIR` 由 API 以 `/console` 路径托管；或托管到独立域名并设置 `PUBLIC_CONSOLE_ORIGIN`（已内置带凭据 CORS）。

## 域名与对客端

| 域名 | 用途 |
| --- | --- |
| `api.mendao.chat` | 控制面；同时默认托管登录页（`PUBLIC_APP_ORIGIN`）与控制台（`/console`） |
| `app.mendao.chat` | 可选：拆分登录页时指向 API 并设置 `PUBLIC_APP_ORIGIN`（cookie 设置 `COOKIE_DOMAIN=.mendao.chat`） |
| `gw.mendao.chat` | 模型网关；地址写入用户级下发配置（`GATEWAY_BASE_URL`） |
| `console.mendao.chat` | 可选：独立控制台 |
| `cdn.mendao.chat` | 可选：Release JSON 与安装包；`RELEASE_BASE_URL` 必须 https 且**不能有重定向**（客户端下载边界 `redirect: "error"`） |

客户端侧只需在构建/发布时注入环境变量：

```bash
SHUCODE_BASE_URL=https://api.mendao.chat     # 桌面/Web 控制面
VITE_SHUCODE_BASE_URL=https://api.mendao.chat # Web 端
```

## 关键契约（不可轻改）

- **用户级配置信封**与客户端 `ProviderProvisioningEnvelope` 同构；权威源在 Shucode 客户端仓库，Go 侧镜像在 `server/internal/contracts`，由冻结 fixtures 对齐测试保证。
- **网关设备 token 必须写入 provider 规则的 `config.access.apiKey`**：客户端 Registry 完整校验要求 Provider access 完整，缺 apiKey 时 Provider 会被整份丢弃（provider-not-found → 信封回滚）。
- **偏好默认模型**：`defaultModelSelection` 只作"偏好"，客户端应用前会剥离并由 Registry 档位事实补全。
- **下发协议默认 OpenAI chat**（`GATEWAY_API_TYPE`，baseUrl 带 `/v1` 前缀）：网关三条协议路由（chat / messages / responses）共享鉴权与额度管道；**模型目录中的模型名必须与上游真实模型名一致**，或用控制台的 `upstreamModel` 字段映射。
- **额度执行只在网关**：准入按 `max_tokens × 输出单价` 原子预留，结束后按上游 usage 结算（预留转实扣与账本写入同一事务）；上游失败/客户端中断释放预留并记录 error/aborted。
- **设备凭据只签发一次**（`consumeLoginFlow` 条件更新）；token 以 sha256 哈希索引 + AES-256-GCM 密文保存，吊销即时生效。
- **Release 发布**：自动分配下一个 revision 并设为生效；服务端只做结构粗检，最终校验在客户端。
- **公开配置信封**字段对齐客户端各解析器：`configs.feedbackUrl`、`configs.builtin_provider_config_json`（仅 https 下发）、`configs.forceUpdate.minimalVersion`、`configs.pluginStoreOrder`。

## 桌面更新（未完成）

`/api/v1/releases/electron/manifest` 目前固定返回 `version: 0.0.0`（等价于“无更新”）。接入自有安装包分发前，**不要在控制台填写强制更新版本号**。

## 测试

```bash
pnpm typecheck && pnpm test       # console + contracts
make -C server lint test          # gofmt + vet + go test -race(容器内)
```

Go 的 Postgres/Redis 套件需要显式环境变量才启用（默认跳过，CI/local 可按需注入）：

```bash
docker run --rm -e TEST_DATABASE_URL=postgres://mendao:mendao@host.docker.internal:5432/mendao \
  -e TEST_REDIS_URL=redis://host.docker.internal:6379/0 -e TEST_MIGRATIONS_DIR=/repo/server/migrations \
  -v "$PWD":/repo -w /repo/server golang:1.26 go test -race ./...
```

## 已知边界（MVP 内）

- 单团队：用户取第一个未禁用团队作为额度归属；多团队与切换后续评估。
- 多上游为「同构透传」：模型绑定供应商（密钥加密存储），未绑定回落 `UPSTREAM_BASE_URL`/`UPSTREAM_API_KEY`；跨协议翻译是后续项。
- 未托管额度充值流程：额度调整在控制台人工操作；计费口径为参考 credit，非发票账单。
- 登录限流与配置事件总线：设置 `REDIS_URL`（compose 整栈默认）时为多实例共享；未设置时退回进程内实现（单实例语义）。
