# 更新日志

本文件记录 shucode-platform（Mendao 平台）的重要变更。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

本仓库为独立平台服务（控制面 + 模型网关 + 管理控制台），与 Shucode 客户端仓库分开发布；客户端接入方案见 Shucode 仓库 `specs/platform/multi-tenant-saas.md`。

版本规则：自 `0.1.0` 起算——新模块 / 新功能递增 `Y`（`Z` 归零）；优化、修复、文档等小改动递增 `Z`；不兼容改动递增 `X`。每个功能模块提交对应一个版本 tag。

## [0.24.1] - 2026-10-19

### 配置与部署

- 新增环境变量：`API_TRUSTED_PROXY_IPS`、`CONSOLE_ALLOWED_HOSTS`、`BOOTSTRAP_SECRET`、`ALLOW_INSECURE_UPSTREAM_URLS`、`UPSTREAM_ALLOW_PRIVATE_NETWORKS`。
- 新增宿主机 Nginx 部署配置与指南（`deploy/nginx/`）：提供 HTTPS、证书续期、Cloudflare 真实 IP 与 SSE 反代配置；生产编排强制 api/gateway 只绑回环，CI 同步部署文件供运维安装。
- `docker-compose.prod.yml` 镜像改用 `IMAGE_TAG`（默认 `latest`），CI 部署注入 `sha-<commit>`。

## [0.24.0] - 2026-10-18

### 新增(多租户团队工作台)

- 控制台按成员关系授权：团队 `owner/admin` 管理本团队，普通成员只读本人用量、设备与资料；全局 `admin` 标记不再授予平台管理权限（见 `specs/tenant-permissions.md`）。
- 新增团队工作台（概览 / 成员管理 / 预算与限制 / 团队设置）：邀请链接（绑定邮箱、7 天过期、只消费一次、数据库只存摘要）、成员月预算、暂停与恢复、团队日/月预算、模型白名单与预算告警阈值。
- 非 root 的用量、导出与审计按团队收敛；用量响应剥离上游模型、供应商、客户端 IP 与价格/成本快照；无有效成员关系或团队停用返回 403，多有效团队返回 409。
- 新增「团队与账号」合并页：团队台账与账号主表同屏，新建账号可直接选择团队，所属团队列内支持加入 / 更换 / 移出；账号详情抽屉汇总近 30 日用量、设备、成员预算与安全操作。
- 顶部读数条新增「查看范围」段，范围与读数同屏，替代侧栏切换器；用量页移除重复的按团队筛选。
- 控制台新增权限路由与用量成本测试（`apps/console/tests`），随 `pnpm test` 执行。

### 新增(额度申请与预算管控)

- 团队管理员可提交额度申请，root 审批或驳回；批准与额度增加同事务且只执行一次（迁移 `0014_quota_requests.sql`）。
- 团队日/月预算与成员月预算在同一预留事务内校验（留空为不限、0 为暂停）；全部未释放预留保守计入当前窗口，避免跨日/月漏算。
- 团队策略的 RPM/TPM/并发与平台上限取较小值，团队配置不能扩大平台授权；模型白名单对推理、模型目录与用户配置下发一致生效（迁移 `0015_team_policy_limits.sql`）。

### 新增(root MFA 与会话安全版本)

- root 支持 TOTP 双因素认证：绑定、启用、恢复码轮换与登录挑战；MFA 设置与恢复码仅 root 可管理。
- 新增 `sessions.security_version`（迁移 `0016_session_security_version.sql`）：改密、禁用或吊销凭据时提升安全版本，旧会话即时失效。

### 新增(用量价格快照与成本估算)

- 结算事务冻结成交单价与参考成本快照（迁移 `0013_usage_pricing_snapshot.sql`）；历史记录不随调价回写，旧记录按未知成本处理。
- 用量页新增当前页历史成本估算与「按当前售价重算」对照，币种独立统计；新增控制台用量成本计算与测试（`apps/console/src/usage-costs.ts`）。

