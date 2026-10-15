# shucode-platform（Mendao 平台）

Shucode 客户端的多租户 SaaS 平台：平台账号登录、管理员控制台、模型目录与**团队共享额度**（credit）、内置配置（Built-in Release）发布、模型网关。

客户端接入方案与验收场景见 Shucode 仓库 [`specs/platform/multi-tenant-saas.md`](../Shucode/specs/platform/multi-tenant-saas.md)。域名暂定 `mendao.chat`。

## 组成

| 目录 | 说明 |
| --- | --- |
| `apps/api` | 控制面（Hono）：匿名客户端配置、平台设备登录流、用户级配置下发、管理控制台 API 与静态托管 |
| `apps/gateway` | 模型网关：平台凭据鉴权 → 团队额度准入（预留）→ 转发 Anthropic 兼容上游 → 按 usage 计量结算 |
| `apps/console` | 管理控制台（Vite + React）：团队/成员/额度、Release 发布、模型目录与价格、设备凭据、平台配置 |
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

## 本地启动（无需数据库）

```bash
mise install          # Node 24 / pnpm 10
pnpm install
MEMORY_DB=1 pnpm dev:api        # http://localhost:8787（内置演示数据，重启丢失）
pnpm dev:console                # http://localhost:5174（代理 /console/api 到 8787）
```

演示账号：`admin@mendao.chat / mendao-admin`（控制台，管理员）、`member@mendao.chat / mendao-member`（成员）。演示团队额度 1000 credit。

内存模式未配置 `PLATFORM_SECRET_KEY` 时会生成临时密钥（重启后旧设备 token 无法恢复，仅限体验）。

## Postgres 启动

```bash
docker compose up -d            # 本机 Postgres 17
cp .env.example .env            # 填 DATABASE_URL、PLATFORM_SECRET_KEY 等
pnpm migrate                    # 应用 migrations/*.sql
SEED_ADMIN_PASSWORD=... pnpm seed   # 创建管理员 + 载入种子内置配置 + 默认模型目录
pnpm dev:api
pnpm dev:gateway
pnpm dev:console
```

生产部署：`pnpm build`（console 产物在 `apps/console/dist`），设置 `CONSOLE_DIST_DIR` 由 API 以 `/console` 路径托管；或把产物托管到 `console.mendao.chat` 并设置 `PUBLIC_CONSOLE_ORIGIN`（已内置带凭据 CORS）。

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

- **用户级配置信封**与客户端 `ProviderProvisioningEnvelope` 同构，`packages/contracts/src/provisioning.ts` 是客户端 `packages/shared/src/provider-provisioning.ts` 的拷贝——两边必须一字不差；凭据 key 必须匹配 `account-provider:<providerId>:api-key`。
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
- 网关单上游：`UPSTREAM_BASE_URL`/`UPSTREAM_API_KEY` 单组配置；多上游路由后续按 `platform_models.upstream_*` 扩展。
- 未托管额度充值流程：额度调整在控制台人工操作；计费口径为参考 credit，非发票账单。
- 控制台登录限流为单实例内存实现；多实例部署时需替换为共享存储限流。
