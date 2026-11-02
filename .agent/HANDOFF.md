# 会话交接（ReleaseGraph）

## 2026-10-29 `.agent/` 引导 + #91 现场核验（Asia/Shanghai）

范围：建立 `.agent/` 六件套（用户指令）；对 issue #91 做只读取证。
未改引擎、未推送、未 dispatch 工作流、未触碰任何 release 状态。

### 现场事实

- 起点 `main@d4264aa`，与 `origin/main` 同步，工作区干净；本轮仅新增
  本目录六文件（未提交）。
- Issue 队列全量：#91 OPEN（2026-09-26，无评论无 label）、#85 CLOSED。
  #91 详证见 issue 正文：TelePost v2.64–2.67 tag 指向直接发版提交
  （如 v2.67.0→`3c7a58b`，`gh api .../commits/3c7a58b/pulls` 为空），
  引擎期望被推算成 PR #240 合并提交（v2.64.0 期望 `d22a82a`，
  tag 实指 `d2e9bda`）→ `RepairPlan` unsafe，永不自愈。
- 本地复现尝试：`GITHUB_TOKEN=$(gh auth token) go run ./cmd/releasegraph
  provider inspect --repo redtidev1918/TelePost` →
  `FLEET_CREDENTIAL_REQUIRED`（exit 40）。符合 AGENTS.md 跨仓需
  `RELEASEGRAPH_FLEET_TOKEN` 的设计；本地未持有该凭据。
- 代码取证：#91 之后已有 `3e7ad7a`（2026-09-30）、`f77f8d4`（2026-10-07）、
  `f855d3e`（2026-10-09）三笔 provider 修复；当前归因链在「无 release PR」
  时把期望提交回退到 Release 元数据 commit（`releaseplease.go:151-157`），
  理论上覆盖直接发版形状 —— 但这**不是**核验，TelePost 实际收敛与否
  未确认（见 LESSONS L-1：仪表盘 HEALTHY 无证明力）。
- 仪表盘（fleet-audit 2026-10-29）：15 仓全 HEALTHY；TelePost 最新
  v2.84.9，Release 含 `RELEASE-METADATA.json` 资产（status.json）。

### 下一位 Agent 的入口

1. RG-02：经控制仓 `provider-operations.yml` dispatch（`apply=false` 先行）
   拿 TelePost 的 provider 裁决；或人工提供 fleet 凭据后本地跑
   `releasegraph provider inspect --repo redtidev1918/TelePost`。
   禁止把本地 `gh` 登记记为 fleet token。
2. 结果分流见 PLAN 第 3 步与 TASKS RG-03：仍冲突 → 修归因/降级诊断
   （动 `internal/provider/`，走 worktree + 全量检查 + PR）；已收敛 →
   附证据关 #91。
3. 引擎若改动：`go vet ./... && go test ./...` + Python 套件 +
   `git diff --exit-code -- dist/release/RELEASE-METADATA.json`；
   套件弄脏该文件时恢复而非提交。

### 本轮改动清单

仅新增 `.agent/{STATUS,PLAN,TASKS,DECISIONS,HANDOFF,LESSONS}.md`；
用户授权后以 docs 提交（单提交单关注点）推送至 `origin/main`。
无其他文件改动；推送前按 AGENTS.md 跑完三查，
`dist/release/RELEASE-METADATA.json` 无差异入库。

## 2026-11-01 转向社区方案试用

用户在实现途中明确要求优先试用 buluma/github-monitor，ReleaseGraph 保持已有发布治理职责。已停止自建监控开发。

- 独立 worktree：`/Users/hezzn/.codex/worktrees/f2e9/worktrees/releasegraph-fleet-pr-health`，分支 `codex/fleet-pr-health`。
- 编码前计划提交 `39eeacb`；未推送、未创建 PR、未变更 GitHub release/PR/Issue。
- 工作区留有未完成 Go 原型（GitHub read/query retry、prhealth scan/alert），不能视为可交付功能；尚无完整测试、CLI、workflow 或真实告警验收。
- 基线：`/tmp/releasegraph-pr-health-evidence/baseline.json`，15 个 enabled 仓库、31 个 open PR；使用普通 gh 只读盘点，不代表 fleet App 权限已验证。
- 后续试用独立社区工具；保留上述工作区以免丢失上下文，不合入主线。

## 2026-11-02 远程执行与关机交接

