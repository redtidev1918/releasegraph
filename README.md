# ReleaseGraph

面向 GitHub Actions 的无服务器、声明式、DAG 驱动多仓库发布编排器。

**语言 / Language:** 中文 · [English](README.en.md)

📖 文档站：<https://redtidev1918.github.io/releasegraph/>

```text
        core
       /    \
     cli    web
       \    /
      deploy
```

ReleaseGraph 根据期望状态、GitHub 与 registry 实际状态、项目 policy 和依赖图生成发布计划，验证 Release，并恢复未完成的同版本事务。

不需要服务器，不需要数据库，不运行轮询 daemon。

## 当前状态

项目正在从经过生产验证的 Python 实现迁移到独立 Go 二进制。

**当前生产里真正在跑的东西**（所有受管仓库调用的 reusable workflow）：

| 能力 | 实现 | 是否写入 |
|---|---|---|
| provider reconciliation（`provider reconcile --apply`） | **Go** | 是——它会收敛 release-please 的元数据与 label |
| 发布规划（`workflow-plan`） | Python | 否 |
| static check / asset gate | Python | 否 |
| staging / publishing / audit | Python | 是 |

所以 Go 核心在生产里**不是**只读的：它已经承担 provider reconciliation 这一步，这也是每个调用方都要 pin 住它的原因。写入路径的其余部分仍是 Python v1。

Go 核心同样可以只读使用，其余能力目前就是这样用的：

```bash
releasegraph doctor
releasegraph fleet --owner acme --public-only --output json
releasegraph graph --file release-graph.yml --format mermaid
releasegraph inspect --path .release-policy.yml
releasegraph audit --path .release-policy.yml --root dist/release --output json
releasegraph plan --path .release-policy.yml --root . --output json
releasegraph plan --graph release-graph.yml --state health.json --output json
releasegraph plan --graph release-graph.yml --live --output json
```

在 Python/Go 契约 canary 通过前，写入式发布、repair、dispatch、registry 发布与保留策略仍走 Python v1 路径。

## 无服务器控制仓库

一个轻量控制仓库只保存用户拓扑和临时 reconcile worker：

```text
release-graph.yml
.github/workflows/orchestrate.yml
.github/workflows/watchdog.yml
```

最小调用方可从 [`examples/control`](examples/control) 复制。reconcile 事件拉取 GitHub 当前状态、计算计划后退出；完成后开启新一轮运行，而不是持有长生命周期任务。

## 30 秒只读试用

```bash
go build -o releasegraph ./cmd/releasegraph
./releasegraph doctor
./releasegraph graph --file examples/control/release-graph.yml --format mermaid
./releasegraph inspect --path examples/policy/.release-policy.yml
```

仅审计类命令不会调用任何写 API。只有在采用现有 reusable 发布工作流时才授予写权限；跨仓库 dispatch 权限请在验证依赖图之后再授予。

## 声明式依赖图

```yaml
apiVersion: releasegraph.dev/v1
projects:
  core:
    repo: {owner: acme, name: core}
  cli:
    repo: {owner: acme, name: cli}
    dependsOn:
      - {id: core, condition: healthy}
```

事件只触发 reconcile，事件本身不是状态：每个计划都由最新的 Git ref、GitHub Release、工作流状态、policy 和 registry 状态实时算出。上游健康会唤醒下游，但绝不会强迫下游做无意义的版本号 bump。

## 项目策略

受管仓库在 `.release-policy.yml` 中声明必须满足的发布契约：

```yaml
apiVersion: releasegraph.dev/v1
kind: binary
versioning:
  provider: release-please
assets:
  required:
    - app-*-linux-amd64.tar.gz
    - app-*-darwin-arm64.tar.gz
registries:
  github:
    required: true
checksums: true
metadata: true
release:
  postRelease:
    - id: refresh-docs
      type: github-workflow
      required: true
      workflow: update-download-page.yml
      inputs:
        tag: "{{tag}}"
```

发版后动作（`release.postRelease`）在 Release 发布后运行：可恢复、幂等、携带精确 tag 的
workflow（见 [POST-RELEASE](docs/POST-RELEASE.md)）。

构建适配器负责编译器和包管理器，并把候选资产放入 `dist/release/`。ReleaseGraph 负责校验发布顺序、资产契约、校验和/元数据、不可变 tag、registry 以及恢复流程。

GitHub Release 页面上的说明文字同样由 ReleaseGraph 生成：面向下载用户，过滤掉 CI/治理/依赖机器人的噪音，语言跟随仓库主 README。详见 [Release 说明（面向用户）](docs/release-notes.md)。

延伸阅读：

- [架构说明](docs/concepts.md)
- [快速开始](docs/quick-start.md)
- [认证与权限](docs/authentication.md)
- [Release 说明（面向用户）](docs/release-notes.md)
- [英文文档](docs/README.md)

## 文档

README 只讲定位；契约、认证与运维在文档站 <https://redtidev1918.github.io/releasegraph/>：

| 你想做什么 | 文档 |
| --- | --- |
| 先跑通一次发布 | [快速开始](docs/quick-start.md) |
| 理解概念与 DAG 契约 | [核心概念](docs/concepts.md) · [计划契约](docs/plan.md) |
| 在自己的仓库里调用 | [如何调用发布工作流](docs/callers.md) |
| 配权限与凭据 | [认证与权限](docs/authentication.md) |
| 排障、回滚、恢复 | [发布健康](docs/health.md) · [恢复](docs/RECOVERY.md) |
| 分支契约与 PR 流程 | [生产操作分支契约](docs/branch-contract.md) · [PR 生命周期](docs/pr-lifecycle.md) |

## 致谢

- [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml)：图定义解析（Go 侧唯一依赖）。
- [GitHub Actions](https://github.com/features/actions)：ReleaseGraph 的执行平面——它编排的是 Actions 工作流，不是自建调度器。
