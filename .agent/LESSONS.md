# 工程教训（ReleaseGraph，按事件非 Bug 清单）

## L-1 「仪表盘 HEALTHY」证明不了 provider 裁决干净（2026-10-29）

fleet-audit 生成的根 STATUS.md / status.json 的 HEALTHY 来自
`release_infra/inventory.py`（workflow 成败 + 资产完整性维度）；issue #91
的 `TAG_CONFLICT` 出自 Go 引擎 `provider reconcile` 的归因维度。TelePost
在仪表盘 HEALTHY（v2.84.9）与 #91 仍 OPEN 并存，两套健康判定互不涵盖。
**教训**：回答「某仓 release 是否健康」前先问是哪个子系统在裁决；
关闭 #91 这类 issue 必须拿 provider 裁决本身的证据，不能引用仪表盘。

## L-2 reconcile 与已发版事实冲突时，先怀疑引擎的「期望」（2026-09-26 #91）

#91 的本质不是 tag 指错，而是引擎把期望提交推算成了 release-PR 合并提交，
而仓库的真实形状是直接落在 main 的发版提交。发版已成功的既成事实优先，
归因模型要么纳入该形状、要么显式 waive，tag 永不移动（AGENTS.md 规则 5）。
**防复发**：归因信号（title / manifest / label / 元数据回退）每加一路，
都要问「没有 release PR 的版本走哪条路」，并在 inspect 测试里固化该形状。

## L-3 测试副产物会污染受跟踪的发行工件（AGENTS.md 已知债）

全量 Python 套件会重写 `dist/release/RELEASE-METADATA.json`（测试时间戳）。
推送前三查的最后一项就是 `git diff --exit-code` 该文件；失败时恢复文件，
绝不 `git add -A`。此债清偿前，任何会话结束前都要复核工作区
（RG-04）。

## L-4 fleet 权限缺失是环境事实，不是可绕过的障碍（AGENTS.md「Scope and credentials」）

跨仓 provider 命令在本地没有 `RELEASEGRAPH_FLEET_TOKEN` 时立即
`FLEET_CREDENTIAL_REQUIRED`（本轮实测 exit 40），且不会回退到仓库 token。
正确路径是控制仓既有工作流（`provider-operations.yml` 等，先
`apply=false`）或人工提供凭据；把普通 `gh` 登录当作 fleet token 使用、
或从 runner 提取 Actions secret，都是被明确禁止的捷径。
