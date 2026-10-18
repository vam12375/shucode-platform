# 更新日志

本文件记录 shucode-platform（Mendao 平台）的重要变更。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

本仓库为独立平台服务（控制面 + 模型网关 + 管理控制台），与 Shucode 客户端仓库分开发布；客户端接入方案见 Shucode 仓库 `specs/platform/multi-tenant-saas.md`。

版本规则：自 `0.1.0` 起算——新模块 / 新功能递增 `Y`（`Z` 归零）；优化、修复、文档等小改动递增 `Z`；不兼容改动递增 `X`。每个功能模块提交对应一个版本 tag。

## [0.13.0] - 2026-10-18

### 新增（控制面）

- 用量接口支持 `model` / `providerId` / `status` 诊断筛选（白名单与 UUID 校验，脏参数 400），聚合与流水联动。
- 供应商「连通/推理测试」结果持久化到 `last_test_*`（每次测试后落库，含失败归类与截断的上游错误详情，密钥不落库不出接口）。
- 模型定价库 GET/PUT 版本化：整表保存携带 `expectedVersion`，版本不符返回 409「定价库已被他人修改」，不再静默覆盖并发编辑。
- 管理动作审计：用户增改、供应商增改删/测试、模型增改删/导入、定价更新、配置发布与切换、设备吊销、团队改名全部留痕（`actor/action/target/detail`，凭据与请求正文不入审计）；新增 `GET /console/api/audit` 游标翻页查询。
- 团队额度调整流水：PATCH 额度/不限额度先取旧值标量快照（规避内存实现引用别名）再写入前后快照；团队详情接口附带最近 10 条调整记录。
- 新增 `GET /console/api/usage/series`：按上海时区天/小时聚合请求数、Token、费用与平均耗时（配合控制台时区化图表）。

## [0.12.0] - 2026-10-18

### 新增（模型网关）

- 供应商自动熔断（New API 借鉴）：绑定供应商的请求在网络失败 / 超时 / 5xx / 401 / 403 时计入连续失败，达到 `PROVIDER_AUTO_DISABLE_THRESHOLD`（默认 3，0 关闭）自动停用；成功转发即重置计数。404 等请求侧问题与 429 限流不计入。
- 熔断不覆盖人工决定：仅启用中的参与统计，人工停用不参与也不会被自动停用覆盖；控制台「启用」即清除自动停用现场。
- 被拒请求报错区分「自动熔断（连续失败原因）」与「已手动停用」。
- `.env.example` / `.env.production.example` 补 `PROVIDER_AUTO_DISABLE_THRESHOLD` 模板；网关测试 ENV 同步新字段，新增熔断行为与阈值解析测试。

## [0.11.0] - 2026-10-18

### 新增（数据层）

- 用量存储的 `listUsage` / `aggregateUsage` 支持按模型、上游供应商、状态组合筛选（Postgres 参数化查询 + 内存实现同语义），对应内存与聚合联动测试。
- 供应商运维状态落库（迁移 `0006_upstream_provider_ops.sql`）：最近一次连通/推理测试结果（时间、结果、分类、HTTP 状态、延迟、探测模型、错误详情）、停用原因（manual / auto）、自动停用时间与原因、连续失败计数；新增 `recordProviderTestResult` / `reportUpstreamFailure`（单语句原子「计数 + 条件熔断」，不覆盖人工停用）/ `reportUpstreamSuccess` 端口。
- 模型定价库乐观锁（`pricing-doc.ts`）：内容寻址版本 `prc_<sha256>`，`getModelPricing` / `saveModelPricing` 且版本不符时拒绝写入；KV 旧数据无需迁移。
- 管理动作审计与团队额度调整流水（迁移 `0007_admin_audit.sql`）：`admin_audit_logs`（谁/动作/目标/元数据，时间倒序索引）与 `team_quota_adjustments`（额度前后快照 + 不限额度切换）；新增相应读写端口，操作者经 join 返回邮箱。
- 新增用量时间序列端口 `aggregateUsageSeries`（按上海墙钟天 / 小时分桶，升序返回）与 `latencyMsSum`（null 延迟不计入均值），内存与 Postgres 同语义，配套 `usage-series.test.ts` 时区分桶测试。

