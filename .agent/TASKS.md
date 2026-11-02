# 任务队列（ReleaseGraph）

> 状态：TODO / IN_PROGRESS / DONE / DEFERRED / BLOCKED。每行带证据。
> ID 前缀 RG-。平台事实（PR、run）以 GitHub 为准，此处只记指针。

## 本轮（2026-10-29 `.agent/` 引导 + #91 核验准备）

| ID | 任务 | 状态 | 证据 |
| --- | --- | --- | --- |
| RG-01 | 建立本目录 `.agent/` 六件套（对齐 PixivFlow / DAViewer 约定） | DONE | 本目录六文件；STATUS 含与根 STATUS.md（生成仪表盘）的边界声明 |
| RG-02 | 用当前引擎核验 issue #91（TelePost 2.64–2.67 TAG_CONFLICT）是否仍复现 | TODO | 本地 `provider inspect` 实测 `FLEET_CREDENTIAL_REQUIRED`（无 `RELEASEGRAPH_FLEET_TOKEN`）；待 `provider-operations.yml` dispatch `apply=false`，或人工提供 fleet 凭据后本地重跑 |
| RG-03 | 依 RG-02 结果处置 #91：改裁决接受直接发版提交形状（或降为诊断项）/ 附证据关闭 issue | TODO | 依赖 RG-02；候选实现点 `internal/provider/releaseplease.go`（providerState / manifestBumpCommit）与 `internal/provider/provider.go:174`（DriftTagConflict 判定） |
| RG-04 | 消除 Python 套件重写 `dist/release/RELEASE-METADATA.json` 的测试债 | DEFERRED | AGENTS.md「已知技术债」；现行缓解是测试后 `git checkout --` 恢复，从未污染提交 |

## 历史锚点（已闭环，仅作索引）

| 事实 | 证据 |
| --- | --- |
| v1.5.19 发行 | `93d95c6` reviewed engine version；下载页 `c772222`；tag 工作流构建发布 |
| #91 之后的三笔 provider 修复 | `3e7ad7a`（2026-09-30 waiver 优先于 tag 冲突）、`f77f8d4`（2026-10-07 历史 Release 保留 + 退役被取代 PR）、`f855d3e`（2026-10-09 manifest 记账排除出归因） |
| PR 归因链重构 | Merge #110 `feat/provider-pr-attribution`；证据信号常量见 `internal/provider/releaseplease.go:200-207` |
| #85 actionlint 债 | 已关闭（2026-09-16） |

| RG-FH1 | Read-only Fleet PR health audit and classification | IN_PROGRESS | docs/pr-health.md; internal/prhealth |
| RG-FH2 | Persistent single Issue, deduplication and recovery | TODO | Depends on RG-FH1 |
| RG-FH3 | Scheduled workflow, artifacts, community/Dependabot guidance, E2E | TODO | Depends on RG-FH2; no automatic merge |

| RG-FH2 | Persistent single Issue, deduplication and recovery | IN_PROGRESS | feat/fleet-pr-health-alerts;待真实 E2E |

## 2026-11-02 关机交接（以最新远程证据覆盖上方历史状态）

| ID | 任务 | 状态 | 证据与下一步 |
| --- | --- | --- | --- |
| RG-SD1 | Fleet PR Health 云端扫描和持久化告警 | IN_PROGRESS | [run 41408421249](https://github.com/redtidev1918/releasegraph/actions/runs/41408421249)：15/15、`DEGRADED`、5 PR `UNKNOWN`；Artifact 与 [Issue #120](https://github.com/redtidev1918/releasegraph/issues/120) 已保存。核实 App 权限及私有仓套餐边界后再验收；不得把权限不足改写成 PASS。 |
| RG-SD2 | docsite 全仓导航契约 | BLOCKED | [run 41299560459](https://github.com/redtidev1918/docsite/actions/runs/41299560459) 与 [最新重跑](https://github.com/redtidev1918/docsite/actions/runs/41429093048) 均在 NekoTime 生成页误报 8 个缺失目标。修正上游检查的生成页处理并跑全仓 CI；勿放宽真实断链检查。 |
| RG-SD3 | PR/CI 云端交接 | DONE | 本轮已合并 PR 见 HANDOFF；远程 workflow 与日志不依赖本机。合并不代表所有后续运行通过。 |
| RG-SD4 | 本交接文档 PR 验收 | IN_PROGRESS | 当前分支 `feat/shutdown-handoff-20261102`；推送后记录 PR、Head SHA 和 CI 结果。 |
