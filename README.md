# shucode-platform（Mendao 平台）

Shucode 客户端的多租户 SaaS 平台：平台账号登录、管理员控制台、模型目录与**团队共享额度**（credit）、内置配置（Built-in Release）发布、模型网关。后端为 Go（Go 1.26 + Gin + pgx），控制台为 React + Vite。

客户端接入方案与验收场景见 [`specs/multi-tenant-saas.md`](specs/multi-tenant-saas.md)（从 Shucode 客户端仓库 `specs/platform/multi-tenant-saas.md` 镜像，权威源与同步规则见文件头）。

## 组成

| 目录 | 说明 |
| --- | --- |
| `server/cmd/api` | 控制面：匿名客户端配置、平台设备登录流、用户级配置下发、管理控制台 API 与 `/console` 静态托管、SSE 配置事件 |
| `server/cmd/gateway` | 模型网关：平台凭据鉴权 → 团队 RPM/TPM/并发限流 → 额度预留（幂等）→ 渠道池调度/重试/故障转移 → 透传上游（Anthropic messages / OpenAI chat / OpenAI responses）→ 按 usage 计量结算 |
| `server/internal/store` | 存储端口 + pgx(Postgres) 与内存双实现（`conformance` 测试保证同语义） |
| `server/internal/{crypto,contracts,pricing,credits}` | 与 TS 契约逐位对齐的凭据/校验/计价镜像层（fixtures 对齐测试） |
| `server/internal/{ratelimit,events}` | 登录限流与配置事件总线（进程内 / Redis 双实现） |
| `server/migrations`、`server/seeds` | 数据库迁移与种子内置配置 |
| `apps/console` | 管理控制台（Vite + React）：「计量台」视觉语言，见 `design/design.md` |
| `packages/contracts` | console 消费的信用点换算（`credits.ts`）；跨端 schema 的可执行镜像在 `server/internal/contracts` |

```text
终端用户客户端                          平台
欢迎页「平台账号登录」 ──浏览器──→  api /app/platform/login（登录+授权页）
   ↑ mendao://platform/callback       │
   轮询领取设备凭据（mdp_*）  ←─────────┘（consumeLoginFlow 条件消费，只签发一次）
周期拉取 user-config（≈1h）←────────  /api/v1/client/user-config（Bearer 设备凭据）
模型请求（gateway provider）──→ gw /v1/chat/completions ──→ 上游供应商
匿名拉取内置配置（≈1h）    ←────────  /api/v1/client/configs + Release JSON（CDN/对象存储）
```

## 可靠性与运维

- **渠道池**：控制台「模型目录 → 路由池」可为同一模型绑定多个供应商；网关按 `priority` 升序、同优先级按 `weight` 加权调度，流式开始前对网络错误/超时/429/5xx/401/403 自动重试下一个上游。自动熔断的供应商会被定时拨测并自动恢复，也可在「上游供应商」手动恢复调度。
- **流控**：团队详情可配置 RPM / TPM / 并发上限；Redis 存在时跨实例共享，否则进程内回退。TPM 使用请求体估算 token（正文长度 / 4 + `max_tokens`）做准入保护。
- **计费一致性**：每次请求先写 `quota_reservations`（`request_id` 唯一），结算时同事务扣回预留、写 usage 与小时汇总；超时预留由 sweeper 回收。重复 `x-request-id` 返回 409。
- **运维大盘**：控制台「运维大盘」展示请求/成功率/P50/P95/P99/消费/活跃设备/未释放预留/供应商健康，支持告警规则与事件；`ALERT_WEBHOOK_URL` 可推送外部通知。
- **用量**：支持 request ID 精确检索、CSV 导出与小时 rollup；全平台趋势优先读 `usage_hourly_rollups`，明细维度回退 `usage_records`。
- **图表**：控制台使用 Recharts（费用趋势、缓存率趋势、模型分布、运维流量/消费），均带图例与 hover tooltip。
- **人民币定价**：成本价库与模型售价均按 CNY 展示（1 credit = ¥1）；DeepSeek 使用官方人民币高峰价，空闲价 = 高峰价 × `offPeakMultiplier`（默认 0.5）。高峰窗口为北京时间周一至周五 09:00-12:00、14:00-18:00，法定节假日通过 `PEAK_HOURS_HOLIDAYS` 补充。

相关环境变量（均可留空使用默认值）：`GATEWAY_MAX_ATTEMPTS`、`GATEWAY_RESERVATION_TTL_SEC`、`PROVIDER_PROBE_INTERVAL_SEC`、`PEAK_HOURS_HOLIDAYS`、`ALERT_WEBHOOK_URL`；详见 `.env.example`。

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
3. 应用迁移（`make -C server migrate`）、载入种子（内置配置 + 默认模型目录）
4. 启动 api / gateway / console。api 与 gateway 在 `golang:1.26` 容器内 `go run`（端口映射回宿主，DSN 自动改写 `host.docker.internal`）；console 由 vite 在宿主机跑。首次编译约数十秒，命中构建缓存卷后数秒