## [0.10.1] - 2026-10-17

### 文档

- README 补客户端 IP 采集语义：默认只取 TCP 对端、忽略所有转发头，端口直连无需配置；使用可信反代时才设置 `GATEWAY_TRUSTED_PROXY_IPS`（网关实际看到的代理精确 IP，逗号分隔，支持 IPv4/IPv6，不支持 CIDR/通配符），并说明反代须清洗转发头、私网/Docker 网桥地址不等于可信、NAT 后无法凭空还原原始来源。
- `.env.production.example` 补 `GATEWAY_TRUSTED_PROXY_IPS` 生产模板说明。

## [0.10.0] - 2026-10-17

### 新增

- 控制台用量页明细表新增「真实IP」列：展示每条请求的客户端真实 IP，来源不可知时显示「—」（团队详情「最近请求」暂不展示）。

## [0.9.0] - 2026-10-17

### 新增

- 网关采集客户端真实 IP（`client-ip.ts`）：默认只记 TCP 对端地址并忽略一切转发头；仅当对端命中 `GATEWAY_TRUSTED_PROXY_IPS`（精确 IP 逗号分隔，拒绝 CIDR/通配符）时才解析 `X-Forwarded-For`——按代理链从右向左走、遇到首个非可信地址即停，不采信客户端预置的最左项；无 XFF 时才接受可信反代提供的 `X-Real-IP`。IPv6 与 IPv4-mapped 地址统一规范化，网桥/私网地址不因本身是私网就被信任。
- `/v1/messages`、`/v1/chat/completions`、`/v1/responses` 三条协议路由均把解析结果写入用量记录；测试环境（无 Node incoming）不采信转发头。
- 测试：IP 规范化、可信代理解析、env 解析与三条路由带 IP 的用例（gateway 共 108 用例）。

## [0.8.0] - 2026-10-17

### 新增

- 迁移 0005：`usage_records` 新增可空 `client_ip` 列（历史数据与无法确定对端时保持 NULL）。
- 存储端口（`UsageRecord.clientIp`）与 Postgres 读写同步，内存实现测试覆盖。

## [0.7.2] - 2026-10-17

### 修复

- deploy 流水线 SSH 步骤显式设置 `command_timeout: 120m`：appleboy/ssh-action 默认 10 分钟，首部署跨境拉取 ghcr.io 全量镜像远超 10 分钟会被中途掐断（已拉完的层留在服务器本地，下次 pull 自动续传）。

## [0.7.1] - 2026-10-17

### 修复

- deploy 流水线 SSH 步骤补 `set -euo pipefail`：此前远端 compose 报错（如 `POSTGRES_PASSWORD` 缺失）时，后续 `docker image prune -f` 的 0 退出码会掩盖失败，job 假绿而服务实际未部署。

## [0.7.0] - 2026-10-17

### 新增

- 生产自动部署流水线（`.github/workflows/deploy.yml`）：push main 触发（支持 `workflow_dispatch` 手动跑），先 typecheck + test 把关，再按 Dockerfile target（api / gateway / tools）构建三个镜像推到 ghcr.io（`latest` + `sha-` 双标签，GHA 构建缓存），最后同步 compose 文件并 SSH 到服务器 `pull && up -d`；`concurrency` 排队防并行部署。前置条件：仓库 Secrets 配置 SSH_HOST / SSH_USER / SSH_KEY（deploy.yml 头部注释）。
- 生产编排覆写（`docker-compose.prod.yml`）：与基础 compose 叠加使用，服务器不本地构建、镜像一律取 ghcr.io；postgres 端口强制只绑回环（5432 不暴露公网），数据库密码强制来自服务器 `.env`（未设置直接报错，避免弱口令上公网），容器内 `DATABASE_URL` 一律走 compose 网络。
- 生产环境模板（`.env.production.example`）：服务器一次性准备步骤（装 Docker、ghcr 登录、密钥生成命令、云防火墙只放行 22 / 8787 / 8788）与全部环境变量占位；IP 直连标注「未定稿」，域名与 HTTPS 定稿后同步替换对外地址。
- `docker-compose.yml` 注释同步双文件叠加的部署方式。

