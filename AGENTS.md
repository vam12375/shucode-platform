# shucode-platform 开发规范

本仓库是 Shucode 客户端的多租户 SaaS 平台：控制面 API 与模型网关（`server/`，Go）+ 管理控制台（`apps/console`，React）+ 跨端契约（`packages/contracts`）。独立仓库、独立部署（Docker 编排与服务器同一套）。后端已于 v0.20.0 完成 Go 全面重写，计划与兼容红线见 `specs/go-rewrite.md`（本地文档）。

## 权威契约（跨端，不可轻改）

- 跨端接口契约以 `specs/multi-tenant-saas.md` 为准——它是 Shucode 客户端仓库 `specs/platform/multi-tenant-saas.md` 的镜像副本：平台侧契约演进在本副本上进行，客户端侧章节以客户端仓库为准；**任何跨端契约改动（provisioning 信封 schema、登录流、网关协议、公开配置信封、Release 清单）必须同步更新两边文件**。
- 跨端 schema 的权威源在 Shucode 客户端仓库 `packages/shared/src/`（镜像副本见 specs）；服务端可执行镜像在 `server/internal/contracts`，与客户端 schema **必须同构**并受 `testdata/fixtures.json`（冻结对齐用例，随契约同步更新）测试夹紧。`packages/contracts` 现仅保留 console 消费的 `credits.ts`。
- 网关设备 token 必须写入 provider 规则的 `config.access.apiKey`（Go: `withPlatformAccessToken`），credentials 保持空数组——客户端 Registry 完整校验要求 access 完整，缺 apiKey 时整个 Provider 被丢弃（provider-not-found → 信封回滚）。
- 通用用户配置信封的 `defaultModelSelection` 只是"偏好"：客户端按模型当前合法能力初始化 reasoningLevel。参考原片分析是用户指定的例外：平台校验现有 DeepSeek 视觉模型公开支持 `high`，并在预检和 scoped 信封中显式下发 `options.reasoningLevel: "high"`；不支持时拒绝，不能静默降档。
- 平台用户级配置只拥有 `platform:gateway` 一个 provider，应用语义是合并（同名覆盖、其余保留），不是整份替换。
- 额度写入由存储事务唯一执行；文本准入在网关，媒体准入在媒体 owner。准入检查已结算余额/预算，不预留或冻结；SSE/JSON 结束按合法实际 usage 幂等实扣，缺少 usage 不按最大输出估算。并发已受理费用完整入账、可负余额，余额耗尽后拒绝新请求。后台未知结果保持核对任务、不占余额。详见 `specs/credit-settlement.md`。
- 设备凭据只签发一次（`consumeLoginFlow` 条件更新）；token 只存 sha256 哈希 + AES-256-GCM 密文，吊销即时生效。
- Release 下载边界是 `redirect: "error"`：CDN/对象存储必须直出 200，不能 301/302。模型目录中的模型名必须与上游真实模型名一致，或用控制台的 `upstreamModel` 字段映射。

## 用量接口的时区语义（易踩坑）

- 用量的 `from`/`to` 查询参数**不带时区**：控制台传 `YYYY-MM-DDTHH:mm:ss` 之类的 naive 串，服务端按 UTC 解析（与 Postgres 会话一致）。
- 趋势图（`/console/api/usage/series`）的分桶在 SQL 中按 **Asia/Shanghai 墙钟**切分（天桶形如 `YYYY-MM-DDT00:00:00`，即 UTC 16:00 为日界），前端展示也按上海墙钟；psql 直查的时间是 UTC。两边对不上数时先核对这三套口径。
- 网关 `client_ip` 默认只取 TCP 对端，仅 `GATEWAY_TRUSTED_PROXY_IPS` 中的精确地址可提供转发头。

## 命令

Node 版本以 `mise.toml` 为准（Node 24 / pnpm 10）；Go 版本 1.26（补丁钉 1.26.10），本机不需要安装——`server/Makefile` 的所有目标都在 `golang:1.26.10` 容器里执行（默认 GOPROXY 走 goproxy.cn，可用环境变量覆盖）。从仓库根目录执行：

