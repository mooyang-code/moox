---
name: dev-helper
description: >-
  Internal MooX developer workflows only (not for end users or public docs).
  Covers remote Linux CGO builds, zip/sync/deploy helpers, and other team
  maintenance tasks. Use when a maintainer needs moox-storage remote compile,
  dev server deploy, or similar internal ops.
---

# dev-helper（内部开发者）

> **仅供 MooX 仓库内部开发者使用。** 不面向用户文档、不写入对外 README。Agent 仅在维护者明确请求内部部署/编译时使用本 skill。

本 skill 收纳**团队日常开发运维**脚本与流程，按场景分节扩展。

## 凭证与 SSH 目标（必读）

**禁止**在仓库脚本、skill、示例命令中写入：

- SSH 密码
- 云 API SecretKey
- 任何可复用的账号口令

请使用 **SSH 公钥** 登录远端；目标主机通过以下方式之一配置（均不入库）：

| 方式 | 说明 |
|------|------|
| 环境变量 `MOOX_DEV_SSH_TARGET` | 例如 `ubuntu@<deploy-host>` |
| `~/.moox-dev.env` | 复制 `skills/dev-helper/env.example` 到用户目录后 `source` |
| `infra/infra.local.yaml` | `remote.ssh` 字段（已 gitignore） |
| SSH config 别名 | `MOOX_DEV_SSH_TARGET=moox-dev`（`~/.ssh/config` 中配置 Host） |
| **交互输入** | 未配置时脚本会在终端提示 `user@host`，可选保存到 `~/.moox-dev.env` |

脚本 `storage-remote-build.sh` 按上述顺序解析目标；**不会**提示或保存 SSH 密码（请用公钥）。

**Agent 注意**：在 Cursor/非 TTY 环境执行时，应先在对话里向用户询问 SSH 目标，再以 `--target` 传入；或使用 `--non-interactive` 并在缺少配置时明确报错，勿把目标或密码写进仓库。

## 工作流索引

| 场景 | 文档 | 脚本 |
|------|------|------|
| moox-storage 远端 CGO 编译部署 | [references/storage-remote-build.md](references/storage-remote-build.md) | `scripts/storage-remote-build.sh` |

---

## 快速开始：moox-storage 远端编译

`moox-storage` 依赖 CGO，**勿在 macOS 上 `GOOS=linux CGO_ENABLED=1` 交叉编译**。标准流程：本地 zip → scp → Ubuntu 上 `CGO_ENABLED=1` 编译 → 部署。

```bash
# 未配置时脚本会交互提示 SSH 目标（不提示密码）
# 也可先: export MOOX_DEV_SSH_TARGET=ubuntu@<deploy-host>

# 在仓库根目录
./skills/dev-helper/scripts/storage-remote-build.sh \
  --deploy-dir /home/ubuntu/moox \
  --deploy
```

仅编译、不部署时去掉 `--deploy`；产物在远端 `/tmp/moox-storage-built`。

详见 [references/storage-remote-build.md](references/storage-remote-build.md)。

## 组件编译方式速查

| 组件 | CGO | 推荐方式 |
|------|-----|----------|
| `moox-admin` | 否 | 本地 `GOOS=linux GOARCH=amd64 go build` |
| `moox-web-host` | 否 | 本地 `GOWORK=off GOOS=linux`（不在 go.work） |
| `moox-storage` | **是** | **dev-helper 远端编译** |
| `moox-cli` / `collector` | 否 | 本地交叉编译 |

admin / web-host 整体部署见 `scripts/deploy-moox.sh`；storage 二进制先用本 skill 产出再部署或 `--deploy`。

## 相关

- [skills/moox/SKILL.md](../moox/SKILL.md) — 仓库通用 skill
- [skills/moox/references/build.md](../moox/references/build.md) — 本地 make build
