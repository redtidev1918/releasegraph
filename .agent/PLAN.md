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