### 安全加固（审计整改）

- 控制面登录/初始化限流改为与网关相同的可信反代规则：只有 TCP 对端命中 `API_TRUSTED_PROXY_IPS` 时才解析 `CF-Connecting-IP` / `X-Forwarded-For`，直连源站无法再通过伪造头切换限流桶。
- 登录流 `pending → authorized/denied/expired` 改为条件更新，避免旧授权请求把已消费流程回写为 `authorized`；消费仍保持 authorized → consumed 的 CAS。
- 首次 root 初始化新增 `BOOTSTRAP_SECRET`（请求头 `x-bootstrap-secret`）；生产模式未配置时拒绝初始化，控制台初始化页增加口令输入。
- 控制面与网关统一禁止上游重定向；供应商 `baseUrl` 强制 https、拒绝 userinfo/查询串；出站默认拒绝回环、私网、链路本地与云元数据地址（`UPSTREAM_ALLOW_PRIVATE_NETWORKS=1` 可放行自建内网）；新建供应商与修改地址/密钥仅 root 可操作。
- OpenAI 流式计量强制 `stream_options.include_usage=true`；上游成功但缺失 usage 时按准入预留额保守结算并标记 `usage_missing_estimated`。准入预留同时覆盖输入估算、`max_tokens` 夹紧写回上游，`n` 支持 1..16 并按倍率预留。
- 结算使用 `context.WithoutCancel` + 10s 超时并重试 3 次，客户端取消不再导致 Postgres 事务无法落库；预留回收改为按当前时间比较已带 TTL 的 `expires_at`；Redis 并发槽位增加持槽续租。
- `request_id` 幂等改为按 `(team_id, request_id)` 隔离，新增迁移 `0011_request_id_scope.sql`。
- 请求边界：控制台 JSON 1 MiB、登录表单/初始化 64 KiB、网关请求与上游非流式响应各 32 MiB；API/网关设置读取头、读取请求、空闲连接超时；登录流增加匿名初始化限流与过期清理。
- 安全响应头（CSP、nosniff、DENY、Referrer-Policy、HSTS）与写操作 Origin/Sec-Fetch-Site 校验；HTTPS 公开地址下按已配置 Host 将 http 请求 308 跳转；新增可选 `CONSOLE_ALLOWED_HOSTS` 限制管理面域名。
- 移除 Gin 默认 recovery（避免 broken-pipe 时 dump Cookie/x-api-key）；上游错误正文返回/落库前脱敏当前供应商密钥；控制台 API Key 输入改为 password 字段。
- 新口令哈希工作因子升级为 scrypt N=2^14/r=8/p=5，登录成功后对旧哈希渐进重哈希。
- 新增可选设备凭据寿命 `DEVICE_TOKEN_TTL_DAYS`（默认 0 = 不按年龄过期）；管理面登录/初始化限流在 Redis 故障时改为 fail-closed。
- 依赖升级：`golang.org/x/net` 0.60.0、`golang.org/x/text` 0.42.0、`quic-go` 0.59.1、Vitest 4.1.12；`pnpm audit` 与 `govulncheck` 均无命中（govulncheck 固定 v1.9.0 并接入 CI）。
- `.env*` 统一不进 Git/构建上下文；运行镜像改为非 root、只读根文件系统、`cap_drop: ALL`、`no-new-privileges` 与资源上限；基础镜像固定到 manifest digest。
- CI Actions 固定到 commit SHA、SSH 部署校验证书指纹、部署按 `sha-<commit>` 不可变镜像标签；新增 Dependabot。

## [0.23.0] - 2026-10-18

### 新增(角色分级与 root 初始化)

