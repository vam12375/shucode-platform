# 视频生产验证记录

日期：2026-10-20。代码已实现；真实网站和付费供应商验收未完成。配置步骤见同目录 SETUP.md，产品／状态／接口边界见 CONTRACT.md。

统一平台账号接入回归：Mendao 的 OAuth／视频 service、旧 OAuth 行为、登录页、启动门禁及视频 React 页面共 12 个测试文件、190 项通过。平台 `GET /api/v1/platform/account` 复用设备认证，不要求团队；无凭据／吊销凭据 401、无团队账号 projection 由 API media 测试覆盖。真实 Electron pending VIDEO-01／VIDEO-07 各 1 项通过：匿名统一登录入口与返回重开、统一账号授权后进入制作记录、轮询保持播放源、真实 Host 导出字节一致且媒体下载不携带设备 Authorization。

## 已执行

| 范围                                                                             | 实际结果                                                                                                         |
| -------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------- |
| Mendao `pnpm typecheck`                                                          | 通过                                                                                                             |
| Mendao `pnpm lint`                                                               | 通过，63 条既有 warning，0 error                                                                                 |
| Mendao `pnpm architecture:check --changed`                                       | 通过，0 violation；UI/services 未全部纳入受控模块，不能据此推断运行时正确                                        |
| Mendao 四个 videoProduction／videoAssetPlayback／videoAssetSelection Vitest 文件 | 27 项通过：输入、冻结 wire、授权隔离、异步切换、分析绑定／恢复、gateway 合并、播放源、真实文件导出与自动素材意图 |
| Mendao `typecheck:e2e`                                                           | 通过，包含两个 pending Electron 场景                                                                             |
| Mendao Electron pending VIDEO-01／VIDEO-07                                       | 各 1 项通过；统一登录回跳、播放源连续性与真实 Host 文件导出                                                      |
| shucode-platform `pnpm typecheck` / `pnpm lint`                                  | 通过                                                                                                             |
| shucode-platform `pnpm test`                                                     | contracts 4 项、console 8 项通过                                                                                 |
| Go `-race ./...` + 临时 Postgres/Redis                                           | 通过；统一额度 suite 包含长时媒体 reservation 不被 TTL 回收；使用 UTC 对齐现有跨存储时间序列                     |
| 最新本地 `go test -race ./internal/media -count=1 -v`                            | 15 项通过，无跳过；包含实际 FFmpeg 合成、已知成功 URL 的视频下载恢复、同团队商品自动匹配与幂等冻结               |
| 最新 Docker `make -C server lint test`                                           | 全量 gofmt／vet／race suite 通过；媒体合成在容器中因无 FFmpeg 跳过，已由上行本地测试覆盖                         |
| Alpine 3.21 运行依赖                                                             | 实装确认 FFmpeg/FFprobe、libass subtitles、libx264、AAC 与中文字体可用                                           |
| 两仓 `git diff --check`                                                          | 通过                                                                                                             |
| `node scripts/check-workspace-freshness.mjs`                                     | 本轮通过，当前 main 与 origin/main 同步                                                                          |

媒体场景包含未知提交不重发、余额不足不提交、提交前取消、受理后取消与成功结算、部分失败复用成功镜头、图片／视频下载重试不重生成、root 核对、团队模型策略。

Host 导出测试实际写入并比对文件，覆盖无 Authorization 下载、禁止跨源／重定向、重复导出不覆盖已有文件、流中断清理临时文件。分析恢复覆盖原会话补充登录／继续指令、旧计划不覆盖新输入、明确 rejected／stale admission 结束分析及未知 admission 不盲目重发。

自动素材检索先以失败测试复现缺口，再实现平台匹配和冻结。覆盖完整商品标签优先、标签／文件名回退、大小写与首尾空格、最多 20 张、按时间排序、图片类型／团队隔离、手动覆盖、无匹配不创建／不付费、同 requestId 不因后来上传而更换素材。客户端创建允许空 assetIds，制作单投影强制非空；Host 使用平台返回的已解析素材构建 Agent 输入。

## 独立 UI 浏览器验证

使用真实 VideoStudio 组件，注入本地 fixture 服务，通过 Playwright CLI 操作。没有真实供应商调用，也没有把 fixture 成片当作生成结果。

已验证一键／复刻切换、商品输入、分享文案提取与去重、图片选择、模型与 1080p 选择、分镜审阅、创建参数、来源证据、分镜展示。390px 与 1440px 下无横向溢出；已目视检查默认浅／深主题及 Zai 浅／深主题截图。已完成合成视频 fixture 的 metadata 加载（readyState 4，5 秒）和导出按钮到服务的 projectId／目录传参验证。此项不覆盖 Host 的原生目录窗口或文件落盘。

本轮再次用真实 Chromium 播放组件验证：overview 计数从 7 到 16 的 9 次轮询均保持同一 video 元素和 src，5 秒视频播放至结束（currentTime 5、readyState 4），签名更新未打断播放。已验证不勾选图片直接提交自动检索意图，且保留手动选图回归。浏览器 fixture 仅验证交互；实际团队匹配和冻结由上述 Go 测试覆盖。

截图保存在本机 `packages/desktop/.e2e-artifacts/video-production-ui/`（测试产物，不提交）。

## Electron 运行证据

在 Node 24.14.0 下显式使用 `E2E_PROVIDER_REPLAY_FIXTURE_PATH=packages/desktop/test/e2e/fixtures/upstream/common.json`，两个 pending 场景均执行了页面断言并通过。VIDEO-07 同时配置 `MENDAO_PLATFORM_API_ORIGIN=http://127.0.0.1:18797`，由回环 fixture 提供账号授权与合成 MP4，无付费调用。

- VIDEO-01：`desktop-e2e-20261019235318644-p5030-baaf1f9324936fc8`。
- VIDEO-07：`desktop-e2e-20261019235811746-p7345-08df63e4cbc33728`。

产物位于 Mendao 桌面包 `.e2e-artifacts/<run-id>/`。完整命令见 Mendao 的 `packages/desktop/test/e2e/video-production/manual-review/pending/README.md`。此前 Node 26 的 `UND_ERR_INVALID_ARG` 及 session 建立失败不再代表最新执行结果。

## 未通过或未执行

- 两个 Electron 场景仍保留在 `packages/desktop/test/e2e/video-production/manual-review/pending/`，尚未人工审阅或晋升正式 CI。
- 已运行 `e2e:fixture:check`；当前工具只发现 conversation-session／plugins 域的 manifest，未支持 video-production 域，检查未通过。新 manifest 保留为待审阅材料，不据此声称已通过 fixture 准入。
- 未用真实抖音／小红书账号验证搜索排序、登录、视频观察或反爬限制。Agent 任务及结构化分镜通道已接入；提示约束要求实际画面证据，失败不能编造计划。排名只代表本次账号可见候选。
- 未配置生产 API 域名、Ark 模型 ID、真实密钥、价格、支付或 DNS；未产生付费模型调用。真实图像／视频生成、供应商下载公开签名素材、完整成片交付仍须按 SETUP.md 联调。
- 未验证 Windows/Linux 原生桌面、跨机器分析恢复；已有平台抽象和严格本地桌面边界不等于这些环境已经测试。

没有提交、推送或部署；保留 shucode-platform 原有本地改动。
