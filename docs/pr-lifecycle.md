# 拉取请求生命周期契约

该契约把一条团队习惯升级为声明式、可测试、可审计的规则：

```text
Open PR 是 merge queue，不是 backlog。

每个 Open PR 恰好属于一种生命周期状态。

一个不再是 merge candidate 的 PR 离开 merge queue，并且在离开之前，它的工程上下文先被保留下来。
```

这是第三个治理维度，与**发布健康**、**分支契约**并列。发布健康回答*已发布的产物是否可信*；分支契约回答*这个分支是否基于生产*；生命周期契约回答*它是否还是 merge candidate*。与分支契约一样，生命周期结论被隔离在它所描述的那个 PR 上——它永远不会把整个仓库标成 `BROKEN`。

## 与仓库状态隔离

治理状态与仓库状态是两件事。一个仓库唯一的 Open PR 被 park 了，并不等于这个仓库坏了；它只意味着这个仓库有一个被 park 的 PR。

| 维度 | 回答的问题 | 结论的影响范围 |
|---|---|---|
| 发布健康 | 已发布的产物是否可信？ | 该次发布 |
| 分支契约 | 这个分支是否基于生产？ | 该 PR |
| PR 生命周期 | 它是否还是 merge candidate？ | 该 PR |

## 状态

| 状态 | 含义 | 是否离开队列 |
|---|---|---|
| `RELEASE` | 受管的发布型 PR（release-please、dependabot，或其它被豁免的分支/作者）。它属于 *publish* queue。 | 否 |
| `KEEP_OPEN` | 人工打了豁免标签。唯一的人工逃生口。 | 否 |
| `ACTIVE` | 仍在推进：CI 正在跑，或在窗口内有过更新。 | 否 |
| `MERGE_READY` | 全绿且可合并，等待人工处理。 | 否 |
| `OBSOLETE` | 已经没有可合并的内容：提交已在 base 中。 | 是 |
| `PARKED` | 已暂停：仍是 draft，或存在冲突，且超过窗口没有活动。 | 是 |
| `ATTENTION` | 需要人工决策，但自动处理不安全。 | 否 |

`ATTENTION` 的存在是为了让证据不完整时退化为「问人」，而不是退化为「关 PR」。一个 CI 失败且一个月无人问津的 PR，正是自动化绝对不该替人做判断的场景。

### 规则链就是契约

规则按以下顺序依次生效，顺序本身即为契约：

1. 命中的**分支**豁免 → `RELEASE`
2. 命中的**作者**豁免 → `RELEASE`
3. 命中的**标签**豁免 → `KEEP_OPEN`
4. CI 仍在运行 → `ACTIVE`
5. 仍在无活动窗口内 → `ACTIVE`
6. 相对 base 零提交，或零变更文件 → `OBSOLETE`
7. 全绿且可合并 → `MERGE_READY`
8. 仍是 draft，或存在冲突 → `PARKED`
9. 其余情况 → `ATTENTION`

豁免优先于一切规则，「仍在推进」优先于「已完成」，而「已经落地」优先于「已可合并」——一个已经没有内容可合并的 PR 根本不是一个 merge candidate。

规则 6 有两种形态，这是刻意的。squash 合并可能留下 `ahead_by > 0` 但 diff 为空的情况：提交身份不同，内容却已经落地。同时比较**变更文件**而不仅是提交计数，才能捕捉到它。

## 三个安全不变量

ReleaseGraph 永不：

1. **自动 merge 普通 PR。** 合并是人的决定。动作集合里根本没有 merge。
2. **force-push 或 rebase。** 历史永不被重写。
3. **删除 PR 分支。** 离开队列的 PR 会把分支留下，工作因此始终可达。

被允许的写操作恰好三种，且每种都是能完成该动作的最窄端点：

| 动作 | 端点 |
|---|---|
| `ensure_issue` | 创建 issue（或复用已存在的那一个） |
| `comment` | 在 PR 上留言解释 |
| `close_pull_request` | 关闭该 PR |

增加动作是一次需要刻意评审的变更：这个封闭集合由测试断言，`merge` 或 `delete_branch` 不可能意外出现。

## 为什么先归档再关闭

一个 Open PR 常常是「工作为何暂停」的唯一持久记录。把它当作「陈旧」直接关掉，等于销毁这段上下文。

因此 `PARKED` 的 PR 总是**先**归档为一个 issue，然后才被关闭。issue 中记录原 PR、分支、最后一个提交、当时的 base、分类原因、无活动时长，以及「如何恢复」一节。PR 本身则留下一条指向该归档的评论。

归档是幂等的：issue 正文带有可 grep 的标记（`<!-- releasegraph:parked-pr:<n> -->`），所以第二次运行会复用该 issue，而不是产生重复项。

`OBSOLETE` 不建归档 issue 就直接关闭，因为没有什么可恢复的：变更已经落地。它仍会收到一条解释性评论，让这次关闭在事后依然可理解。

Policy 校验强制这一配对：只要 `closeParked` 为 `true`，`archiveParkedToIssue` 就必须为 `true`。关闭 parked 工作却不归档，是配置错误，不是可选项。

## Policy 结构

在仓库的 `.release-policy.yml` 中声明：

```yaml
repository:
  pullRequests:
    lifecycle:
      parkedAfterDays: 7        # 无活动窗口；1..365
      archiveParkedToIssue: true # 只要 closeParked 为 true 就必须为 true
      closeParked: true          # 归档之后，parked 工作离开队列
      deleteBranch: false        # 必须为 false；没有 opt-out
      exempt:
        branches:                # 由他处管理（glob 模式）
          - "release-please--*"
        actors:                  # 由他处管理（大小写不敏感）
          - "github-actions[bot]"
          - "dependabot[bot]"
        labels:                  # 人工逃生口
          - "keep-open"
```

