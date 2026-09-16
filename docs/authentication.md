# 认证与权限

## Level 1：只读审计

只授予 repository contents 和 metadata 读取权限。公开仓库可以匿名读取，但 API 限额较低。

## Level 2：单仓库发布

managed repository 使用该次 GitHub Actions job 的短期 `GITHUB_TOKEN`，按项目需要授予 `contents: write` 与 registry trusted publishing 权限。

## Level 3：DAG 编排

优先使用 GitHub App installation token，不需要 GitHub App server。App 只安装到 graph 中的仓库：

- `repository_dispatch`：目标仓库需要 `Contents: write`。
- `workflow_dispatch`：目标仓库需要 `Actions: write`。

小型 fleet 可以使用只覆盖 graph 仓库的 fine-grained PAT。不要默认使用 classic 全仓库 PAT。reusable workflow 不能提升 caller 已授予的 token 权限。

完整配置示例见[英文认证文档](/en/authentication.md)。

## workflow-dispatch post-release 权限

`release.postRelease` 的 `type: github-workflow` 会对同仓库发起 `workflow_dispatch`，该端点要求 ephemeral `GITHUB_TOKEN` 拥有 `Actions: write`：

- `github-workflow` postRelease 要求调用方 workflow 授予 `actions: write`。
- 执行 `github-workflow` postRelease 的 job 必须在自身 permissions 中保留 `actions: write`。
- 同仓库 `workflow_dispatch` 在授予 `Actions: write` 时，用 `GITHUB_TOKEN` 即可，默认不需要 PAT/App。