| 用途 | 命令 |
| --- | --- |
| 类型检查 | `pnpm typecheck`（console + contracts） |
| 前端/契约测试 | `pnpm test` |
| Lint | `pnpm lint` / `pnpm lint:fix`（oxlint）；`make -C server lint`（gofmt + vet） |
| Go 测试 | `make -C server test`（`go test -race ./...`） |
| 一体化开发 | `pnpm dev`（Postgres → 迁移 → 种子 → api + gateway + console；后端在容器内 go run） |
| 无库体验模式 | `pnpm dev:memory`（进程内存储 + 演示数据） |
| Docker 全栈 | `pnpm docker:up` / `docker:down` / `docker:logs` |
| 迁移 / 种子 | `pnpm migrate` / `pnpm seed`（= `make -C server migrate/seed`） |
| 单独调试 | `pnpm dev:api` / `dev:gateway` / `dev:console` |
| Go 构建 | `make -C server build`（四二进制到 `server/bin/`） |

- 开发端口：api 8787、gateway 8788、console dev 5177。这些端口常被其它会话占用；清理进程前先查父子进程归属，未经用户明确授权不清理他人进程。
- Docker 整栈与本地 dev 默认同占 8787/8788，不要同时运行；需要并存时给 stack 设 `API_HOST_PORT`/`GATEWAY_HOST_PORT` 改映射端口。
- 只起数据库用 `docker compose up -d postgres`；整条 `docker compose up -d` 会按依赖先跑迁移与种子再拉起全部服务。
- Go conformance 全量测试需要 `TEST_DATABASE_URL`（PG 套件）与 `TEST_REDIS_URL`（Redis 限流/总线）与 `TEST_MIGRATIONS_DIR`（默认 `../../migrations`，相对 `server/internal/store`）；缺省时相应套件自动跳过。
- 共享文件（如本文件、README）编辑前必须重读，可能有其它会话并行改动。

## 实现与验证

- 数据访问一律走 `server/internal/store` 的存储端口（`store.go`）；api/gateway 不直连 SQL。Postgres 与内存实现（`MEMORY_DB=1`）必须同语义，由 `conformance_test.go` 的统一套件与跨实现对比测试夹紧，尤其是配额临界区。
- 内存实现的读写一律返回副本，禁止复刻旧 TS 内存库「get* 返回原地引用」的别名陷阱；「读旧值→改→写」逻辑先取标量快照。
- 契约校验：跨端 schema 的 Go 镜像在 `server/internal/contracts`；契约演进时以客户端仓库 schema 为准同步 Go 镜像，并更新冻结的 `testdata/fixtures.json`/`vectors.json` 保证对齐测试绿。
- Redis 只用于登录限流与配置事件总线（`REDIS_URL` 设置时）；配额与会话/设备 token 权威仍在 Postgres，**永不**用 Redis 做额度或凭据的真相源。
- 有行为改动必须执行 `pnpm typecheck`、`pnpm test` 与 `make -C server lint test`，报告真实结果；不将已有失败写成通过。
- 修复 bug 用中文注释说明根因与修复依据；发现设计缺陷先与用户对齐，不不断增加兜底分支掩盖问题。
- 凭据（平台 token、上游 key、口令）不进日志、不进配置明文。
- 设计思路、验收记录、价格/成本等敏感说明统一放 `docs/`；`docs/`、`specs/`、`design/` 是本地忽略目录，禁止提交入库或复制到公开 README、代码注释和提交信息。代码包内只保留不含敏感信息的测试夹具 README。

## Console UI

- 视觉语言为「计量台」（顶部读数条 + 指针刻度尺额度 + 琥珀信号，纯 CSS），规范见 `design/design.md`，组件在 `apps/console/src/components/`。
- 控制台默认由 API 同源托管在 `/console`（Go 静态托管，SPA fallback）；拆分独立域名部署时设置 `PUBLIC_CONSOLE_ORIGIN`（已内置带凭据 CORS）。

## 服务器部署（未定稿）

- 域名、密钥、上游供应商尚未定稿：相关值按「未定稿」处理，不要硬编码进代码或当作正式配置写入文档。
- 桌面更新清单 `/api/v1/releases/electron/manifest` 仍是 stub（固定 0.0.0）；接入自有安装包分发前，**不要在控制台填强制更新版本号**（客户端会被要求更新却拉不到包）。
- 镜像由 GitHub Actions 构建（api / gateway / tools 三个 target）推 ghcr，服务器 SSH 拉取重启；compose 自动注入 `REDIS_URL=redis://redis:6379/0`。