字段省略时的默认值：

| 字段 | 默认值 |
|---|---|
| `parkedAfterDays` | `7` |
| `archiveParkedToIssue` | `true` |
| `closeParked` | `true` |
| `deleteBranch` | `false`（且不允许设为 `true`） |
| `enabled` | `true` —— 声明该块即启用 |

这个维度是**可选接入**的：没有声明 `pullRequests.lifecycle` 的仓库只被分类用于报表，任何操作都不会发生。这也是只读的 fleet audit 可以安全地在所有仓库上运行的原因。

声明 `enabled: false` 则保留声明用于审计，同时关闭强制执行。

### 为什么是 7 天，以及为什么不是「陈旧就关」

窗口刻意偏短：深度到 30 天的 merge queue 已经不是队列了。但决定状态的从来不是窗口本身——没有任何 PR 仅因为「老」就被关闭。只有当它**可证明已经不在队列中**时才会被关闭：要么变更已经落地（`OBSOLETE`），要么工作被其自身状态明确暂停（是 draft 或存在冲突，即 `PARKED`）。仅有「老」这一个事实，只会得到 `ATTENTION`，即交给人工。

### `keep-open` 是逃生口

给 PR 打上 `keep-open` 标签，契约就永不触碰它。这是刻意且永久的设计：分类是建议性的，人必须始终能够说「不，这个留着」。

如果仓库需要额外的保留标签，把它们列进 `exempt.labels`。但它无法让契约去操作一个已被打标签的 PR。

## 本地 CLI

```bash
# 只读：报告每个 Open PR 的状态，不做任何变更。
releasegraph pr-lifecycle audit

# 只针对一个仓库，而不是 fleet manifest。
releasegraph pr-lifecycle audit --repo owner/name

# 看清会发生什么，但什么也不做。
releasegraph pr-lifecycle apply --dry-run --limit 2

# 执行，每个仓库最多允许 2 个 PR 离开队列。
releasegraph pr-lifecycle apply --limit 2

# 供报表使用：把机器可读报告写成边车文件（注意不是 --output，后者是输出格式）。
releasegraph pr-lifecycle audit --format json --report pr-lifecycle.json
```

`--limit` 限制单次运行的爆炸半径。`--dry-run` 会枚举将要执行的写操作（包括会创建的归档 issue），但不执行。

退出码遵循既有契约：scope 违规为 `NeedsReview`，provider 瞬时失败为 `Retry`，policy 错误为 `Blocked`。

## CI 接入

一个每日 scheduled workflow 在 fleet 范围内执行契约：
[`.github/workflows/pr-lifecycle.yml`](../.github/workflows/pr-lifecycle.yml)。
没有任何仓库需要自己装 `stale.yml`：一套 policy，一个分类器，一份审计记录。

- **定时**运行执行契约，并用 `limit` 限制影响范围；
- **手动**运行（workflow dispatch）默认只做审计，仅当 `apply` 为 `true` 时才动手。

定时运行的 `limit` **从 1 起步**：契约有权关闭 PR，所以最初几次定时运行按 canary 对待——每个仓库每天最多 1 个 PR 离开队列，人可以逐个核对「归档 issue 创建了吗、comment 内容对吗、PR 关了吗、分支还在吗」，确认后再把 `limit` 提到常态值。

## 权限

生命周期检查使用它自己的 GitHub App installation token，由 workflow 在每次运行时以「能工作的最小权限」创建：

| 权限 | 级别 | 用途 |
|---|---|---|
| Metadata | read | 对 app token 而言始终只读 |
| Contents | read | 读取 `.release-policy.yml`；比较 head 与 base |
| Pull requests | write | 在 PR 上留言、关闭 PR |
| Issues | write | 创建并复用归档 issue |
| Actions | read | workflow runs 与 jobs |
| **Checks** | **read** | 提交的 **check runs** 属于 Checks API |

`checks: read` 与 `actions: read` 不是一回事。缺少它，每个 PR 看起来都「没有任何 CI」，于是全绿的 PR 永远无法被判定为 `MERGE_READY`，最终会因「无活动」老化成 `PARKED`。两者并存，是因为它们背后是两个不同的 API。

这里刻意**没有** `contents: write`、没有 `administration`、没有 `secrets`：该检查无法删除分支、无法重写历史、也无法修改仓库设置。而报表自身的审计使用权限更窄、完全只读的 token。

## Fleet 报表

`releasegraph fleet audit` 把生命周期维度与其它维度并列输出：

```text
Repository                Release       Branch Contract   PR Lifecycle
owner/name                v1.4.11       enforced          3/5 (-2)
```

`3/5 (-2)` 读作：5 个 Open，3 个仍在 merge queue 中，2 个即将离开。单看 Open 数会被读成 backlog，所以这一列刻意表达队列而不是计数。

没有声明契约的仓库显示 `—`，与缺失的分支契约完全一致：未声明的维度不是一个结论。分类结果由 `releasegraph pr-lifecycle audit --report pr-lifecycle.json` 产出，渲染后的字段归 `release_infra` 所有，因此同一份结论不会被渲染两次。

## 恢复

如果某个 PR 被误关：

1. 分支仍然存在——没有任何东西被删除；
2. 如果它原本是 `PARKED`，归档 issue 仍然打开并保存着上下文；
3. 重新打开该 PR，或从该分支新开一个；
4. 如果它不应再次被分类，打上 `keep-open` 标签。

这个契约没有任何破坏性动作，所以恢复是「重新打开」，而不是「找回」。
