# 会话交接（ReleaseGraph）

## 2026-10-29 `.agent/` 引导 + #91 现场核验（Asia/Shanghai）

范围：建立 `.agent/` 六件套（用户指令）；对 issue #91 做只读取证。
未改引擎、未推送、未 dispatch 工作流、未触碰任何 release 状态。

### 现场事实

- 起点 `main@d4264aa`，与 `origin/main` 同步，工作区干净；本轮仅新增
  本目录六文件（未提交）。
- Issue 队列全量：#91 OPEN（2026-09-26，无评论无 label）、#85 CLOSED。
  #91 详证见 issue 正文：TelePost v2.64–2.67 tag 指向直接发版提交
  （如 v2.67.0→`3c7a58b`，`gh api .../commits/3c7a58b/pulls` 为空），
  引擎期望被推算成 PR #240 合并提交（v2.64.0 期望 `d22a82a`，
  tag 实指 `d2e9bda`）→ `RepairPlan` unsafe，永不自愈。
- 本地复现尝试：`GITHUB_TOKEN=$(gh auth token) go run ./cmd/releasegraph
  provider inspect --repo redtidev1918/TelePost` →
  `FLEET_CREDENTIAL_REQUIRED`（exit 40）。符合 AGENTS.md 跨仓需
  `RELEASEGRAPH_FLEET_TOKEN` 的设计；本地未持有该凭据。
- 代码取证：#91 之后已有 `3e7ad7a`（2026-09-30）、`f77f8d4`（2026-10-07）、
  `f855d3e`（2026-10-09）三笔 provider 修复；当前归因链在「无 release PR」
  时把期望提交回退到 Release 元数据 commit（`releaseplease.go:151-157`），
  理论上覆盖直接发版形状 —— 但这**不是**核验，TelePost 实际收敛与否
  未确认（见 LESSONS L-1：仪表盘 HEALTHY 无证明力）。
- 仪表盘（fleet-audit 2026-10-29）：15 仓全 HEALTHY；TelePost 最新
  v2.84.9，Release 含 `RELEASE-METADATA.json` 资产（status.json）。

### 下一位 Agent 的入口

1. RG-02：经控制仓 `provider-operations.yml` dispatch（`apply=false` 先行）
   拿 TelePost 的 provider 裁决；或人工提供 fleet 凭据后本地跑
   `releasegraph provider inspect --repo redtidev1918/TelePost`。
   禁止把本地 `gh` 登记记为 fleet token。
2. 结果分流见 PLAN 第 3 步与 TASKS RG-03：仍冲突 → 修归因/降级诊断
   （动 `internal/provider/`，走 worktree + 全量检查 + PR）；已收敛 →
   附证据关 #91。
3. 引擎若改动：`go vet ./... && go test ./...` + Python 套件 +
   `git diff --exit-code -- dist/release/RELEASE-METADATA.json`；
   套件弄脏该文件时恢复而非提交。

### 本轮改动清单

仅新增 `.agent/{STATUS,PLAN,TASKS,DECISIONS,HANDOFF,LESSONS}.md`；
用户授权后以 docs 提交（单提交单关注点）推送至 `origin/main`。
无其他文件改动；推送前按 AGENTS.md 跑完三查，
`dist/release/RELEASE-METADATA.json` 无差异入库。
