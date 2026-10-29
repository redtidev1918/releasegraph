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
