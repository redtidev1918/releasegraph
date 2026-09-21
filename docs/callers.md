# 如何调用发布工作流

受管仓库只带一个调用方文件 `.github/workflows/release.yml`，除此之外不带任何发布相关的东西。所有发布逻辑都在本仓库，所以业务仓库自己从不实现版本管理、资产闸门或发布动作。

```yaml
jobs:
  release:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    permissions: {contents: write, packages: write, pull-requests: write, id-token: write}
    secrets: inherit
```

请固定 tag：`@v1` 是已发布的大版本，写 commit SHA 则精确固定。

## 输出

| 输出 | 取值 | 用途 |
|---|---|---|
| `release_health` | `healthy` \| `tag-drift` \| `repair` \| `missing` | **要分支判断的字段。** 只要不是 `healthy`，就说明已发布状态不满足 policy |
| `run_release` | `1` \| `0` | 这次运行到底会不会发布，还是一次 no-op |
| `version` | 如 `1.2.3` | 本次计划的目标版本 |
| `tag` | 如 `v1.2.3` | 本次计划的目标 tag |
| `tag_drift` | `true` \| `false` | 已存在的 tag 不是当前 head |
| `paths_released` | Release Please 组件路径 | 本次发布的 monorepo 组件 |

```yaml
- id: release
  uses: ...
- if: steps.release.outputs.release_health != 'healthy'
  run: echo "::warning::release state is ${{ steps.release.outputs.release_health }}"
```

这些都是**增量**的。四个 `release_health` 字符串是**冻结的生产契约**：工作流内部有 8 处用 `!= 'healthy'` 判断，且有 14 个仓库调用它，所以只能增加取值，**不能改名或删除**。词表为何是封闭的、以及健康与"调用方该做什么动作"的区别，见 [发布健康](health.md)。

每个输出都是从 `build-plan` job 转发出来的。如果某个 workflow 输出指向了一个并不存在的 job 输出，调用方拿到的会是**空字符串**，而且仓库里任何地方都不会报错 —— 所以 `test_every_forwarded_output_exists_on_its_job` 用结构化方式检查这些引用，而不是信任它们。

## 输入

| 输入 | 默认 | 含义 |
|---|---|---|
| `version` | `""` | 覆盖计划的目标版本 |
| `dry_run` | `false` | 只计划和构建，不发布任何东西 |
| `force` | `false` | 对已健康的版本重跑诊断 |
| `repair` | `false` | 修复**同一个**版本的不完整发布 |
| `stage` | `all` | 运行哪些阶段 |

## Secrets

全部可选：`RELEASE_PLEASE_TOKEN`、`NPM_TOKEN`、`ANDROID_KEYSTORE_B64`、`ANDROID_KEYSTORE_PROPERTIES`。缺某个 secret 只会关掉对应的发布，不会让整次运行失败。

## Caller 侧注册表发布与 ACK 时序

`finalize` 里的 Provider acknowledgement 是发布事务的最后一步。若仓库把必需的注册表发布放在 caller 自己的独立 job 里（例如 PyPI Trusted Publishing），ACK 会在那个 job 之前执行，健康门禁会以 `REGISTRY_INCOMPLETE` 拒绝 ACK——门禁在正常工作，不是流水线故障。这类仓库要在注册表 job 之后追加一个薄 caller job，复用本仓库的 `reusable-acknowledge.yml`：

```yaml
acknowledge:
  needs: [release, publish-pypi]
  if: >-
    needs.release.result == 'success' &&
    needs.release.outputs.run_release == '1' &&
    (needs.publish-pypi.result == 'success' || needs.publish-pypi.result == 'skipped') &&
    github.event_name != 'pull_request' &&
    !(github.event_name == 'workflow_dispatch' && github.event.inputs.dry_run == 'true')
  uses: redtidev1918/releasegraph/.github/workflows/reusable-acknowledge.yml@<pinned-ref>
  with:
    version: ${{ needs.release.outputs.version }}
  secrets: inherit
```

该 workflow 从 `job.workflow_sha` 构建引擎，与 caller 的 pin 严格同 commit，重跑同一幂等 `provider reconcile --apply`，并在注册表索引传播期间有界重试；只有真正出现 ACK 记录、或 `providerState == "TAGGED"` 且 `health == "HEALTHY"` 才算成功。`provider reconcile` 本身在 ACK 被拒绝时也会正常退出，所以 caller job 绝不能只看退出码。没有独立注册表 job 的仓库不需要这个 job。

## Release 正文

Release 页面上的说明文字由 ReleaseGraph 生成，调用方不需要提供：`feat` / `fix` / 破坏性变更会被归类并改写成用户措辞，而 `chore` / `ci` / `governance` / 依赖机器人 / 版本号 bump 不会出现在页面上。特殊发版可以用提交正文里的 `release-note:` 覆盖，或放 `.github/release-notes/<version>.md` 整篇替换。语言跟随仓库主 README。详见 [Release 说明（面向用户）](release-notes.md)。

## 调用方不该做的事

业务仓库不要跑 `gh release create`、`git tag -f`、`git push --force`，也不要用任何手写 API 变更去"修"自己的发布。那些做法会绕开平台强制执行的每一条 exactly-once 不变量。如果缺某个原语，它应该作为新原语加进本仓库，带上自己的测试和 dry run。
