# 核心概念

**语言 / Language:** 中文 · [English](/en/concepts.md)

## Desired 与 Actual

事件不是事实来源。ReleaseGraph 收到事件后，会重新查询 Git tag、GitHub Release、资产、workflow 和 registry，再用以下输入生成计划：

```text
Desired State + Actual State + Policy + Dependency Graph → ReleasePlan
```

## Event 与 Reconcile

标准执行模型是：

```text
Event → Reconcile → Plan → Dispatch → Exit
```

上游发布完成只会唤醒下游重新计算。下游没有新 desired version 时必须是 `NOOP`，不能自动制造 patch version。

## DAG

依赖图必须无环。独立分支可以并行，一个失败分支不能阻塞无关分支。cycle、缺失节点或非法 condition 都会让 graph validation hard fail。

## 安全发布与恢复

正式 Release 必须在测试、构建和资产检查之后公开。已有 tag 只能在指向预期 commit 时继续；错误 commit、不同 digest 的同名资产和 immutable registry 冲突都必须 hard fail。基础设施故障通过相同版本的 `repair`/`retry` 恢复，不能靠再 bump patch 绕过。

历史 Git tag 永不由 retention 删除。`status.json` 只是快照，不是数据库。
