# Release 说明（面向用户）

GitHub Release 页面是写给**下载用户**的，不是写给合并 PR 的人看的。ReleaseGraph 负责生成这份说明，业务仓库不写、不维护它。

`CHANGELOG.md` 可以继续保留完整的历史；Release 正文只是面向用户的摘要。

## 正文结构

按需出现，**空小节直接省略**：

| 英文仓库 | 中文仓库 |
|---|---|
| `What's new` | `新增功能` |
| `Fixes` | `问题修复` |
| `Improvements` | `体验改进` |
| `Breaking changes` | `破坏性变更` |
| `Upgrade notes` | `升级说明` |
| `Downloads` | `下载` |

`Downloads` **只在真的有东西可下载时出现**。服务仓库、部署仓库、容器仓库、纯 registry 仓库没有二进制，正文会明确写出交付渠道（例如 `ghcr.io/...` 镜像、npm/PyPI 包），而不是留一个空白的下载区。

## 语言

语言跟随仓库的**主 README**（GitHub 首页展示的那一个）：`README.md` 明显是中文就用中文小节，否则用英文。`README.en.md` 这种镜像不参与判断。

需要强制指定时，在业务仓库的 `.release-policy.yml` 里写：

```json
{"release": {"notes": {"language": "zh"}}}
```

可选值是 `auto`（默认）、`en`、`zh`。

## 自动推导覆盖 80–90%

ReleaseGraph 把 Conventional Commits 当作**输入信号**，而不是最终文案：

- `feat` → 新增；`fix` / `hotfix` / `bugfix` / `security` / `revert` → 修复；`perf` / `refactor` / `improve` → 改进。
- `chore`、`ci`、`build`、`release`、`docs`、`test`、`deps`、`governance`、`ops`、`infra`，以及**任何未登记的 type**，一律不进正文。
- 依赖机器人（dependabot / renovate / github-actions[bot]）的提交一律不进正文。
- 版本号 bump、merge commit、`release-please` 提交一律不进正文。
- 正文里不会出现 type、scope 或 PR 编号：`fix(media): refactor planner (#123)` 永远不会原文出现在页面上。
- 主语里的自动化词汇（`release-infra`、`provider reconciliation`、`branch contract`、`generated metadata`、`governance`、`lockfile` 等）会让整条被丢弃，即使它的 type 看起来像 `feat`。

## 人工覆盖（特殊发版才用）

普通提交**什么都不用做**。只有特殊发版需要人工写文案，三种方式任选：

1. **提交正文里加一行 trailer**（最常用）：

   ```text
   fix: avoid crash when sending galleries over 10MB

   release-note: Fixed gallery delivery when some images exceeded Telegram's photo size limits.
   ```

   键名不区分大小写，可以续行：

   | 键 | 效果 |
   |---|---|
   | `release-note:` | 用这句话替换自动生成的文案（原文照用，不再改写） |
   | `release-note-breaking:` | 同上，并强制计入「破坏性变更」 |
   | `upgrade-note:` | 追加一条「升级说明」 |

   `release-note:` 也能「救回」一个本来会被过滤掉的 type（例如 `housekeeping:`），因为那是人做的判断。

2. **整篇手写**：在业务仓库放 `.github/release-notes/<version>.md`（或 `v<version>.md`、`current.md`）。命中即整篇替换。如果手写正文里没有 `Downloads` / `下载` 小节，ReleaseGraph 仍会把真实资产表追加在后面。

3. **策略里固定语言**：见上一节。

本地预览（不需要发版）：

```bash
cd <业务仓库>
PYTHONPATH=<releasegraph> python3 -m release_infra.cli notes --path .release-policy.yml --version 1.2.3
```

## 发布契约

生成好的正文由 ReleaseGraph 在事务里使用，而不是 GitHub 的 `--generate-notes`：

- **草稿先行**：正文随 draft 一起创建；构建产物先落到 draft，资产闸门通过后才提升为正式 Release。声明了二进制资产的仓库，缺资产时 `publish` 直接拒绝提升——不会出现「只有 Source code 却成了官方 Release」。
- **Latest**：预发布（版本号带 SemVer 预发布段，如 `1.5.0-rc.1`，或策略里写了 `release.prerelease: true`）永远不抢 `Latest`；修复旧版本时显式 `--latest=false`，不会把 `Latest` 往回挪。
- **历史**：已发布的稳定版本是历史，默认**不删**。只有策略显式写 `retention.pruneStable: true` 才会按 `retention.stable` 回收，且当前 `Latest` 永远不回收。预发布与失败草稿仍按 `retention.prerelease` / `retention.failed_draft` 回收。