## [0.6.1] - 2026-10-17

### 文档

- 新增 `AGENTS.md` 开发规范：权威契约与双仓库同步规则、额度执行与凭据边界、开发命令与时区语义备忘、服务器部署「未定稿」约束。
- 新增 `design.md`：控制台 UI 重设计方案——三方向可交互原型（账房 / 计量台 / 印坊）与「计量台 · 方向 B」的落地依据。
- README 同步 Docker 一键部署（`pnpm docker:up`）与统一开发脚本（`pnpm dev` / `dev:memory`）说明。
- CHANGELOG 版本规范化：发布版本统一采用 `vX.Y.Z` 语义化递增，本批次按模块分节记录为 0.2.0–0.6.1。

## [0.6.0] - 2026-10-17

### 新增

- Docker 一键全栈：新增 `Dockerfile`（tools / api / gateway 多阶段构建，控制台构建产物打进 api 镜像以 `/console` 同源托管）与 `.dockerignore`；`docker-compose.yml` 编排 postgres（健康检查）→ migrate → seed → api / gateway（healthcheck 与依赖启动顺序），根目录 `.env` 经 `env_file` 注入；一条 `docker compose up -d`（等价 `pnpm docker:up`）起整栈，`docker:down` / `docker:logs` 配套，`API_HOST_PORT` / `GATEWAY_HOST_PORT` 可改映射端口与本地 dev 并存。
- 统一开发脚本：`pnpm dev`（Postgres → 迁移 → 种子 → api + gateway + console）与 `pnpm dev:memory`（进程内存储 + 演示数据），由 `scripts/dev.mjs` 驱动；console dev 端口调整为 5177。
- Lint 接入 oxlint：`pnpm lint` / `pnpm lint:fix`，全仓 0 警告 0 错误。

### 变更

- `.env.example`：补 `GATEWAY_API_TYPE`（平台下发协议：openai-chat-completions / anthropic-messages）、`UPSTREAM_TIMEOUT_MS` / `UPSTREAM_IDLE_TIMEOUT_MS` 上游限时，以及多上游供应商与 `PLATFORM_SECRET_KEY` 共享解密说明。

## [0.5.0] - 2026-10-17

### 新增

- 控制台滚动重构为「固定骨架 + 内部滚动」：读数条与侧栏常驻（`height: 100dvh`，主区 `.main` 自滚动），不再整页外部滚动；用量页与团队详情的「最近请求」长表自带内部滚动（约 60vh）与粘性表头，请求多时不必拖着整页滚到底。滚动条统一为「轨道透明、滚动时淡入、静止后渐隐」（标准 `scrollbar-color` 过渡 + 捕获级 scroll 监听切换 `.is-scrolling`，覆盖主区/侧栏/长表/下拉菜单所有内部滚动容器）。
- 控制台全站下拉替换为自制组件（`components/Select.tsx`，键盘可达：↑↓/Enter/Esc/Home/End，跟随触发器定位防裁剪、空间不足向上翻转），不再使用系统原生 `<select>`；顺带修正面板入场动画填充态（`both`→`backwards`）导致的浮层被后续面板遮盖问题。
- 控制台全局操作反馈：新增 Toast 堆栈（右上角、成功/警示/错误三态、自动消隐）与 Promise 化确认弹窗组件；保存、发布、创建等操作成功/失败均有提示，删除模型、禁用用户、吊销设备、移除成员改用自定义确认弹窗（重置密码为带输入框的弹窗），全站不再使用原生 `alert` / `confirm` / `prompt`。
- 控制台新页面：「上游供应商」与「模型定价库」（下发分组；定价库可增删改并粘贴 cc-switch 口径 JSON 合并）；用量新增按团队/时间筛选的明细「加载更多」游标翻页界面；「从上游导入」模型弹层（选供应商 → 拉取上游模型列表 → 勾选批量建档，已在目录的自动标记并跳过）。
- 模型目录每行可绑定上游供应商并编辑全量价格字段。

### 修复