- 用户角色调整为 `root`（唯一超级管理员）、`admin`（普通管理员）、`user`（普通用户）；存量 `member` 迁移为 `user`。
- root 只能在控制台首次初始化时创建，账号与口令只以 scrypt 哈希写入数据库，不再读取 `SEED_ADMIN_EMAIL` / `SEED_ADMIN_PASSWORD`。
- 数据库增加 root 唯一索引与「root 不可禁用」约束；接口层禁止 root 被降级、禁用或由其他账号修改。
- 普通管理员只能管理普通用户；创建管理员、管理员/用户角色升降级仅 root 可执行。
- 存量部署升级时，最早创建的可用管理员自动提升为 root，沿用原密码登录。

### 迁移与配置

- 新增迁移 `0010_root_role.sql`：角色枚举扩展、`member` → `user`、存量管理员提升、root 唯一与不可禁用约束。
- seed 只负责内置配置与模型目录；`.env.example` / `.env.production.example` 移除 `SEED_ADMIN_*`。
- 控制台新增首次 root 初始化页；用户页按操作者角色展示可执行操作。

## [0.22.0] - 2026-10-18

### 新增(人民币定价与 DeepSeek 价目)

- 成本价库切换为人民币口径：条目新增 `currency` 与 `offPeakMultiplier`；DeepSeek 两个官方模型按官方 CNY 高峰价入库。
  - `deepseek-flash`：入 2 / 出 8 / 缓存读 0.04 元每 1M tokens，空闲倍率 0.5。
  - `deepseek-v4-pro`：入 9 / 出 27 / 缓存读 0.30 元每 1M tokens，空闲倍率 0.5。
- 网关按北京时间高峰窗口计费：周一至周五 09:00-12:00、14:00-18:00 为高峰，其余时段（含周末/法定节假日）应用空闲倍率；法定节假日通过 `PEAK_HOURS_HOLIDAYS` 补充。
- 模型目录售价字段改为人民币展示（1 credit = ¥1），并在模型卡片展示只读成本价（来自定价库，含高峰/空闲）。
- 定价库页面新增币种、空闲倍率、空闲价列与搜索框；DeepSeek 条目可完整对照官方价目。
- 用量页新增「成本估算」：按成本价库逐模型计算，币种随条目显示（USD/CNY），不做汇率折算；并增加「按当前售价重算」区间说明。

### 迁移与配置

- 新增迁移 `0009_cny_pricing.sql`：模型空闲倍率列、DeepSeek 模型与成本价库数据更新；新增 `deepseek-v4-pro` 模型行（沿用 DeepSeek 供应商）。
- 新增环境变量 `PEAK_HOURS_HOLIDAYS`（北京时间法定节假日，逗号分隔 `YYYY-MM-DD`）。
- 新增测试：高峰/空闲时段判定、网关高低峰费率、成本价扩展字段保留。

## [0.21.0] - 2026-10-18

### 新增(网关可靠性)

- 渠道池：新增 `upstream_routes`，一个模型可绑定多个上游供应商；网关按 `priority` 升序、同优先级按 `weight` 加权调度，流式响应开始前对网络错误/超时/408/429/5xx/401/403 自动重试下一个上游。
- 自动恢复：定时探测自动熔断的供应商（`GET /v1/models`），恢复后重新纳入调度；控制台「上游供应商」增加「恢复调度」按钮。
- 网关不再静默回落：已配置路由池但全部不可用时，返回明确的 404，而不是把请求发到 env 默认上游。

### 新增(流控与计费)

- 团队级 RPM/TPM/并发限制（`teams.rpm_limit` / `tpm_limit` / `concurrency_limit`），Redis 共享计数、进程内回退，超限返回 429 + `Retry-After`；控制台团队详情可编辑。
- 额度预留状态机：新增 `quota_reservations`，`request_id` 唯一；重复请求返回 409，预留/结算/释放/过期四态可对账。
- 预留回收：api/gateway 启动 sweeper，定期回收超时未结算预留；`GATEWAY_RESERVATION_TTL_SEC` 可配。
- 计费幂等：`SettleReservation` 在同一事务内完成预留扣回、usage 落库与小时汇总，重复结算幂等。

### 新增(运维与用量)

