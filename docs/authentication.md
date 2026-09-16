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
