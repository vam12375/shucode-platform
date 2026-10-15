# 更新日志

本文件记录 shucode-platform（Mendao 平台）的重要变更。

格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

本仓库为独立平台服务（控制面 + 模型网关 + 管理控制台），与 Shucode 客户端仓库分开发布；客户端接入方案见 Shucode 仓库 `specs/platform/multi-tenant-saas.md`。

## [0.1.0] - 2026-10-15

### 新增

- 工程基线：pnpm workspace（Node 24 / pnpm 10）、共享 TypeScript 配置、`.env.example` 环境样例、本地 Postgres 的 docker-compose、mise 版本固定。
- `packages/contracts`：与 Shucode 客户端对齐的契约——Provider Provisioning 信封（syncId 幂等 / CAS / 回滚语义复用客户端实现）、公开客户端配置信封（帮助配置 / 内置配置下载地址 / 强制更新版本号 / 插件商店排序）、平台设备登录协议（`mdp_` 设备凭据、init/poll 状态机）、信用点计算（1 credit = 10⁶ micro-credit，价格按每 1M token 的 credit 数）。
- `packages/db`：存储端口（Ports）与语义契约；Postgres 实现（postgres.js 直连 + 手写迁移，额度预留为单语句原子 CAS）；内存实现（`MEMORY_DB=1` 本地体验与测试共用）；AES-256-GCM 凭据加密（`PLATFORM_SECRET_KEY`）与 scrypt 口令哈希。
- `apps/api` 控制面（Hono）：平台设备登录全链路（`/api/v1/platform/auth/init|poll` + 登录 / 授权 / deep link 中转页，`consumeLoginFlow` 条件消费保证凭据只签发一次）；匿名公开配置 `/api/v1/client/configs` 与 `/n/configs`；全局 Release JSON 托管（`/api/v1/releases/builtin/*`）与桌面更新清单占位（返回 `version: 0.0.0` 等价无更新）；用户级配置下发 `/api/v1/client/user-config`（Bearer 设备凭据，输出 provisioning 信封：网关 provider + 团队模型 + 默认模型 + 凭据）；管理控制台 API（登录 / 团队 / 成员 / 额度 / 用户 / Release 发布与切换 / 模型目录 / 设备吊销 / 平台配置 / 用量查询）与 `/console` 静态托管；内存模式内置演示数据。
- `apps/gateway` 模型网关：Anthropic 兼容 `/v1/messages`；平台凭据鉴权 → 团队额度准入（按 `max_tokens × 输出单价` 原子预留，超限 429）→ 转发上游 → 按上游 usage 结算（预留转实扣与账本写入同事务）；SSE 流式边透传边采集 usage（`message_start` / `message_delta`），上游失败或客户端中断释放预留并记录 error/aborted。
- `apps/console` 管理控制台（Vite + React）：控制台登录、团队列表与新建、团队详情（额度调整 / 不限额度开关 / 成员增删 / 模型用量汇总 / 最近请求）、内置配置发布与版本历史切换、模型目录与价格编辑、设备凭据吊销、平台配置（帮助链接 / 社区链接 / 强制更新版本号）。
- 测试与验证：契约信用点、配额临界区（预留受限 / 结算转实扣 / 释放 / 不限额度 / 禁用团队）、控制面登录流 E2E（含吊销后 401）、Release 发布递增、网关计量（非流式 / 流式 / 额度不足 / 上游失败 / 凭据吊销）；`pnpm typecheck` 与 `pnpm test` 全绿，另有真实进程冒烟验证。
