# shucode-platform（Mendao 平台）

Shucode 客户端的多租户 SaaS 平台：平台账号登录、管理员控制台、模型目录与**团队共享额度**（credit）、内置配置（Built-in Release）发布、模型网关。

客户端接入方案与验收场景见 [`specs/multi-tenant-saas.md`](specs/multi-tenant-saas.md)（从 Shucode 客户端仓库 `specs/platform/multi-tenant-saas.md` 镜像，权威源与同步规则见文件头）。域名暂定 `mendao.chat`。

## 组成

| 目录 | 说明 |
| --- | --- |
| `apps/api` | 控制面（Hono）：匿名客户端配置、平台设备登录流、用户级配置下发、管理控制台 API 与静态托管 |
| `apps/gateway` | 模型网关：平台凭据鉴权 → 团队额度准入（预留）→ 透传上游（Anthropic 与 OpenAI chat 双协议）→ 按 usage 计量结算 |
| `apps/console` | 管理控制台（Vite + React）：团队/成员/额度、用户管理（创建/禁用/重置密码）、用量统计、Release 发布、模型目录与价格、设备凭据、平台配置。视觉语言为「计量台」（顶部读数条 + 指针刻度尺额度 + 琥珀信号，纯 CSS，见 `design/design.md`） |
| `packages/contracts` | 与 Shucode 客户端对齐的契约（Provisioning 信封、公开配置信封、登录协议、信用点计算），从客户端仓库拷贝同步 |
| `packages/db` | 存储端口（Ports）+ Postgres 实现（postgres.js）+ 内存实现（本地体验/测试）+ 迁移 |

```text
终端用户客户端                          平台
欢迎页「平台账号登录」 ──浏览器──→  api.mendao.chat /app/platform/login（登录+授权页）
   ↑ zcode://platform/callback        │
   轮询领取设备凭据（mdp_*）  ←─────────┘（consumeLoginFlow 条件消费，只签发一次）
周期拉取 user-config（≈1h）←────────  /api/v1/client/user-config（Bearer 设备凭据）
模型请求（gateway provider）──→ gw.mendao.chat /v1/messages ──→ 上游供应商
匿名拉取内置配置（≈1h）    ←────────  /api/v1/client/configs + Release JSON（CDN/对象存储）
```

## 本地启动

```bash
mise install          # Node 24 / pnpm 10
pnpm install
pnpm dev              # 一条命令：Postgres → 迁移 → 种子 → api + gateway + console
```

`pnpm dev` 按顺序做四件事，全部可重复执行（已就绪的步骤自动跳过）：

1. 读取根目录 `.env`；缺失时从 `.env.example` 生成并填入随机 `PLATFORM_SECRET_KEY`（已有 .env 不会被覆盖）
2. `DATABASE_URL` 指向本机且 5432 未监听时执行 `docker compose up -d postgres`，并等待 Postgres 就绪
3. 应用迁移（`pnpm migrate`）、载入种子（管理员 + 内置配置 + 默认模型目录）
4. 启动 api / gateway / console（带 `api |`、`gateway |`、`console |` 前缀输出），Ctrl-C 停止全部

结束时打印访问地址：控制台 `http://localhost:5177/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788`。
管理员默认 `admin@mendao.chat / mendao-admin`（本地默认口令，可在 `.env` 设置 `SEED_ADMIN_PASSWORD` 覆盖；已有管理员时不会重置）。

不用数据库的体验模式（进程内存储，内置演示数据）：

```bash
pnpm dev:memory       # 跳过 Postgres/迁移/种子
```

演示账号：`admin@mendao.chat / mendao-admin`（控制台，管理员）、`member@mendao.chat / mendao-member`（成员）。演示团队额度 1000 credit。

内存模式未配置 `PLATFORM_SECRET_KEY` 时会生成临时密钥（重启后旧设备 token 无法恢复，仅限体验）。

需要单独调试某个服务时仍可用 `pnpm dev:api`、`pnpm dev:gateway`、`pnpm dev:console`；手工执行各步骤：

```bash
docker compose up -d postgres   # 本机 Postgres 17（整套容器则用 docker compose up -d，见下节）
cp .env.example .env            # 填 DATABASE_URL、PLATFORM_SECRET_KEY 等
pnpm migrate                    # 应用 migrations/*.sql
SEED_ADMIN_PASSWORD=... pnpm seed   # 创建管理员 + 载入种子内置配置 + 默认模型目录
pnpm dev:api
pnpm dev:gateway
pnpm dev:console
```