本节是当前快照；上方 2026-11-01 条目是历史过程，不再代表分支或 PR 的现状。
本轮相关本地 worktree 和起始 checkout 的 `git status --porcelain` 均为空；
没有需要本机持续运行的关键进程。未完成的 TelePost 文档同步原型保存在
`9f898c8e3c6fb74a8bf6354b19fec20b3cf9758d` stash 与独立离线补丁证据；
未当成已提交、已推送或已通过 CI 的工作。

| Repository | Branch / Commit（PR Head） | PR | CI / 被验证的 Head | Execution / Next Action / Blocker |
| --- | --- | --- | --- | --- |
| docsite | `feat/download-page-pr-flow` / `8aefad2` | [#17 merged](https://github.com/redtidev1918/docsite/pull/17) | 合并后 `main@75621ccb` 的 [navcheck](https://github.com/redtidev1918/docsite/actions/runs/41429093048) 失败 | GitHub Actions；修正 NekoTime 生成页的上游 navcheck，再用 PR 验证；当前阻断 |
| DAViewer | `codex/fix-sdk-candidate-trust` / `ce0f2b6`；`codex/sync-docsite-template` / `8c10b58` | [#150 merged](https://github.com/redtidev1918/DAViewer/pull/150)、[#151 merged](https://github.com/redtidev1918/DAViewer/pull/151) | PR 已合并；后续部署未在本次核验 | GitHub Actions；不依赖本机；无待推送本地改动 |
| DeviantDrop | `codex/sync-docsite-template` / `837fc05` | [#76 merged](https://github.com/redtidev1918/DeviantDrop/pull/76) | PR 已合并；后续部署未在本次核验 | GitHub Actions；无待推送本地改动 |
| deviantart-downloader | `codex/sync-docsite-template` / `6764565` | [#60 merged](https://github.com/redtidev1918/deviantart-downloader/pull/60) | PR 已合并；后续部署未在本次核验 | GitHub Actions；无待推送本地改动 |
| PixivFlow | `codex/sync-docsite-template` / `ef44f05`；另有旧 `pr-243` worktree | [#245 merged](https://github.com/redtidev1918/PixivFlow/pull/245)；[#243 merged](https://github.com/redtidev1918/PixivFlow/pull/243) | #243 当前 Head `8fbcb60`，所列业务检查通过；#245 已合并，后续部署未核验 | GitHub Actions；无待推送本地改动 |
| NekoTime | `codex/sync-docsite-template` / `76577f9` | [#30 merged](https://github.com/redtidev1918/NekoTime/pull/30) | [docs-check `09abb373`](https://github.com/redtidev1918/NekoTime/actions/runs/41289010196) 与 [Pages `7b7ea3bd`](https://github.com/redtidev1918/NekoTime/actions/runs/41288825784) 成功；上游 navcheck 仍失败 | GitHub Actions；需修正上游检查；无待推送本地改动 |
| TelePost | `pr-379` 本地 `4782ece`（历史 checkout） | [#379 merged](https://github.com/redtidev1918/TelePost/pull/379) | PR 最终 Head `162fdde` 所列必需检查通过；未完成模板同步未提交 | GitHub Actions；保留 stash/patch，不把原型当远程任务 |
| releasegraph | `codex/fleet-pr-health` / `f93fc76` | [#117 merged](https://github.com/redtidev1918/releasegraph/pull/117) | [定时扫描](https://github.com/redtidev1918/releasegraph/actions/runs/41408421249) `f81d029` 为 `DEGRADED`，Artifact 已上传 | GitHub Actions；[Issue #120](https://github.com/redtidev1918/releasegraph/issues/120) 持续记录；能力缺口待核实，非全绿 |

远程入口：ReleaseGraph `pr-health.yml` 支持 `workflow_dispatch` 和定时运行；
`provider-watchdog.yml`、`fleet-audit.yml` 的近期定时运行均在 GitHub 完成。
上述失败有 Actions Job 日志，Fleet Health 有 JSON Artifact 和 Issue #120；
重复故障在同一 Issue 更新，不需本机进程。没有开启自动合并。

关机判定：本地有效改动保存后可以关机；远程的 `DEGRADED` 和 docsite
navcheck 失败不会因关机消失，也不能宣称已修复。需要人工核对 ReleaseGraph
App 权限与 ParaNote 私有仓 branch-protection API 的套餐 403；随后通过远程
PR 验收修复。若本交接 PR 的 CI 尚未结束，只标记为等待，不标记为通过。
