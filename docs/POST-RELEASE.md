# Post-release actions（发版后动作）

发版后动作是 **Release 发布并审计通过之后** 的可恢复、幂等步骤。Release 本身保持已发布；
只有所有 `required` 动作成功，发布事务才算完全收敛。

## 什么时候需要

典型用途：GitHub Release 落地后刷新生成的下载页、重建文档站、发通知、同步注册表索引。
触发方是 *ReleaseGraph* 而不是 `release: published` webhook，因此即使用仓库自己的
`GITHUB_TOKEN` 创建的 Release（其事件永远不会再次级联触发 workflow），链路仍然可用——
exact tag 会被显式下传，下游永远不需要猜 "latest"。

## Policy 配置

在 `.release-policy.yml` 中加 `release.postRelease` 列表：

```yaml
release:
  postRelease:
    - id: refresh-docs
      type: github-workflow
      required: true
      workflow: update-download-page.yml
      inputs:
        tag: "{{tag}}"
    - id: deploy-docs
      type: github-workflow
      required: false
      workflow: docs.yml
      ref: default # Pages 部署用默认分支
```

字段：

| 字段 | 含义 |
| --- | --- |
| `id` | 动作唯一 id（必填） |
| `type` | `github-workflow`（目前唯一 adapter） |
| `required` | `true`：该动作不成功，发布事务不算收敛；`false`（默认）：失败只记录 warning |
| `workflow` | 要 dispatch 的 workflow 文件（`type: github-workflow` 需要） |
| `inputs` | `workflow_dispatch` 入参；`{{tag}}`、`{{version}}`、`{{repo}}`、`{{release_id}}`、`{{release_url}}` 从 exact release context 替换 |

旧版单行字符串 `release.post_publish` 保持原行为不变。没有配置 `postRelease` 的仓库
行为与之前完全一致——不会被隐式 dispatch 任何东西。

## 语义

- Release body 里带机器可读 marker（`<!-- releasegraph-post-release {...} -->`），
  记录每个动作的 `status` / `attempts` / `last_error`。健康状态每次都从 GitHub 重新推导，
  没有状态目录。
- 幂等：已经是 `success` 的动作绝不再 dispatch（同一 repo + tag + action id）。
- Resume：之后的 reconcile 看到 `post_release_health=pending` 时重新进入发布 workflow，
  只补跑缺失的动作；不会重建 Release、不会重新打 tag、不会重新上传资产。
- required 失败时 Release 保持已发布，事务标记为 `post-release pending`；下一次定时运行
  会重试（沿用现有 cooldown 护栏）。
- `github-workflow` adapter 先 dispatch `workflow_dispatch`，再把关联 run 轮询到终态
  （有界，默认 30 分钟），而不是把 204 当成成功。

## 验证

```bash
python3 -m release_infra.cli post-release status --path .release-policy.yml
python3 -m release_infra.cli post-release run    --path .release-policy.yml   # 恢复 + 重试
```

`releasegraph workflow-plan`（以及 reusable `reusable-release.yml` 的 build-plan job）
现在输出 `post_release_health`：`absent`（未配置动作）· `pending`（Release 已发布、动作未完成）·
`satisfied`（全部动作成功）。
