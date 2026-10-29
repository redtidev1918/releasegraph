# 架构决策（ReleaseGraph，长期有效）

> 本文件沉淀**影响本仓开发方式**的决策与理由。规则权威在
> [AGENTS.md](../AGENTS.md) 与 `docs/`；此处是开发视角的索引与边界，
> 与权威冲突时以权威为准。

## D-01 本仓 `.agent/` 的边界（2026-10-29）

本目录只记录 releasegraph 仓自身的开发/会话状态。fleet 清单权威是
`fleet.yaml`；fleet 状态快照权威是 fleet-audit 生成的根 `STATUS.md` /
`status.json`（与本目录 STATUS.md 同名不同物，禁止手工编辑）；PR / CI /
Release 以 GitHub 为准。本目录禁止维护 fleet 版本号快照。

## D-02 Provider 状态是推导值，不是权威（源自 AGENTS.md 规则 10）

真实 GitHub / registry 状态决定健康；release-please label 只是
acknowledgement。归因不靠单一信号：title 匹配 → 带 provider label 的
manifest-bump 合并提交 → 直接识别仍挂着的 autorelease label，
单一信号无法确认时宁可 UNKNOWN 也不猜（`internal/provider/releaseplease.go`
providerState）。无 release PR（manual / tag provider 或直接发版提交）时，
期望 tag 目标回退到 Release 自身元数据记录的 commit。新增归因信号必须
保持这个「证据优先」次序。

## D-03 TAG_CONFLICT 是停止信号，不是修复项（源自 AGENTS.md 规则 5/6）

tag 指向与期望不符时：停止对该 release 的写入，核对 peeled tag commit 与
期望证据；引擎归因错了修引擎，tag 永不移动。历史版本的「接受现状」只能由
人工显式 `provider waive`，禁止补造 GitHub Release 来满足 provider。
#91 的诉求是把仓库既有形状（直接发版提交）纳入合法目标集，属于修引擎，
不属于移 tag。

## D-04 修改性会话用独立 worktree（源自 AGENTS.md 规则 2）

`scripts/agent-worktree create <task-id>`；共享 checkout 不承载修改。
纯新增、不触碰受跟踪状态的文档引导（RG-01）除外。

## D-05 引擎解析只用不可变引用（源自 AGENTS.md 规则 11）

可复用治理工作流内部从自身 `job.workflow_sha` 解析引擎，绝不从 v1/main
这类可变 channel；发布/热修/运维分支从当前 configured production-base HEAD
创建并直接指向该 base，不得堆叠在未合并 feature 分支上。执行细节以
`docs/branch-contract.md` 为准。

## D-06 四对象四职责（源自 AGENTS.md 规则 12）

Issue = backlog，branch = workspace，PR = merge queue，release PR =
publish queue。失去合并资格的 PR 先把工程上下文归档进 issue 再离队，
不删分支、不改历史；`keep-open` 是唯一永久逃生门。执行以
`.github/workflows/pr-lifecycle.yml` 为准（`docs/pr-lifecycle.md`）。

## D-07 写入路径按能力分批迁移（README「当前状态」，持续有效）

Go 核心已承担生产写入的 provider reconciliation；发布写入、repair、
dispatch、registry 发布与保留策略在 Python/Go 契约 canary 通过前保持
Python v1。不因「Go 已在」就整体切换。
