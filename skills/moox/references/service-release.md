# 服务包发布

MooX 的发布单元是服务包，而不是裸二进制。服务包使用 ZIP 格式，包含服务二进制、配置
以及启动、停止、健康检查脚本。使用 `moox-cli setup deploy-service` 发布到远端，避免在
Agent 或 shell 中拼接 SSH 密码。

## ZIP 包结构

服务包至少包含以下相对路径：

```text
bin/<service-binary>
config/<service-config>
start.sh
stop.sh
healthcheck.sh
```

包内不得包含绝对路径、`..` 路径、符号链接或以下运行时目录：
`data/`、`logs/`、`run/`、`secrets/`、`certs/`。凭据、数据库、日志和证书由远端现有
部署保管，不能打入服务包。

使用仓库脚本打包。脚本会校验必要目录、生命周期脚本、符号链接和运行时目录，并以
原子方式生成权限为 `0600` 的 ZIP 文件：

```bash
./scripts/build/package-service.sh \
  --service-dir ./release/service-package \
  --output ./release/moox-admin-linux-amd64.zip
```

## 前置条件

- 用户维护且权限为 `0600` 的 `moox.toml`。
- 目标主机已在 `moox.toml` 中定义，例如 `control`。
- SSH 主机指纹已通过独立可信渠道核验，并已写入 MooX known hosts：

```bash
./bin/moox-cli setup trust-host \
  --file ./moox.toml \
  --host control \
  --fingerprint 'SHA256:<已独立核验的指纹>'
```

Agent 不得读取、解析、打印、复制或 `source` `moox.toml` 中的密码和密钥。

## 发布命令

```bash
./bin/moox-cli setup deploy-service \
  --file ./moox.toml \
  --host control \
  --service admin \
  --package ./release/moox-admin-linux-amd64.zip
```

默认远端部署目录为 `/data/moox/prod`，服务部署到其他目录时显式指定：

```bash
./bin/moox-cli setup deploy-service \
  --file ./moox.toml \
  --host compute \
  --service storage-primary \
  --package ./release/moox-storage-primary-linux-amd64.zip \
  --deploy-dir /data/moox/storage
```

## 发布流程

CLI 会按以下顺序执行：

当 `--service` 为 `admin`、`admin_gateway`、`web-host` 或 `web_host` 时，CLI
会在上传前检查控制面 Caddy internal CA 是否已被当前浏览器机器信任，必要时自动
执行平台证书安装。这样发布 Web 前端或 Admin 后端后，SSH WebSocket 不会因为
`ERR_CERT_AUTHORITY_INVALID` 才暴露问题。公网 ACME 模式会跳过该检查。

1. 在本地校验 ZIP 路径、大小、目录穿越、符号链接、必要文件和 SHA-256 摘要。
2. 通过 SSH/SFTP 将 ZIP 上传到远端受限的临时路径，并再次校验远端摘要。
3. 在远端临时目录解压，校验 `unzip`、配置、二进制和生命周期脚本。
4. 备份服务包将覆盖的文件，停止指定服务，将临时内容合并到部署目录。
5. 使用新包中的 `start.sh` 拉起服务，并执行 `healthcheck.sh`。
6. 健康检查失败时按文件清单恢复旧版本；成功后删除备份、临时目录和远端 ZIP。

一次服务发布只操作 `--service` 指定的服务，不会重新部署整个控制面。需要完整控制面
部署时使用 `setup deploy-control` 或仓库级 `make deploy`。

## 安全要求

禁止使用：

```bash
env SSHPASS='...' sshpass -e ssh ...
ssh -o StrictHostKeyChecking=no ...
```

也不要将密码导出到环境变量、写入命令行、shell 历史、临时脚本、CI 日志或 ZIP 包。
如果密码已经出现在命令行、聊天记录或日志中，应立即轮换远端密码，并更新用户维护的
`moox.toml`。

## 常见失败

- `host_key_unknown`：先通过独立渠道核验指纹，再执行 `setup trust-host`。
- `service_package_invalid`：检查 ZIP 结构、必要脚本、路径和运行时目录限制。
- `service_prepare_failed`：确认远端安装了 `unzip`，且部署目录可写。
- `service_activate_failed`：检查新包配置、服务日志和端口占用；CLI 会尝试回滚文件。
- `service_digest_mismatch`：检查上传结果和远端文件系统，不要跳过摘要校验。
