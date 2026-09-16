# 生产操作分支契约

该契约把一条团队习惯升级为声明式、可测试、可审计的规则：

```text
开发分支允许堆叠。

生产操作分支必须直接以配置的生产基线为 PR base，并直接派生于该基线当前 HEAD。

生产操作分支不得依赖尚未合并的功能分支。

生产基线前进后，尚未合并的生产操作分支必须先更新/重建到新的生产基线，才能合并。
```

## 开发分支与生产操作分支

堆叠开发完全合法，不受影响：

```text
main
  \
   feat/provider
        \
         feat/provider-tests
              \
               feat/provider-docs
```

生产操作分支（匹配 `chore/cutover-*`、`ops/*`、`release/*`、`hotfix/*` 或仓库声明的其他模式）不是开发工作的延续。它表达的是一次**生产状态转换**——例如 `EXECUTION_PROVIDER=github → fly`——因此其父状态必须是当前生产基线，而不是某个尚未合并的功能状态。

## 不变量

对 head 分支命中生产操作模式的 PR：

1. **base 目标** — `PR base == 配置的生产基线`。
2. **祖先关系** — `git merge-base(head, base) == git rev-parse(base)`。
3. **变更范围**（可选，repo policy 声明）— 变更文件必须落在该 operation 声明的 `allowedPaths` 内。

判定只基于 **git 拓扑**（`merge-base` / `rev-parse`），绝不使用 commit message、PR 标题、patch-id 或内容相似度。

## 为什么 squash merge 会破坏堆叠的 cutover

```text
A---S        main   （S = feature B+C 的 squash）
 \
  B---C      feat/provider
       \
        D    chore/cutover-...
```

`content(S) ≈ content(B+C)`，但 commit 身份不同：`merge-base(D, main) = A` 而不是 `S`。因此契约拒绝 `D`——即使代码看起来一样。生产转换必须显式基于某个生产 commit，而不是"碰巧内容相同"。

同一规则也意味着：main 在 PR 打开后前进（有新 commit 合入），打开的生产 PR 会变红——该分支不再派生自当前生产基线 HEAD，必须先刷新再合并。这是故意的。

## Policy schema

在仓库 `.release-policy.yml` 中声明：

```yaml
repository:
  git:
    productionOperations:
      base: default        # "default" 解析为仓库默认分支；
      branches:            # 或显式 ref：main、master、prod……
        - "chore/cutover-*"
        - "ops/*"
        - "release/*"
        - "hotfix/*"
      requireLatestBase: true   # 必须为 true；没有逃生口
      operations:          # 可选的变更范围声明
        cutover:
          branches:
            - "chore/cutover-*"
          allowedPaths:    # glob 模式；"**" 跨目录
            - "fly/*.toml"
            - ".github/workflows/**"
```

`allowedPaths` 是 repo 级可选能力，ReleaseGraph core 不硬编码任何仓库的路径。未知字段与非法 glob 都是配置错误。

## CI 集成

ReleaseGraph 提供规范的 reusable gate：
[`.github/workflows/reusable-branch-contract.yml`](../.github/workflows/reusable-branch-contract.yml)。
消费仓只保留极薄 caller，并 pin 到不可变 ref：

```yaml
name: Branch contract
on: [pull_request]
permissions:
  contents: read
jobs:
  branch-contract:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-branch-contract.yml@0b0c28990abac59aa5ae1d2c50bf95ce06d26a8d # ReleaseGraph v1.4.11
```

人类可读的版本 tag 可以出现在注释里，但生产 caller 必须用完整的 40 位 commit SHA 固定 reusable workflow。

行为：

- 普通 feature PR 直接跳过（零开销，不会因 ancestry 更新频繁失败）；
- 违约的生产操作 PR **hard fail**，并附上 diff proof（变更文件、commit 数、base/head/merge-base SHA）；
- gate 绝不 rebase、force-push 或改写生产分支。

GitHub branch protection 继续负责 required checks、合并权限与评审要求；branch contract 与之互补，负责 ruleset 无法表达的 ancestry 判定。

## 本地 CLI

CI 与本地共用同一核心评估器：

```bash
releasegraph branch-contract check --head chore/cutover-provider-fly --base main
```

可选便捷命令（不是 enforcement）：

```bash
releasegraph branch-contract new chore/cutover-provider-fly
# fetch 生产基线 → 校验 clean worktree → 从远程基线精确 HEAD 创建分支
```

## 失败恢复

gate 失败时，默认重建转换，而不是 rebase：

1. `git fetch origin main`
2. 从基线精确 HEAD 创建全新分支
   （`releasegraph branch-contract new chore/cutover-<topic>`）
3. cherry-pick 或重新应用**仅**本次预期的生产转换
4. 校验 diff
5. 替换或更新该 pull request

cutover 分支理应非常小，重建比回放历史更便宜也更安全。
