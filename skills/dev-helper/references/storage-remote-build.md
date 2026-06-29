# moox-storage 远端 CGO 编译部署

## 何时使用

- 需要部署新版 `moox-storage`（含 `view.timer` 等 CGO 逻辑）
- 本地 Mac 无法交叉编译 storage
- 维护者要求「打包 zip、同步远端、解压编译、部署」

## 前置条件

**本地**：仓库根目录可执行；已安装 `zip`、`scp`、`ssh`。

**凭证**：勿在仓库中写 SSH 密码。配置 `MOOX_DEV_SSH_TARGET`、`infra/infra.local.yaml`，或运行脚本时按提示输入 `user@host`（见 [SKILL.md](../SKILL.md)）。

**远端 Linux**（`$MOOX_DEV_SSH_TARGET` 或 SSH config 别名）：

- Go **1.24+**，例如 `/home/ubuntu/.local/go1.24.0/bin/go`
- `gcc` / `g++`（`build-essential`）
- 部署目录 `/home/ubuntu/moox`（含 `start.sh`、`storage/config`）
- 临时空间 **2GB+**

## 一键脚本

```bash
# 已 export MOOX_DEV_SSH_TARGET 或配置 infra.local.yaml 时无需 --target
./skills/dev-helper/scripts/storage-remote-build.sh

./skills/dev-helper/scripts/storage-remote-build.sh \
  --deploy-dir /home/ubuntu/moox \
  --deploy
```

| 参数 | 默认 | 说明 |
|------|------|------|
| `--target` | 见环境 | SSH 目标；默认 `MOOX_DEV_SSH_TARGET` 或 `infra.local.yaml` `remote.ssh` |
| `--deploy-dir` | `/home/ubuntu/moox` | 远端部署根目录 |
| `--remote-go` | `/home/ubuntu/.local/go1.24.0/bin/go` | 远端 Go |
| `--remote-build-dir` | `/home/ubuntu/moox-build-remote` | 远端解压目录 |
| `--deploy` | 关 | 安装到 `bin/` 并 `./start.sh storage` |
| `--skip-pack` | 关 | 跳过本地打包 |
| `--non-interactive` | 关 | 无配置时不提示，直接失败（供 CI/Agent） |

## 手动步骤

以下命令中 `TARGET` 表示你的 SSH 目标（环境变量或别名），**不要**把密码写进脚本文件。

### 1. 本地打包

```bash
cd "$(dirname "$(git -C /path/to/moox rev-parse --show-toplevel)")"
COPYFILE_DISABLE=1 zip -r /tmp/moox-build.zip moox \
  -x "moox/.git/*" \
  -x "moox/web/node_modules/*" \
  -x "moox/bin/*" \
  -x "moox/**/data/*" \
  -x "moox/release/*" \
  -x "moox/**/.DS_Store" \
  -x "moox/**/pebble/main/*"

unzip -l /tmp/moox-build.zip | grep pebble/store.go
```

### 2. 同步

```bash
scp /tmp/moox-build.zip "${MOOX_DEV_SSH_TARGET}:/tmp/moox-build.zip"
```

### 3. 远端编译

```bash
ssh "${MOOX_DEV_SSH_TARGET}" 'set -e
export PATH=/home/ubuntu/.local/go1.24.0/bin:$PATH
rm -rf /home/ubuntu/moox-build-remote && mkdir -p /home/ubuntu/moox-build-remote
cd /home/ubuntu/moox-build-remote && unzip -q /tmp/moox-build.zip
cd moox
CGO_ENABLED=1 go build -ldflags "-s -w" \
  -o /tmp/moox-storage-built ./modules/storage/cmd/moox-storage
file /tmp/moox-storage-built
'
```

### 4. 部署

```bash
ssh "${MOOX_DEV_SSH_TARGET}" 'set -e
cd /home/ubuntu/moox
./stop.sh storage
cp /tmp/moox-storage-built ./bin/moox-storage && chmod +x ./bin/moox-storage
export STARTUP_WAIT_SECONDS=15
./start.sh storage && ./status.sh
'
```

### 5. 验证

```bash
ssh "${MOOX_DEV_SSH_TARGET}" '
  ss -tlnp | grep -E "20200|20201|20202"
  tail -3 /home/ubuntu/moox/logs/storage/trpc.log | grep -E "launch success|ERROR"
'
```

期望：`view.timer launch success`，端口 `20200/20201/20202` 监听。

## 常见问题

**`no required module provides package .../pebble`** — zip 缺源码；确认从仓库父目录打包且含 `pebble/store.go`，在解压后 `moox/` 根目录 build。

**`start.sh storage` 报 failed** — 提高 `STARTUP_WAIT_SECONDS=15`。

**tar 代替 zip** — 可以，`COPYFILE_DISABLE=1 tar czf` + 远端 `tar xzf`。