- 运维大盘 `/console/api/ops/*`：QPS/请求数、成功率、P50/P95/P99、消费、活跃设备、未释放预留、供应商健康；控制台新增「运维大盘」页面（Recharts 交互图表 + hover tooltip）。
- 告警规则与事件：`admin_alert_rules` / `admin_alert_events`，支持错误率、P95 延迟、未释放预留、窗口消费、上游错误数；可配 `ALERT_WEBHOOK_URL` 推送。
- 用量导出：`GET /console/api/usage/export` 按当前筛选导出 CSV（UTF-8 BOM，Excel 友好）。
- 用量检索：`requestId` 精确过滤；控制台用量页新增 request ID 输入框与导出按钮。
- 小时 rollup：新增 `usage_hourly_rollups`，全平台趋势优先读汇总表；明细维度仍回退 `usage_records`。

### 变更(控制台图表)

- 引入 Recharts 替换纯 CSS 柱状图：费用趋势（柱 + 请求数折线）、缓存率趋势（面积 + 命中率折线）、模型分布环形图，均带图例、坐标轴与 hover tooltip。
- 用量趋势默认值改为「今天 + 按小时」（`apps/console/src/pages/Usage.tsx`）。

### 迁移与配置

- 新增迁移 `0008_platform_ops.sql`：路由池、团队流控列、预留表、小时汇总表与回填、request_id/供应商/状态索引、告警表。
- 新增环境变量：`GATEWAY_MAX_ATTEMPTS`、`GATEWAY_RESERVATION_TTL_SEC`、`PROVIDER_PROBE_INTERVAL_SEC`、`ALERT_WEBHOOK_URL`（`.env.example` 已同步）。
- 新增 Go 测试：渠道路由/故障转移、RPM 限流、重复 request_id、预留状态机、rollup 与 ops/告警 conformance；Postgres conformance 已在本地库验证。

## [0.20.6] - 2026-10-18

### 修复(开发脚本)

- `scripts/dev.mjs` 移除 `startService` 环境变量展开中的空对象兜底（`...(service.env ?? {})` → `...service.env`），消除 oxlint `unicorn(no-useless-fallback-in-spread)` warning；`pnpm lint` 恢复 0 warnings / 0 errors。

## [0.20.5] - 2026-10-18

### 清理

- 删除 `apps/api`、`apps/gateway`、`packages/db` 的残留目录（源码已于 0.20.0 删除，本地仅剩未跟踪的 node_modules 缓存）。
- `pnpm-workspace.yaml` 收窄为 `apps/console` + `packages/contracts`；`pnpm install --frozen-lockfile` 后 workspace 为根 + 两个包，锁文件无需变更。

## [0.20.4] - 2026-10-18

### 修复(部署)

- `docker-compose.prod.yml` 为 api/gateway/migrate/seed 增加 `build: !reset null`，清除 base compose 的构建段：服务器 `docker compose pull` 只拉 ghcr 镜像，不再提示必须先 build。
- 本地不带 prod 覆写的 `docker compose up -d` 不受影响，仍按 Dockerfile 构建。

## [0.20.3] - 2026-10-18

### 修复(API/网关)

- API 与网关显式关闭 Gin 的 `RedirectTrailingSlash`/`RedirectFixedPath`，与 TS 路由逐位对齐：`/healthz/`、`/n/configs/`、`/v1/models/` 等尾斜杠路径返回 404，不再 301 去斜杠。
- 补回归测试：API `/healthz/`、`/n/configs/`；网关 `/healthz/`、`/v1/models/`。

## [0.20.2] - 2026-10-18

### 清理

- 删除重写遗留的无消费方 TS：`packages/contracts` 中仅保留 console 消费的 `credits.ts`（zod 依赖一并移除），删除 provisioning/platform-auth/client-configs/model-selection/release/index；`scripts/reference/` 与两个 fixtures 导出脚本删除（互操作向量与契约 fixtures 转为冻结测试资产，对齐测试继续生效）。
- Go 测试提示与 AGENTS/README 同步更新：契约演进以客户端仓库 schema 为准同步 `server/internal/contracts` 与冻结 fixtures。

