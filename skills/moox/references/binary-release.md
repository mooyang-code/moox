# 单个二进制服务发布

当只需要发布或替换远端某个二进制服务，而不需要重新部署整个控制面时，使用本流程。
当前 CLI 已提供 Web Host 的 `moox-cli setup deploy-web-host` 入口；其他服务应复用
相同的凭据读取、SSH 主机校验、原子上传、健康检查和失败回滚原则接入对应命令。
命令由 CLI 在进程内读取 `custom.toml`，通过 SSH/SFTP 完成上传、原子替换、重启和
健康检查；不要在 Agent 或 shell 中拼接 SSH 密码。

## 前置条件

在仓库根目录准备以下内容：

- 用户维护且权限为 `0600` 的 `custom.toml`。
- 目标主机已在 `custom.toml` 中定义，例如 `control`。
- 目标主机的 SSH 指纹已通过独立可信渠道核验，并已写入本机 MooX known hosts：

```bash
./bin/moox-cli setup trust-host \
  --file ./custom.toml \
  --host control \
  --fingerprint 'SHA256:<已独立核验的指纹>'
```

如果 `custom.toml` 尚未准备好，只能让用户自行创建或修改；Agent 不得读取、解析、
打印、复制或 `source` 其中的密码和密钥。

## 构建目标二进制

以 Web Host 为例，远端 Linux amd64 主机使用：

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host
```

远端 Linux arm64 主机使用：

```bash
TARGET_GOOS=linux TARGET_GOARCH=arm64 ./scripts/build.sh web-host
```

发布前可检查本地文件类型和摘要，但不要把凭据放入检查命令：

```bash
file ./bin/moox-web-host
sha256sum ./bin/moox-web-host
```

## 发布命令

默认发布到远端 `~/moox/prod`：

```bash
./bin/moox-cli setup deploy-web-host \
  --file ./custom.toml \
  --host control \
  --binary ./bin/moox-web-host
```

如果远端使用其他部署目录，显式指定目录：

```bash
./bin/moox-cli setup deploy-web-host \
  --file ./custom.toml \
  --host control \
  --binary ./bin/moox-web-host \
  --deploy-dir /home/ubuntu/moox/prod
```

命令成功后输出 JSON，包含远端路径和本地、远端 SHA-256 摘要，不包含密码或密钥。

## 命令执行内容

CLI 会按以下顺序完成发布：

1. 在进程内加载并校验 `custom.toml`，不把密码写入命令参数、环境变量或日志。
2. 校验已信任的 SSH 主机指纹，并解析 `~` 对应的远端用户目录。
3. 校验远端部署目录、`stop.sh`、`start.sh` 和 `healthcheck.sh`。
4. 备份当前二进制，停止现有 Web Host，通过 SFTP 上传临时文件，再原子替换为新二进制。
5. 设置二进制权限为 `0755`，启动 Web Host，执行健康检查并比较 SHA-256 摘要。
6. 只有所有检查成功后才删除旧版本备份；中途失败时自动回滚并尝试恢复服务。

因此，这个命令只发布 Web Host，不会重新部署 Admin、Gateway、Storage 或其他服务。
需要完整控制面部署时，继续使用 `setup deploy-control` 或仓库级 `make deploy`。

## 安全要求

禁止使用以下方式发布：

```bash
env SSHPASS='...' sshpass -e ssh ...
ssh -o StrictHostKeyChecking=no ...
```

也不要将密码导出到环境变量、写入命令行、shell 历史、临时脚本、CI 日志或文档。
如果密码已经出现在命令行、聊天记录或日志中，应立即轮换远端密码，并同步更新用户维护
的 `custom.toml`；不要把旧密码再次写入仓库或回复内容。

## 常见失败

- `host_key_unknown`：先通过独立渠道核验指纹，再执行 `setup trust-host`。
- `web_host_prepare_failed`：确认远端目录及 `stop.sh`、`start.sh`、`healthcheck.sh`
  存在且可执行。
- `web_host_activate_failed`：检查远端服务启动日志和端口占用；CLI 会保留或恢复旧二进制。
- `web_host_digest_mismatch`：检查构建目标架构、上传结果和远端文件系统；不要直接跳过摘要校验。
