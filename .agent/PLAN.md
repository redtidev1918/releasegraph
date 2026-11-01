# 当前计划（ReleaseGraph）

## 2026-10-29 `.agent/` 体系引导 + Issue #91 核验准备

1. ✅ 按 PixivFlow / DAViewer 既有约定建立 `.agent/` 六件套
   （STATUS / PLAN / TASKS / DECISIONS / HANDOFF / LESSONS）。
2. ⬜ RG-02：核验 #91 在当前引擎（v1.5.19 代码）下是否仍复现 ——
   本地 `provider inspect --repo redtidev1918/TelePost` 需
   `RELEASEGRAPH_FLEET_TOKEN`（本地无，实测 `FLEET_CREDENTIAL_REQUIRED`）；
   首选路径是控制仓既有工作流 `provider-operations.yml`（repository
   inspect/plan），先 `apply=false` dispatch 并等待结果。
3. ⬜ RG-03（依赖 RG-02 结果）：
   - 若冲突仍在：按 #91 建议改裁决 —— 把「默认分支上消息为
     `chore: release <version>` 且把 release-please manifest 推进到
     `<version>` 的直接提交」接受为合法 tag 目标；至少把该形状降为诊断项，
     不再阻塞 `RepairPlan`。禁止移动已发布 tag（不变约束）。
   - 若已收敛：在 #91 附上引擎证据链（归因信号 + 期望提交回退路径 + 工作流
     run）后关闭。
4. 持续：Python → Go 迁移按 README 既有路线推进；写入式发布、repair、
   dispatch、registry 发布与保留策略在契约 canary 通过前不切 Go。

## 范围冻结（本轮不做）

- 不为核验 #91 申请/创建任何凭据；不把本地 `gh` 登录改称 fleet token。
- 不移动任何 tag、不补造历史 GitHub Release；历史接受的唯一出口是
  人工决定后的显式 `provider waive`。
- 不重复发布 v1.5.19。

## 验收条件

- RG-02 有工作流 run 或 fleet token 环境下的 reconcile 输出作为证据，
  而不是「看起来应该已修」。
- 若动引擎：`go vet ./... && go test ./...`、Python 套件、
  `git diff --exit-code -- dist/release/RELEASE-METADATA.json` 全过；
  修复以 reviewable PR 交付，遵循一提交一关注点。

## 2026-11-01 Fleet PR Health（实施计划，编码前提交）

1. 保存启用的 fleet 清单内开放 PR / statusCheckRollup 与 Dependabot 配置基线；记录覆盖范围及凭据限制。
2. 新增独立 `pr-health audit`，复用 Go fleet / credential / GitHub 客户端；完整分页、必需检查、Actions 证据、确定性分类；未知与缺失不得通过。
3. 新增唯一控制仓告警原语，默认计划、显式 apply；稳定指纹、恢复关闭、不完整扫描保留既有故障；扫描与写入 token 分离。
4. 每小时 Actions + dispatch + concurrency + 独立失败告警 + 始终输出 Summary / JSON；不操作业务 PR。
5. 覆盖用户验收场景，运行 Go/Python/static/actionlint 检查；建立独立 PR 并核验 CI。
6. 在专用控制仓测试 PR 制造可控失败，验证真实告警、重复抑制、恢复；定时执行需要默认分支工作流，未取得真实证据前不得宣称验收完成。
7. 更新六份治理文件、架构/测试文档和交接；只建议最小 Dependabot 配置修复。

权限现状：控制仓配置了 App client ID / private-key secret 与 fleet secret（仅核对名称）。本地普通 gh 登录不充当 fleet token；真实 App 权限由 runner 验证。清单 16 条目，15 enabled，svn-easy-kit disabled。

## 2026-11-01 更新：三阶段 Fleet Health 治理（当前授权）

社区试用后，用户要求恢复 ReleaseGraph 权威控制平面方案，社区界面仅可选。

- PR 1：只读审计、GraphQL/REST 完整性、故障分类、健康基线及测试。
- PR 2：唯一 Issue 持久化、首次/最近发现/恢复时间、去重、恢复、告警失败隔离及测试。依赖 PR 1，以 feature stack 提交。
- PR 3：定时/手动扫描、分离 App 读取与控制仓 Issue 写入、Artifacts、独立监控失败路径、专用 E2E 验证、社区/Dependabot 接入与验收文档。依赖 PR 2。

三个 PR 均不自动合并。新 schedule 在默认分支合入前不会生效；真实 E2E 在本控制仓专用测试分支执行，不改变业务 PR。未验证项必须保留待验收状态。

社区现场：buluma/github-monitor f9a717b 后端扫描 28 个非归档仓库，31 个 PR（20 pass/11 fail），与受管基线集合完全一致。界面报 Svelte effect_orphan；源码吞掉部分 API 异常且 checks 只读前 100 条，不可作为治理权威。临时本地服务已停止；源码留在 ~/.local/share/github-monitor，未加入本仓。

## 2026-11-01 最终任务覆盖此前执行约定

按上游模板、下游同步、安全边界、只读审计、持久化告警与调度依赖顺序推进。已授权的基础设施 PR 在最新 SHA CI 验收后按仓库规则合并；业务依赖 PR 不自动合并。Fleet 外仅只读报告，docsite 为用户明确点名的上游修复例外。调度先每小时，故障键为仓库+PR+head SHA+检查身份；SKIPPED 独立保存。复用现有 GitHub 连接器及 gh，不安装重复 MCP。