### 文档

- README 组成表与关键契约段同步上述边界。

## [0.20.1] - 2026-10-18

### 文档

- AGENTS.md / README.md 全面更新为 Go 时代：仓库结构（server/ + console + contracts）、容器化工具链（本机无需 Go、GOPROXY 口径）、命令表、Redis 边界、conformance 测试环境变量、时区语义修正（usage from/to 按 UTC 解析、series 按上海墙钟分桶）。
- `.env.example` 增加可选 `REDIS_URL` 说明；`.env.production.example` 同步（compose 整栈自动注入）。
- fixtures 重新生成所需的 TS 参考实现保留在 `scripts/reference/`（crypto/passwords/pricing-doc），导出脚本已切换引用。

## [0.20.0] - 2026-10-18

### 变更(后端 Go 重构 M4:切换与清理)

- **后端切换完成**:api / gateway / migrate / seed 全部由 Go 二进制承接,镜像 target(api/gateway/tools)与 ghcr 镜像名、compose 服务名与端口保持不变,服务器部署链路零改动。
- Dockerfile 改为 Node(构建 console)→ Go(构建四二进制)→ alpine 运行时的三段结构;运行时镜像内置 ca-certificates/curl(健康检查)。
- docker-compose:api 注入 `REDIS_URL=redis://redis:6379/0`(共享限流/事件总线)并依赖 redis 健康;migrate/seed 改跑 Go 命令;健康检查统一走 `/healthz`。
- 迁移与种子目录移动:`packages/db/migrations` → `server/migrations`,`apps/api/seeds` → `server/seeds`;migrate/seed/api 默认路径同步。
- 本地开发:`pnpm dev` 后端改为 `make -C server run-*`(golang 容器内 go run、端口映射回宿主、DSN 自动改写 host.docker.internal),console 仍由 vite 启动;`pnpm dev:memory` 语义不变;首次编译约数十秒(构建缓存卷命中后数秒)。
- 删除 TypeScript 后端实现:`apps/api`、`apps/gateway`、`packages/db`(契约镜像与 fixtures 生成所需的三个 TS 参考实现保留在 `scripts/reference/`,仅供重新导出测试向量);pnpm workspace 收窄为 console + contracts。
- CI 保持双口径:pnpm typecheck/test(console+contracts)+ Go gofmt/vet/test -race;`setup-go` 缓存 go.sum。

### 验证

- `docker compose build` → `up -d` 整栈真实冒烟:api/gateway 健康、console SPA 托管与 302、管理员登录与 teams、活动 Release、定价库、网关鉴权错误形状全部通过。
- Go 全量 `-race` 测试(含 PG/Redis conformance 与跨实现对比)绿;pnpm typecheck/test 绿。

## [0.19.0] - 2026-10-18

### 新增(后端 Go 重构 M3:控制面 API)

- `internal/apiapp`:Go 版控制面 API,镜像 apps/api 全部端点——console 管理面(auth/teams/users/releases/models/providers/定价库/配置/用量/series/设备/审计)、平台设备登录流(客户端 init/poll + 浏览器登录/授权页 + deep link)、公开配置与 Release、user-config(信封 + ETag/304)、客户端 SSE 事件。
- 会话与凭据:`mendao_session` cookie 属性逐项一致(httpOnly/SameSite=Lax/secure 派生/7 天/可选 Domain);`uuid.base36` token 形态;设备凭据 Bearer 与 x-api-key 双通道;密码重置/禁用/降级联动吊销会话与设备。
- 信封:provider 规则含 `config.access.apiKey`(设备 token),`credentials` 保持空数组;syncId 内容寻址;默认模型选择照旧。
- 定价库:内置 15KB 默认定价资产 embed,首访初始化 + 内容版本乐观锁(409);导入模型按 modelId 自动带价。
- 供应商运维:base_url 归一(剥尾部 /v1)、密钥仅存密文+hint、模型列表代理(15s 超时)、连通/推理测试(三协议探测、超时/网络/鉴权/404/429 归类、结果落库、密钥不出后端)。
- 控制台静态托管:/console 302 → /console/,SPA fallback、MIME/缓存头、路径穿越防护;`PUBLIC_CONSOLE_ORIGIN` 带凭据 CORS 预检。
- Redis 接入:设置 REDIS_URL 时登录限流与配置事件总线走共享存储,否则进程内(单实例语义)。
- `cmd/api`:启动装配(memory=演示种子+临时密钥,PG=启动即迁移+强制 PLATFORM_SECRET_KEY),启动时补种子 Release/默认模型,优雅退出。