结束时打印访问地址：控制台 `http://localhost:5177/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788`。
首次打开控制台会进入超级管理员初始化页：root 全库唯一，账号与口令只以 scrypt 哈希写入数据库，不读 `.env`；存量部署升级时最早创建的可用管理员会自动提升为 root（沿用原密码）。

不用数据库的体验模式（进程内存储，内置演示数据）：

```bash
pnpm dev:memory       # 跳过 Postgres/迁移/种子
```

演示账号：`admin@mendao.chat / mendao-admin`（控制台，超级管理员 root）、`member@mendao.chat / mendao-member`（普通用户 user）。演示团队额度 1000 credit。

单独调试：`pnpm dev:api` / `pnpm dev:gateway` / `pnpm dev:console`；手工执行各步骤：

```bash
docker compose up -d postgres redis
cp .env.example .env            # 填 DATABASE_URL、PLATFORM_SECRET_KEY 等
pnpm migrate                    # 应用 server/migrations/*.sql
pnpm seed                       # 内置配置 + 默认模型目录（root 由控制台初始化）
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
  → seed（一次性，内置配置 + 默认模型目录；root 由控制台首次初始化）
  → api + gateway（常驻，unless-stopped）
```

镜像由根目录 `Dockerfile` 多阶段构建：Node 构建 console 产物 → Go 静态编译 api/gateway/migrate/seed → alpine 运行时（含 ca-certificates/curl）。console 产物并入 api 镜像以 `/console` 托管；compose 为 api 注入 `REDIS_URL`（共享限流与配置事件总线）。

首次部署请在对外放行前打开控制台完成 root 初始化。新部署必须先在 `.env` 设置 `BOOTSTRAP_SECRET`，初始化请求需携带同值 `x-bootstrap-secret` 头；初始化成功后删除该变量并重启 api。初始化接口在 root 存在后自动关闭。

| 项 | 说明 |
| --- | --- |
| 访问地址 | 控制台 `http://localhost:8787/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788` |
| 服务器部署 | 使用 `docker-compose.yml` + `docker-compose.prod.yml`，api/gateway 固定只绑 `127.0.0.1`，由宿主机 Nginx 提供 HTTPS；首次发布前先验证 Nginx 入口。公开地址、证书与反代步骤见 `deploy/nginx/README.md`，公网只放行 SSH / 80 / 443 |
| 宿主机端口 | `API_HOST_PORT` / `GATEWAY_HOST_PORT` 可改映射端口，需同步 Nginx 回源；`POSTGRES_HOST_PORT` 仅基础编排使用，生产固定 `127.0.0.1:5432` |
| 只起数据库 | `docker compose up -d postgres redis`；整条 `docker compose up -d` 会按依赖先跑迁移与种子 |
| 与本地 dev 并存 | 两者默认都占 8787 / 8788，不要同时用；需要并存时给 stack 改宿主端口映射 |
| 停止 | `pnpm docker:down`（停 api / gateway / redis 与一次性容器，保留 Postgres 数据卷） |
| 日志 | `pnpm docker:logs`；`docker compose ps -a` 查看健康状态（migrate / seed 显示已退出属正常） |

种子说明：seed 只负责内置配置与模型目录（幂等），不再创建管理员账号；root 首次初始化在控制台完成，账号口令只以哈希入库，`.env` 中不再有 root 凭据。控制台向所有账号开放：`root` 管理平台；团队 `owner/admin` 管理自己的团队，权限来自成员关系，全局 `admin` 标记不授予平台权限；普通成员查看个人用量、设备与资料。定价、参考成本、供应商和全局配置仅 root 可见。完整矩阵与升级规则见 [`specs/tenant-permissions.md`](specs/tenant-permissions.md)。

用量中的客户端 IP 默认只取 TCP 对端，忽略所有转发头；端口直连无需额外配置。使用可信反代时，才在 `.env` 设置 `GATEWAY_TRUSTED_PROXY_IPS` 为网关实际看到的代理精确 IP（逗号分隔，支持 IPv4/IPv6，不支持 CIDR/通配符）。仅这些对端提供的 `X-Forwarded-For` 会按链从右向左解析，遇到首个非可信地址即停止；没有 XFF 时才接受经校验的 `X-Real-IP`。反代须覆盖转发头或在 XFF 追加实际对端地址，并限制其入口访问；不要仅因地址属于私网或 Docker 网桥就信任它。

旧的手工部署方式仍可用：`pnpm build`（console 产物在 `apps/console/dist`），设置 `CONSOLE_DIST_DIR` 由 API 以 `/console` 路径托管；或托管到独立域名并设置 `PUBLIC_CONSOLE_ORIGIN`（已内置带凭据 CORS）。

使用宿主机 Nginx 与 Cloudflare DNS 时，域名、证书签发、反代配置、真实 IP 与验收命令见 [`deploy/nginx/README.md`](deploy/nginx/README.md)。

### 生产安全基线