- 控制台表单不再被浏览器密码管理器误判：上游供应商的 API Key 输入改用文本框 + CSS 圆点遮罩（`-webkit-text-security`，不再触发「要保存密码吗」弹窗与登录凭据自动填充）；创建用户初始密码与重置密码输入补 `autocomplete="new-password"`；真实登录页保持原生密码管理行为。

## [0.4.0] - 2026-10-17

### 新增

- 配置变更实时通知（SSE）：新增 `GET /api/v1/client/events`（Bearer 设备凭据长连接）——管理端模型/供应商/团队与成员/设备吊销/平台配置/Release 变更经进程内事件总线（300ms 去抖合并 domain）推送 `config.changed`，客户端收到即触发既有 user-config 同步（未连接时完全退回周期轮询，行为不变）；`user-config` 同时补 ETag 协商缓存（未变化 304）。事件语义（hello/retry/ping 心跳/断线重连）已写入两份 specs，客户端订阅端属 Shucode 仓库联调范围。
- 模型定价库（服务端）：内置 60+ 主流模型默认定价（`apps/api/src/assets`，cc-switch / models.dev 口径：每 1M token 价格）；支持增删改与粘贴 cc-switch `model-pricing.json` 合并更新（KV 持久化）；「模型目录」手动添加与「从上游导入」按模型 ID 命中即自动带价（含缓存读写价与显示名），仍默认停用待人工启用。
- 上游供应商管理 API（多上游阶段一 · 同构透传）：增删改查（默认 OpenAI chat 协议，密钥 AES-256-GCM 加密落库、只回显掩码）、「获取上游模型列表」对照填写 `upstream_model`、模型目录绑定供应商；未绑定回落 env 单上游；管理端变更触发 SSE 推送。
- 用量明细游标分页：`/console/api/usage` 与团队详情「最近请求」支持不透明游标翻页（`(created_at, id)` 元组，新写入不影响已翻页位置）。

### 修复

- 控制台安全：重置密码 / 禁用 / 降级用户后立即吊销其全部后台会话与设备凭据（此前重置密码后旧会话继续有效）；重新启用不回补已吊销凭据。

## [0.3.0] - 2026-10-17

### 新增

- 网关新增 OpenAI 兼容 `GET /v1/models`（设备凭据鉴权）：返回已启用平台模型清单（`object:"list"`，`owned_by:"mendao-platform"`），修复客户端 Provider「获取模型列表」404；与模型请求路由共用鉴权逻辑。契约已同步至平台与客户端两份 specs。
- 网关新增 OpenAI **Responses API**（`POST /v1/responses`）透传：与 chat completions 同属 OpenAI 兼容家族（绑定同一供应商，协议校验按家族一致），usage 解析 `input_tokens_details.cached_tokens`、流式按 `response.completed` 事件计量，准入预留读 `max_output_tokens`。
- 网关按模型绑定的供应商转发（多上游阶段一）：协议不一致 / 供应商停用返回 404，未绑定回落 env 单上游；用量记录写入 `provider_id` 归属。

### 修复

- 网关上游限时与中断收尾：连接与非流式响应体限时（`UPSTREAM_TIMEOUT_MS`，默认 120s），流式闲置限时（`UPSTREAM_IDLE_TIMEOUT_MS`，默认 60s），超时中断上游并释放已预留额度，非流式读取失败返回 504；上游流中断（含超时）此前会静默漏释放预留，现经 `TransformStream` 的 `abort` 路径统一按上游失败结算。
- SSE usage 采集：事件缓冲超 1MB 即丢弃并计数，无法解析的帧计数并输出告警日志，不再静默吞掉导致丢计量。

## [0.2.0] - 2026-10-17

### 新增

- 迁移 0002 · 用量表索引：`(created_at DESC, id DESC)` 复合索引替代原单列 `created_at` 索引以支撑游标元组比较；聚合覆盖索引让 `GROUP BY model` 走 index-only，不再逐行回表。
- 迁移 0003 · 上游供应商：`upstream_providers` 表（协议、base_url、AES-256-GCM 密文密钥、启停开关），用量记录新增 `provider_id` 归属，模型目录增加供应商绑定。
- 迁移 0004 · Responses 协议：供应商协议新增 `openai-responses`（Responses 专用接入：上游只提供 `/v1/responses`，chat 请求明确 404）。
- 存储端口（`ports.ts`）：新增上游供应商与模型绑定的读写语义、按 `(created_at, id)` 元组的游标翻页；Postgres 与内存实现保持同一端口语义（配额临界区不变），内存实现测试同步覆盖。

