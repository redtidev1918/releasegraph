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
