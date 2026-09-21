# 注册表发布与仓库改名

本文档收集跨仓库反复出现的 OIDC / trusted publisher 规则。规则已经在真实
流水线中验证过；不要依赖外部网页搜索结果来覆盖下面任何一条。

## 共同规则

- GitHub 仓库名是大小写敏感的 OIDC claim。仓库改名后，所有 trusted publisher
  里的 Repository 都必须改成 GitHub 当前的规范名；旧小写名即使能通过 GitHub
  API 重定向，也会被 OIDC 校验拒绝。
- PyPI 和 pub.dev 的 trusted publisher 配置都只能在各自网站的 Admin / Publishing
  页面修改，没有公开的管理 API 或 CLI。`dart pub`、`twine` 只能发布，不能改配置。
- npm 和 GHCR 不使用这种“Repository claim 匹配”的发布方配置：npm 用 token，
  GHCR 用 `GITHUB_TOKEN`，仓库改名不会破坏发布方匹配。
- 已发布版本的 registry 元数据（例如 pub.dev 侧边的 Repository 链接）是历史快照，
  不会在管理员页面“改回来”；下一次用新 pubspec/新元数据发布新版本后自动更新。

## PyPI

- Trusted Publisher 字段必须完全匹配：Owner、Repository（规范名大小写）、
  Workflow 文件名、Environment 名。
- 仓库改名后若仍用旧 Repository，发布会报 `invalid-publisher: valid token, but no
  corresponding publisher`。
- PyPI 允许 push / workflow_dispatch 等非 tag 触发，不存在 pub.dev 那种 tag-only
  限制。

## pub.dev

- pub.dev 只允许 **由 git tag 触发** 的 GitHub Actions 发布。branch refType 的
  OIDC token 一律被拒绝，报错原文：
  `publishing is only allowed from 'tag' refType, this token has 'branch' refType`。
- 每个包在 Admin → Publishing → GitHub Actions 里配置一个 tag pattern，形如
  `my_package-v{{version}}`。发布 workflow 必须由匹配该 pattern 的 tag push 触发。
- 仓库改名后，必须把每个包的 Trusted Publisher Repository 改成规范名；如果 tag
  pattern 也按包名区分，改名不改变 pattern。
- 多包仓库建议每个包一个组件 tag，例如 `dakit_core-v1.2.3`；不要在 main push
  里尝试直接发布。

## 仓库改名后的完整步骤

1. 用 `gh api users/<owner>/repos --paginate --jq '.[].name'` 拿到规范名；
2. 更新 `fleet.yaml` 里所有旧名；
3. 在 PyPI / pub.dev 的 Admin 页面把 trusted publisher 改成规范名；
4. 推入代码后跑一次 `fleet-audit` 重建 `status.json` 和 snapshots；
5. 下一次真实发布验证。旧 CHANGELOG / README 链接会被 GitHub 重定向，属于
   可选的文本清理，不是发布门禁。
