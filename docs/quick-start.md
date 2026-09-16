# 快速开始

**语言 / Language:** 中文 · [English](/en/quick-start.md)

先只读试用，再逐步授权。

## 1. 构建 CLI

```bash
git clone https://github.com/redtidev1918/releasegraph.git
cd releasegraph
go build -o releasegraph ./cmd/releasegraph
./releasegraph doctor
```

## 2. 定义 DAG

在 control repository 创建 `release-graph.yml`：

```yaml
apiVersion: releasegraph.dev/v1
projects:
  core:
    repo: {owner: acme, name: core}
  app:
    repo: {owner: acme, name: app}
    dependsOn:
      - {id: core, condition: healthy}
```

## 3. 为项目声明发布契约

在 managed repository 创建 `.release-policy.yml`：

```yaml
apiVersion: releasegraph.dev/v1
kind: binary
versioning:
  provider: release-please
assets:
  required:
    - app-linux-amd64
registries:
  github:
    required: true
checksums: true
metadata: true
```

项目自己的 build script 负责把候选文件放入 `dist/release/`；ReleaseGraph 只验证并编排发布契约。

## 4. 生成只读 live plan

```bash
GITHUB_TOKEN=... ./releasegraph plan \
  --graph release-graph.yml \
  --live \
  --output json
```

公开仓库可以不设置 token，但匿名 API 限额较低。只读阶段不要授予写权限。

## 5. 放入 control repository

复制 [`examples/control`](https://github.com/redtidev1918/releasegraph/tree/v1/examples/control) 中的两个 caller workflow。事件触发一次重新读取和计划，watchdog 每 5 小时补偿丢失事件；两者都不会启动常驻进程。

当前示例只生成 live plan，不 dispatch。等写路径通过 contract canary 后，再按[认证与权限](authentication.md)增加最小跨仓库权限。