## [0.18.0] - 2026-10-18

### 新增(后端 Go 重构 M2:模型网关)

- `internal/gwapp`:Go 版模型网关,与 `apps/gateway/src/app.ts` 逐行为对齐——三协议(anthropic messages / openai chat / openai responses)同一条管道:设备凭据鉴权 → 模型校验 → 上游解析(绑定供应商优先/回落 env) → 额度原子预留 → 透传转发 → usage 结算。
- 错误形状、状态码与文案逐字对齐;客户端平台凭据不外发;`upstreamModel` 映射;供应商密文解密(PLATFORM_SECRET_KEY 缺失/解密失败各自 500 文案);供应商家族校验与 Responses 专用供应商拒绝 chat。
- 超时语义对齐:header/总时限覆盖连接(非流式覆盖响应体),流式响应头后切换空闲看门狗;上游重定向拒绝跟随;失败/超时/中断一律释放预留并写失败账本。
- 流式:SSE 边透传边采集 usage(三协议扫描器、1MB 帧缓冲上限、坏帧 dropped 计数),结束按最终 usage 结算;客户端中断按 aborted 记录;空闲超时按 upstream_idle_timeout 记录。
- 熔断:5xx/401/403 与连接级失败计数,`PROVIDER_AUTO_DISABLE_THRESHOLD` 达阈值自动停用并透出原因;429/404 不计数;成功转发即清零。
- `cmd/gateway`:启动装配(内存模式=空库仅冒烟、Postgres 模式、SecretBox、优雅退出),与 TS 入口日志语义一致。

## [0.17.0] - 2026-10-18

### 新增(后端 Go 重构 M1:存储与横切设施)

- `internal/store`:PlatformStore 端口(55 方法)的 pgx 与内存双实现,SQL 语义逐条移植自 `packages/db/src/postgres.ts`;内存实现以值拷贝提供 PG 快照语义,修复 TS 内存库的原地引用别名陷阱。
- 一致性测试:内存套件、Postgres 套件(自建/自删 scratch 库并应用同一份迁移)、跨实现对比(明细/聚合/时区分桶/游标翻页输出现逐字节一致),含 100 并发额度预留恰好成功 50 次的 -race 临界区测试。
- `cmd/migrate` / `cmd/seed`:接管迁移器(platform_migrations 账本,与 TS 迁移结果 schema/账目完全一致)与种子(管理员、种子 Release revision 31、默认模型,二次执行幂等)。
- `internal/ratelimit`:登录限流滑动窗口,进程内与 Redis(ZSET+Lua 原子窗口)双实现;Redis 故障 fail-open。
- `internal/events`:配置变更事件总线,300ms 合并域并集广播;进程内与 Redis pub/sub 双实现(跨实例 fan-out,自建订阅不丢本实例事件)。
- `internal/platformenv`:env 文件向上查找(不覆盖已有值)、slog JSON 日志、MEMORY_DB 口径。

### 修正(TS 双实现发现的既有缺陷,Go 两实现已统一)

- 供应商熔断 `PROVIDER_AUTO_DISABLE_THRESHOLD=0` 在 TS Postgres 实现下会「首败即熔断」,与文档「0=关闭」不符;Go 两实现按文档语义修正。
- `reportUpstreamFailure` 的返回值在 TS Postgres 实现下会被历史 auto 标记污染;Go 改以「本次调用是否触发停用」判定。