- **HTTPS 是硬前提**：公网 HTTP 会暴露口令、会话与录入的上游 key。生产编排将 api/gateway 固定绑定到 `127.0.0.1`，宿主机 Nginx 负责 TLS，避免公网 8787/8788 绕过 HTTPS。HSTS 为 `max-age=31536000; includeSubDomains`（preload 需另行评估）。边缘不要对 `/console/api/*`、SSE 或网关响应启用 Cache Everything；Nginx 示例已关闭代理缓冲与缓存，支持流式响应。
- **管理与公开入口分离**：设置 `CONSOLE_ALLOWED_HOSTS` 后，`/console` 只接受指定 Host；配合 Cloudflare Access（或 VPN）保护管理域名，公开 API 域名无法绕入。首次初始化接口另由 `SETUP_ALLOWED_HOSTS` 限制到专用初始化 Host（该 Host 也要加入 `CONSOLE_ALLOWED_HOSTS`），避免公网探测 `setup/status`/`setup/root`。
- **登录人机验证**：同时配置 `TURNSTILE_SITE_KEY` 与 `TURNSTILE_SECRET_KEY` 后，控制台登录页和设备授权登录页会渲染 Cloudflare Turnstile；服务端在密码校验前调用 siteverify 并校验 `action=login`，校验服务不可用时 fail-closed。site key 经 `/console/api/public-config` 下发，secret 只留在服务端；两项必须同时配置，留空则保持原登录行为。Turnstile 站点域名需覆盖控制台域名。
- **登录防爆破分层**：登录同时检查 Pure-IP 全局桶（100 次/24h）、IP+归一化邮箱桶（10 次/5min，去 `+tag`、Gmail 点号归一）、root 专属桶（5 次/h）；限流键在 Redis/内存中先 SHA-256 收敛。未知邮箱与禁用账号仍执行 scrypt（dummy/真实哈希）避免时序枚举；登录/初始化 JSON 上限 64 KiB，邮箱字段 ≤254 字符。
- **会话**：普通账号会话 7 天，root/admin 会话 24 小时；密码或 MFA 变更会撤销旧会话。
- **可信代理解析**：api 与 gateway 都只在 TCP 对端命中 `API_TRUSTED_PROXY_IPS` / `GATEWAY_TRUSTED_PROXY_IPS` 精确地址时才读取转发头；反代须覆盖或清洗转发头。宿主反代访问 Docker 发布的回环端口后，容器可能看到网桥 gateway，必须填写实测的精确对端；只有实际对端为 `127.0.0.1` 时才填该值。`CF-Connecting-IP` 默认不信任（普通反代不会清洗它），只有源站只能经 Cloudflare 访问时才设 `TRUST_CF_CONNECTING_IP=1`。
- **上游密钥边界**：供应商地址强制 https、拒绝 userinfo/查询串，管理端与网关都不跟随重定向；默认禁止出站访问回环、私网与云元数据地址（自建内网上游需显式设 `UPSTREAM_ALLOW_PRIVATE_NETWORKS=1`）。新建供应商与修改地址/密钥仅 root 可操作。
- **计量**：OpenAI 流式请求由网关强制 `include_usage=true`；上游成功但缺失 usage 时按准入预留额保守结算并标记 `usage_missing_estimated`，便于对账。请求幂等键按 `(team_id, request_id)` 隔离。
- **请求边界**：控制台 JSON 1 MiB、登录/初始化 JSON 与登录表单 64 KiB、网关请求 32 MiB，上游非流式响应 32 MiB；SSE 与长响应不设写超时。管理面登录/初始化与设备授权页的密码验证在限流后端故障时 fail-closed，匿名设备登录流初始化沿用原可用性策略。
- **日志**：不再使用 Gin 默认 recovery（避免 broken-pipe 时 dump Cookie/x-api-key）；上游错误正文返回前会脱敏当前供应商密钥。

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
- **上游 Base URL**：可填服务根地址（自动追加 `/v1`）或以 `/v数字` 结尾的 API 前缀（直接追加端点）；例如火山引擎的 `/api/v3` 会请求 `/api/v3/chat/completions` 或 `/api/v3/responses`。供应商测试、模型列表、模型探测与网关转发共用该规则，已有配置无需迁移。
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

- 单团队：新增成员关系拒绝跨团队重复加入；历史多有效团队返回 409，需 root 修复归属，不再自动选第一个。无有效成员关系或团队停用返回 403。
- 多上游为「同构透传」：模型绑定供应商（密钥加密存储），未绑定回落 `UPSTREAM_BASE_URL`/`UPSTREAM_API_KEY`；跨协议翻译是后续项。
- 额度申请由团队管理员提交、root 人工批准或拒绝；批准与额度增加同事务且仅执行一次。尚未接入在线支付、订阅或发票。参考成本采用请求时冻结的估算快照，旧记录不回填为零。
- 登录限流与配置事件总线：设置 `REDIS_URL`（compose 整栈默认）时为多实例共享；未设置时退回进程内实现（单实例语义）。
