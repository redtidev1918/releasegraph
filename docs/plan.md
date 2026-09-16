# 计划契约

一个计划只回答一个问题：**这张图声明的所有东西里，哪些现在可以开始、哪些在等待、哪些根本不需要动？** 它每次都从最新状态算出、从不存储，所以不存在"过期的计划"。

`schemas/plan-v1.json` 描述的是 `.github/workflows/reusable-readonly-plan.yml` 在生产中消费的那份 JSON。正因为有这个消费方，它的形状才是契约，而不是实现细节。

## 线上格式

```bash
releasegraph plan --graph release-graph.yml --state health.json --output json
releasegraph plan --graph release-graph.yml --live --output json
```

两者形状相同，外面套 CLI envelope：

```json
{
  "schemaVersion": 1,
  "releasegraphVersion": "0.1.0-go-readonly",
  "generatedAt": "2026-01-01T00:00:00Z",
  "data": {
    "ready": ["cli", "web"],
    "blocked": { "deploy": ["cli", "web"] },
    "noop": ["core"]
  }
}
```

| 字段 | 含义 |
|---|---|
| `ready` | 依赖条件都满足、且项目不是 `HEALTHY`：可以开始 |
| `blocked` | 项目 id -> 条件未满足的依赖 id 列表 |
| `noop` | 已经 `HEALTHY`。**不会为了发布而重发一个已发布的版本** |

`testdata/plan/graph-plan.json` 是用真实二进制产出并提交的 fixture，`tests/test_plan_schema.py` 用它校验 schema。这个 fixture 就是"schema 描述的是工具真实输出、而不是某个人以为的输出"的证据。

## 范围：一个动词，两份互不兼容的载荷

`releasegraph plan` 有两种形式，载荷**互不兼容**，却共用同一个属性名：

| 形式 | `blocked` | 还输出 |
|---|---|---|
| `plan --graph`（图视图） | 对象：项目 id -> 阻塞它的 id | — |
| `plan --path`（仓库计划） | id 数组 | `nodes` |

`schemas/plan-v1.json` **只**描述图视图，因为只有它有生产消费方。它被刻意收窄：`RepositoryPlanIsOutOfScopeTest` 会把一份仓库计划的载荷喂进去并要求校验**失败**，所以这条范围边界是被强制的，不是写在散文里的。等仓库计划需要契约时，它单独拥有一份 schema —— 一份 schema 无法诚实地同时覆盖两份对同一字段类型有分歧的载荷。

## state 输入

`--state <file>` 把项目 id 映射到该节点的当前状态。它接受**完整的** `internal/domain.Health` 词表，比 [发布健康](health.md) 里的仓库健康词表更宽：

```
HEALTHY READY RUNNING BLOCKED RECOVERABLE DEGRADED BROKEN NOOP
UNMANAGED NO_RELEASE NEEDS_REVIEW ACK_PENDING WAIVED
```

这里更宽是对的，因为这个文件描述的是**节点**状态，而节点可以是 `READY` 或 `ACK_PENDING` —— 作为仓库健康它们是说不通的。`testdata/graph/diamond-health.json` 用的就是 `READY`，所以被测试的是更宽的那套。

只有 `HEALTHY` 意味着"不需要动"。其他任何值 —— 包括根本不在这份列表里的值 —— 都会让该项目成为待计划项：加载器不做校验，所以无法识别的值会**计划工作**，而不是被静默跳过。这是失败时安全的那一侧。

## 失败怎么读

JSON 模式下计划失败时 stdout 是**空的**：退出码非零，stderr 上是人可读的文本（如 `ERROR GRAPH_ERROR: cycle: a -> c -> b -> a`）。所以工作流里的 `cat release-plan.json` 永远不会展示一份写了一半的文档。

envelope 类型还声明了一个 `error` 字段。**今天没有任何命令设置它**，schema 在该字段自己的 description 里就这么写，而不是暗示一份并不存在的契约。一旦有命令开始输出它，`test_plan_schema.py` 会失败，于是散文必须和代码一起改。