## Docker 一键启动（与服务器部署同一套编排）

```bash
cp .env.example .env    # 至少填 PLATFORM_SECRET_KEY；正式部署再填域名与上游
pnpm docker:up          # = docker compose up -d --build
```

一条命令拉起整栈，顺序由 compose 依赖保证，全部可重复执行：

```text
postgres（healthcheck 就绪）→ migrate（一次性，应用 migrations/*.sql）
  → seed（一次性，管理员 + 内置配置 + 默认模型目录）→ api + gateway（常驻，unless-stopped）
```

镜像由根目录 `Dockerfile` 多阶段构建（api / gateway 用 tsx 直跑 TypeScript；控制台在 `console-build` 阶段构建，产物并入 api 镜像以 `/console` 托管）。`.env` 通过 `env_file` 注入容器，`DATABASE_URL`、容器内端口在 compose 里显式覆盖为 compose 网络地址。

| 项 | 说明 |
| --- | --- |
| 访问地址 | 控制台 `http://localhost:8787/console/`、控制面 `http://localhost:8787`、网关 `http://localhost:8788` |
| 服务器部署 | 把 `.env` 的 `PUBLIC_API_ORIGIN` / `PUBLIC_APP_ORIGIN` / `GATEWAY_BASE_URL` / `COOKIE_DOMAIN` / `UPSTREAM_API_KEY` 换成正式值，服务器上执行同一条 `pnpm docker:up`；反向代理（HTTPS）指向 8787 / 8788 |
| 宿主机端口 | `API_HOST_PORT` / `GATEWAY_HOST_PORT` / `POSTGRES_HOST_PORT` 可改映射端口（改后需同步 `.env` 里的 `PUBLIC_*` 与 `GATEWAY_BASE_URL`） |
| 只起数据库 | `docker compose up -d postgres` 只起 Postgres（本地 `pnpm dev` 内部就是这么做的）；整条 `docker compose up -d` 会拉起全部服务并按依赖先跑迁移与种子 |
| 与本地 dev 并存 | 两者默认都占 8787 / 8788，不要同时用；需要并存时给 stack 设 `API_HOST_PORT` / `GATEWAY_HOST_PORT` 改映射端口 |
| 停止 | `pnpm docker:down`（停 api / gateway 与一次性容器，保留 Postgres 容器与数据卷） |
| 日志 | `pnpm docker:logs`；`docker compose ps -a` 查看健康状态（migrate / seed 显示已退出属正常） |

种子说明：`SEED_ADMIN_PASSWORD` 为空且管理员不存在时只打印提示（不创建管理员），内置配置与模型目录仍会载入并保持幂等；生产环境建议在 `.env` 设好一次性口令后再执行，或改由控制台邀请成员。

用量中的客户端 IP 默认只取 TCP 对端，忽略所有转发头；端口直连无需额外配置。使用可信反代时，才在 `.env` 设置 `GATEWAY_TRUSTED_PROXY_IPS` 为网关实际看到的代理精确 IP（逗号分隔，支持 IPv4/IPv6，不支持 CIDR/通配符）。仅这些对端提供的 `X-Forwarded-For` 会按链从右向左解析，遇到首个非可信地址即停止；没有 XFF 时才接受经校验的 `X-Real-IP`。反代须覆盖转发头或在 XFF 追加实际对端地址，并限制其入口访问；不要仅因地址属于私网或 Docker 网桥就信任它。Docker Desktop / NAT 若已丢失原始源地址，显示的可能是网桥地址，此配置不能凭空恢复客户端 IP。

旧的手工部署方式（不用容器）仍可用：`pnpm build`（console 产物在 `apps/console/dist`），设置 `CONSOLE_DIST_DIR` 由 API 以 `/console` 路径托管；或把产物托管到 `console.mendao.chat` 并设置 `PUBLIC_CONSOLE_ORIGIN`（已内置带凭据 CORS）。

## 域名与对客端

| 域名 | 用途 |
| --- | --- |
| `api.mendao.chat` | 控制面；同时默认托管登录页（`PUBLIC_APP_ORIGIN`）与控制台（`/console`） |
| `app.mendao.chat` | 可选：拆分登录页时指向 API 并设置 `PUBLIC_APP_ORIGIN`（同站子域，cookie 设置 `COOKIE_DOMAIN=.mendao.chat`） |
| `gw.mendao.chat` | 模型网关；地址写入用户级下发配置（`GATEWAY_BASE_URL`），不是客户端环境变量 |
| `console.mendao.chat` | 可选：独立控制台 |
| `cdn.mendao.chat` | 可选：Release JSON 与安装包；`RELEASE_BASE_URL` 必须 https 且**不能有重定向**（客户端下载边界 `redirect: "error"`） |