## [0.16.0] - 2026-10-18

### 新增(后端 Go 重构 M1:凭据与契约镜像层)

- `internal/crypto`:SecretBox(AES-256-GCM `v1.iv.ct.tag`)、scrypt 口令哈希与 token 形态的 Go 实现,TS→Go 与 Go→TS 双向互操作验证通过(存量密文/口令可读,新写入同格式)。
- `internal/contracts`:zod 契约镜像(设备 token/flow-id、init 请求 strict 解析、登录流轮询形状、公开配置信封、Release 宽松校验含 zod issue code 文案、deep link encodeURIComponent),由 TS 导出的 fixtures 对齐测试夹紧。
- `internal/pricing`:定价文档内容版本哈希(stable 六字段固定键序),与 TS 输出逐字节一致,避免定价乐观锁误报 409。
- `internal/credits`:micro-credits 整型计价与 Math.round 语义复刻。
- `scripts/export-crypto-vectors.ts` / `scripts/export-contract-fixtures.ts`:TS→Go 对齐用例导出脚本。

## [0.15.0] - 2026-10-18

### 新增(基建)

- 新增 `server/` Go 模块骨架(Go 1.26 + Gin,`cmd/{api,gateway,migrate,seed}` 占位):后端 Go 全面重写启动,权威计划 `specs/go-rewrite.md`(本地文档)。重构期间 TS 全栈保持可运行,M4 才切换。
- `server/Makefile` 全 Docker 工具链:`golang:1.26` 容器 + 模块/构建缓存卷,默认 `GOPROXY` 走 goproxy.cn(可用环境变量覆盖);本机无需安装 Go,与服务器/CI 构建口径一致。
- CI `check` job 新增 Go 检查:setup-go 1.26 + gofmt + go vet + `go test -race ./...`。
- Dockerfile 新增 `go-builder` 多阶段(静态编译四个二进制;M4 切换时由 api/gateway/tools target 引用,本轮未被引用)。
- docker-compose 新增 `redis:7-alpine`(限流共享窗口与配置事件 pub/sub 用途,应用侧 `REDIS_URL` 未设时自动回退进程内实现)。

### 变更

- `pnpm docker:down` 一并移除 redis 容器。
- `mise.toml` 声明 `go = "1.26"`(仓库口径;本机开发走 Makefile 的 Docker 工具链,本地安装 Go 为可选)。

## [0.14.0] - 2026-10-18

### 新增（管理控制台）

- 用量页升级为请求诊断：筛选维度扩到团队/用户/模型/供应商/状态/日期且聚合联动；每条请求可打开详情弹窗（请求 ID、用户、团队、设备、模型→上游映射、命中的供应商、四类 Token、费用、延迟、错误类型、真实 IP）；趋势图保持平台口径但改为一次 series 请求按上海墙钟分桶，明细时间上海时区展示。
- 供应商页运行台化：每行可发起「测试」（留空只测连通鉴权，填模型名则真实最小推理，弹窗提示可能产生少量上游费用）；列表展示「最近测试」持久化结果与停用类型（生效 / 手动停用 / 自动停用含原因与连败数），自动熔断后可一键手动恢复。
- 定价库乐观锁：保存携带版本，他人先改时提示「已被他人修改」并自动刷新最新值，未保存编辑明确丢弃。
- 新增「审计日志」页（组织分组）：管理动作时间倒序、动作中文化、目标与详情 JSON，游标加载更多。
- 团队详情新增「额度变更记录」：前后额度快照、不限额度切换、操作者邮箱。
- 界面时间统一按上海墙钟展示（新增 `time.ts`），「今日」与时间桶日界与后端 +8 解读一致；新增 UI 组件库页（UiLibrary）沉淀「计量台」令牌与控件规范（StatusChip/样式令牌扩充）。

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
