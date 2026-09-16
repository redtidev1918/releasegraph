# ReleaseGraph 中文文档

ReleaseGraph 是一个面向 GitHub Actions 的无服务器、多仓库发布编排器。

它读取期望版本、GitHub 与 registry 的实际状态、项目发布策略和依赖 DAG，判断哪些项目可以发布、哪些被阻塞、哪些可以原版本恢复。每次运行都在 GitHub Actions 中完成并退出，不需要常驻服务器或中央数据库。

**语言 / Language:** 中文 · [English](/en/) · [GitHub 仓库](https://github.com/redtidev1918/releasegraph)

## 从这里开始

- [快速开始](quick-start.md)：先以只读方式生成 live plan。
- [核心概念](concepts.md)：Desired/Actual、事件、DAG 与恢复语义。
- [认证与权限](authentication.md)：审计、单仓库发布、跨仓库编排三级权限。
- [如何调用发布工作流](callers.md)：调用方接口、输出与冻结的 `release_health` 契约。
- [发布健康](health.md)：一套词表、原因码，以及"健康不是动作"。
- [计划契约](plan.md)：`plan --graph` 的线上格式与它的边界。
- [生产操作分支契约](branch-contract.md)：cutover/release/hotfix/ops 分支必须直接派生自当前生产基线 HEAD，及其 reusable CI gate。
- [英文架构文档](/en/ARCHITECTURE.md)
- [英文 Policy 参考](/en/POLICY.md)
- [英文恢复说明](/en/RECOVERY.md)

当前 Go 核心仍处于只读 canary 阶段；Go 写操作、registry 检查与 dispatch 尚未标为稳定能力。

## fleet 观测快照

本仓库是 release control / fleet repository。除了 desired / managed 清单
[`fleet.yaml`](https://github.com/redtidev1918/releasegraph/blob/main/fleet.yaml) 之外，
还**有意版本化提交**两个生成物快照：

| 文件 | 用途 | 生成方式 |
| :-- | :-- | :-- |
| [`STATUS.md`](https://github.com/redtidev1918/releasegraph/blob/main/STATUS.md) | 人类可读的 fleet 状态快照 | `.github/workflows/fleet-audit.yml` 运行 `release_infra/inventory.py` |
| [`status.json`](https://github.com/redtidev1918/releasegraph/blob/main/status.json) | 机器可读的同源快照（含 `schema_version`） | 同一次运行，与 `STATUS.md` 时间戳一致 |

它们**不是手工配置**，也**不改成纯 CI artifact**：提交入库让 git 历史天然成为 fleet 状态
的变化记录，可以回溯「某个仓库何时从 `NEEDS_REVIEW` 变成 `HEALTHY`」。workflow 只在状态
变化时提交，不会每次运行都产生 commit。

职责边界：

```text
fleet.yaml   = desired / managed inventory（唯一事实源）
STATUS.md    = generated human snapshot
status.json  = generated machine snapshot
```

两个文件头部都已标注 **GENERATED — DO NOT EDIT**。要改状态请改 `fleet.yaml` 或生成器，
而不是改快照。
