# 工程状态（ReleaseGraph）

> 本仓开发/会话状态的摘要快照。平台事实（PR、CI、Release）以 GitHub 为准，
> 本文件不复制。任务进度的权威是 [TASKS.md](TASKS.md)；单次会话交接见
> [HANDOFF.md](HANDOFF.md)。

- 更新时间：2026-10-29（Asia/Shanghai）
- 当前版本：v1.5.19（reviewed engine version 93d95c6；下载页 c772222）
- 分支：`main`（引导前与远端同步于 `d4264aa`；本轮以 docs 提交将本目录
  六件套入库并推送，工作区保持干净）。
- 阶段：Python → Go 迁移中。Go 核心已承担**生产写入路径**的 provider
  reconciliation 一步（受管仓库 pin 住它）；staging / publishing / audit 等
  其余写入能力仍走 Python v1，待 Python/Go 契约 canary 通过（README「当前状态」）。

## 开放事项

- **Issue #91（OPEN，2026-09-26）**：`provider reconcile` 对 TelePost
  2.64–2.67 报 `TAG_CONFLICT` —— 这几个版本是**直接落在 main 上的发版提交**
  （`chore: release X`，无关联 PR），引擎把「期望提交」推算成 release-PR
  合并提交（PR #240），永久对不上；`RepairPlan` 判 unsafe，永不自愈。
  Issue 之后引擎已落三笔相关修复（2026-09-30 `3e7ad7a` waiver 优先于永久
  tag 冲突、2026-10-07 `f77f8d4` 保留已接受的历史 Release 并退役被取代的
  PR、2026-10-09 `f855d3e` 把 manifest 记账排除出 release 归因），当前代码
  的归因链（title → 带 Provider label 的 manifest-bump → 直接识别 pending
  label；无 PR 时 `ExpectedCommit` 回退到 Release 元数据 commit，
  `internal/provider/releaseplease.go:151-157`）**可能已覆盖该形状**，
  但尚无 fleet 权限核验 TelePost 是否已收敛 —— 验证路径与阻塞见
  TASKS RG-02。Issue 在核验前保持 OPEN。
- 已知技术债（AGENTS.md 记录）：全量 Python 测试会重写受跟踪的
  `dist/release/RELEASE-METADATA.json`；失败时 `git checkout --` 恢复，
  严禁 `git add -A` 盲提交。
- Issue #85（actionlint canary 债）已关闭（2026-09-16），无遗留。

## 当前无阻塞

- 根 STATUS.md 仪表盘（2026-10-29 生成）：15 个 fleet 仓库全部 HEALTHY。
  注意该健康来自 `release_infra/inventory.py`（workflow/资产维度），**不是**
  provider reconcile 的裁决维度，不能用来关闭 #91（见 LESSONS L-1）。

## 与本仓其他事实源的边界（唯一事实来源）

| 事实 | 权威 |
| --- | --- |
| 本仓开发任务/会话状态 | 本目录 `.agent/` |
| desired / managed 仓库清单 | [`fleet.yaml`](../fleet.yaml)（`fleet discover` 只列候选） |
| fleet 状态快照 | 根 [`STATUS.md`](../STATUS.md) / `status.json`（fleet-audit 生成，**DO NOT EDIT**；与本文件同名但不同物） |
| PR / CI / Release / Issue | GitHub |
| 分支契约 / PR 生命周期 / 认证的执行细节 | `docs/branch-contract.md`、`docs/pr-lifecycle.md`、`docs/authentication.md` |

本目录禁止复制 fleet 版本快照（会与生成的仪表盘漂移）；需要时链接查询。

## 2026-11-01 Fleet Health

Stage 1 audit implementation under test on `codex/fleet-pr-health`. Baseline and
protocol: `docs/pr-health-baseline.md`, `docs/pr-health.md`. Stages 2/3 and real
end-to-end alert acceptance are pending. Three PRs requested; none may auto-merge.

## 2026-11-01 Fleet Health 阶段二(告警)与权限降级

分支 feat/fleet-pr-health-alerts:GraphQL 拆分(branchProtectionRule 独立查询),
权限缺失时 checks 证据保留、PR 诚实 UNKNOWN;UpdateIssue 原语;单一标记 Issue
持久化(首次/最近发现、事件周期、恢复日志、幂等通知、超长拒绝、读写凭据分离)。
测试含权限降级、生命周期、事件周期、重试不重复通知。Administration 为人工阻断
(App 所有者授权),系统按降级模式持续运行。

## 2026-11-01 告警 E2E 进行中

真实标记 Issue #120 已创建(65 事件,真实失败+权限降级如实混排)。发现 pr:UNKNOWN
事件缺 Evidence 代码 → 能力缺口章节缺失;分支 feat/fleet-health-incident-evidence
补上并加披露回归。合并后下一次扫描同时验证去重(同指纹应零新通知)。

## 2026-11-01 恢复权威粒度重构

恢复不再以「整次扫描 complete」为前提(该前提在用户选择不授予 Administration 的
长期降级姿态下会永久冻结一切恢复、堆积已解决事件)。现按事件自身的观测质量判定:
仓库发现成功即权威;check 事件在其 PR 的 checks 证据完整收集时即可恢复;pr:verdict
事件需确定性判定或被完整观测的新 head 取代;观测失败的 PR 冻结自身事件,不影响别处。

## 2026-11-02 关机交接快照（GitHub 事实以链接为准）

- 本轮相关本地 worktree 已逐个检查，均无未提交文件；本交接改动在独立
  `feat/shutdown-handoff-20261102` 分支完成并须经远程 PR 验收。
- Fleet PR Health 在 `f81d029` 上定时运行：
  [run 41408421249](https://github.com/redtidev1918/releasegraph/actions/runs/41408421249)
  为 `DEGRADED`，扫描覆盖 15/15 仓库但 `complete=false`；5 个 PR 因
  `API_PERMISSION` 保持 `UNKNOWN`。JSON Artifact 已上传，单一持久化
  [Issue #120](https://github.com/redtidev1918/releasegraph/issues/120) 已更新。
  不得将此结果称为全绿。`Administration` 读权限是当前实现报告的能力缺口；
  对私有仓还需区分 GitHub 套餐限制与 App 授权，不应未经验证直接扩权。
- docsite [navcheck run 41299560459](https://github.com/redtidev1918/docsite/actions/runs/41299560459)
  在 `75621ccb` 失败于 NekoTime 8 个侧边栏目标；
  [最新重跑 41429093048](https://github.com/redtidev1918/docsite/actions/runs/41429093048)
  仍失败。NekoTime 的 `prepare-docs.mjs` 会生成这些被忽略的 `docs/` 页面；
  [docs-check](https://github.com/redtidev1918/NekoTime/actions/runs/41289010196)
  和 [Pages 部署](https://github.com/redtidev1918/NekoTime/actions/runs/41288825784)
  在相关提交成功。上游 navcheck 未运行生成步骤，故此检查尚未修复，
  不能把导航契约报告为通过。