客户端侧只需在构建/发布时注入环境变量（不新增配置项）：

```bash
SHUCODE_BASE_URL=https://api.mendao.chat     # 桌面/Web 控制面
VITE_SHUCODE_BASE_URL=https://api.mendao.chat # Web 端
```

客户端集成（欢迎页按钮、`zcode://platform/callback` deep link 路由、平台登录服务、user-config 同步调度）在 Shucode 仓库实现，见 spec 的分期计划。

## 关键契约（不可轻改）

- **用户级配置信封**与客户端 `ProviderProvisioningEnvelope` 同构，`packages/contracts/src/provisioning.ts` 是客户端 `packages/shared/src/provider-provisioning.ts` 的拷贝——两边必须一字不差。
- **网关设备 token 必须写入 provider 规则的 `config.access.apiKey`**（`withPlatformAccessToken`）：客户端 Registry 完整校验要求 Provider access 完整，缺 apiKey 时 Provider 会被整份丢弃（provider-not-found → 信封回滚）；credentials 通道的 `account-provider:*:api-key` 仅用于 Account Provider，普通 personal provider 不消费。
- **偏好默认模型**：信封的 `defaultModelSelection` 只作"偏好"，客户端会在应用前剥离、应用成功后用内置模型规则的档位事实（reasoningLevel）补全再写入——云端不需要（也无法）知道各模型的合法档位。
- **下发协议默认 OpenAI chat**（`GATEWAY_API_TYPE`，默认 `openai-chat-completions`，baseUrl 带 `/v1` 前缀）：客户端对网关发 `/v1/chat/completions`；网关两条协议路由（chat / messages）共享鉴权与额度管道，并把协议透传到同一上游。**模型目录中的模型名必须与上游真实模型名一致**，或用控制台的 `upstreamModel` 字段映射，否则上游会 400/404。
- **额度执行只在网关**：准入按 `max_tokens × 输出单价` 预留（`tryReserveQuota` 原子检查），流结束/非流式响应后按上游 usage 结算（`settleUsage` 预留转实扣，与账本写入同一事务）；上游失败/客户端中断释放预留并记录 error/aborted。客户端配置可被用户修改，不能作为额度执行点。
- **设备凭据只签发一次**：`consumeLoginFlow` 条件更新保证并发轮询只有一个赢家；token 以 sha256 哈希索引 + AES-256-GCM 密文保存（`PLATFORM_SECRET_KEY`），吊销即时生效。
- **Release 发布**：Console 发布自动分配下一个 revision 并设为生效；内容必须是客户端可解析的完整配置，建议以 `config/provider/shucode-builtin.json` 为模板修改（服务端只做结构粗检，最终校验在客户端，错误内容会被整份拒绝并回落旧缓存）。
- **公开配置信封**字段对齐客户端各解析器：`configs.feedbackUrl`、`configs.builtin_provider_config_json`（仅 https 下发）、`configs.forceUpdate.minimalVersion`、`configs.pluginStoreOrder`。

## 桌面更新（未完成）

`/api/v1/releases/electron/manifest` 目前固定返回 `version: 0.0.0`（等价于“无更新”），桌面自动更新会静默通过。接入自有安装包分发前，**不要在控制台填写强制更新版本号**（否则客户端会被要求更新却拉不到包）。后续步骤：接入安装包上传/CDN 与真实 manifest。

## 测试

```bash
pnpm test        # contracts: 信用点；db: 配额临界区；api: 登录流 E2E + Release；gateway: 计量/额度/流式
pnpm typecheck
```

## 已知边界（MVP 内）

- 单团队：用户取第一个未禁用团队作为额度归属；多团队与切换后续评估。
- 多上游为「同构透传」：供应商在控制台「上游供应商」按需接入（OpenAI 兼容为默认协议，密钥加密存储），模型在「模型目录」绑定供应商，未绑定回落 `UPSTREAM_BASE_URL`/`UPSTREAM_API_KEY` 单组配置；跨协议翻译（客户端说 A 协议、上游是 B 协议）是后续项。
- 未托管额度充值流程：额度调整在控制台人工操作；计费口径为参考 credit，非发票账单。
- 控制台登录限流为单实例内存实现；多实例部署时需替换为共享存储限流。