## [0.1.0] - 2026-10-15

### 新增

- 工程基线：pnpm workspace（Node 24 / pnpm 10）、共享 TypeScript 配置、`.env.example` 环境样例、本地 Postgres 的 docker-compose、mise 版本固定。
- `packages/contracts`：与 Shucode 客户端对齐的契约——Provider Provisioning 信封（syncId 幂等 / CAS / 回滚语义复用客户端实现）、公开客户端配置信封（帮助配置 / 内置配置下载地址 / 强制更新版本号 / 插件商店排序）、平台设备登录协议（`mdp_` 设备凭据、init/poll 状态机）、信用点计算（1 credit = 10⁶ micro-credit，价格按每 1M token 的 credit 数）。
- `packages/db`：存储端口（Ports）与语义契约；Postgres 实现（postgres.js 直连 + 手写迁移，额度预留为单语句原子 CAS）；内存实现（`MEMORY_DB=1` 本地体验与测试共用）；AES-256-GCM 凭据加密（`PLATFORM_SECRET_KEY`）与 scrypt 口令哈希。
- `apps/api` 控制面（Hono）：平台设备登录全链路（`/api/v1/platform/auth/init|poll` + 登录 / 授权 / deep link 中转页，`consumeLoginFlow` 条件消费保证凭据只签发一次）；匿名公开配置 `/api/v1/client/configs` 与 `/n/configs`；全局 Release JSON 托管（`/api/v1/releases/builtin/*`）与桌面更新清单占位（返回 `version: 0.0.0` 等价无更新）；用户级配置下发 `/api/v1/client/user-config`（Bearer 设备凭据，输出 provisioning 信封：网关 provider + 团队模型 + 默认模型 + 凭据）；管理控制台 API（登录 / 团队 / 成员 / 额度 / 用户 / Release 发布与切换 / 模型目录 / 设备吊销 / 平台配置 / 用量查询）与 `/console` 静态托管；内存模式内置演示数据。
- `apps/gateway` 模型网关：Anthropic 兼容 `/v1/messages`；平台凭据鉴权 → 团队额度准入（按 `max_tokens × 输出单价` 原子预留，超限 429）→ 转发上游 → 按上游 usage 结算（预留转实扣与账本写入同事务）；SSE 流式边透传边采集 usage（`message_start` / `message_delta`），上游失败或客户端中断释放预留并记录 error/aborted。
- `apps/console` 管理控制台（Vite + React）：控制台登录、团队列表与新建、团队详情（额度调整 / 不限额度开关 / 成员增删 / 模型用量汇总 / 最近请求）、用户管理（创建 / 禁用与启用 / 重置密码，禁用即时阻断登录与网关请求）、全局用量统计（按团队与时间筛选的模型汇总与请求明细）、内置配置发布与版本历史切换、模型目录与价格编辑、设备凭据吊销、平台配置（帮助链接 / 社区链接 / 强制更新版本号）。UI 按 `design/prototypes/meter.html`（方向 B · 计量台）1:1 落地：顶部四格读数条（今日请求 / 今日费用 / 活跃设备 / 生效版本，数值进位动效）、指针刻度尺额度表（近额转信号红）、分组机架侧栏、近额提示条与近 14 天费用柱图；纯 CSS 设计令牌与系统字体栈，零新增运行时依赖。
- 测试与验证：契约信用点、配额临界区（预留受限 / 结算转实扣 / 释放 / 不限额度 / 禁用团队）、控制面登录流 E2E（含吊销后 401）、Release 发布递增、网关计量（非流式 / 流式 / 额度不足 / 上游失败 / 凭据吊销）；`pnpm typecheck` 与 `pnpm test` 全绿，另有真实进程冒烟验证。
