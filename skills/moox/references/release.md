# 发布与部署

## 发布包

```bash
make release                                # 生成当前平台的发布包
VERSION=v0.1.0 make release-matrix          # 生成全部平台的发布包和 SHA-256
```

发布包包含全部二进制、`moox.toml` 示例、默认初始化数据和文档，默认矩阵是 Linux amd64/arm64、macOS amd64/arm64 和 Windows amd64
（`RELEASE_PLATFORMS` 可以缩小）。推送 `v*` 标签后 GitHub Actions 和 CNB 流水线创建发布并上传同一批归档。
矩阵交叉构建时，除非目标是当前主机或设置了 `STORAGE_CGO_ENABLED=1`，Storage 用无 CGO 的回退构建；完整的 DuckDB 版本需要目标平台的 C 工具链。

**部署不使用发布包。** `moox-cli setup deploy-host` 在仓库里按 `moox.toml` 渲染每台主机的发布（配置、组件规格、运行脚本），
构建二进制，打包上传，由主机上的安装器切换。生产部署只用 `moox-cli setup` 命令，CI 不部署生产。

## 部署命令

前置条件：用户维护且权限为 `0600` 的 `moox.toml`；目标主机已在其中定义；SSH 主机指纹已通过独立可信渠道核验并记录
（`setup trust-host`）。Agent 不得读取、解析、打印、复制或 `source` `moox.toml`，也不要把密码放进命令行、环境变量、shell 历史、
临时脚本或 CI 日志。

```bash
./bin/moox-cli setup deploy-host --file ./moox.toml --host storage                       # 整台主机
./bin/moox-cli setup deploy-host --file ./moox.toml --host control --components monitor  # 只部署其中几个组件
./bin/moox-cli setup deploy-service --file ./moox.toml --component admin                 # 部署了该组件的全部主机
./bin/moox-cli setup deploy-service --file ./moox.toml --component access --host storage # 指定一台主机
./bin/moox-cli setup rollback --file ./moox.toml --host storage                          # 切回上一个发布
./bin/moox-cli setup render --file ./moox.toml --host storage --out /tmp/render-storage  # 只渲染，不连接主机
```

| 选项 | 作用 |
| --- | --- |
| `--skip-build` | 复用仓库 `bin/` 中已有的二进制 |
| `--reuse-binaries` | 不上传二进制，复用主机当前发布中的；只更新配置、密钥和证书（EventBus 凭据同步用它） |
| `--no-start` | 只安装，不启动组件 |
| `--no-sync` | 不同步 Admin 中的部署记录（Admin 不可用、需要先修复 control 时使用） |
| `--maintenance-lock-held` | 操作员已在该主机上持有维护锁，安装器不再加锁 |

每次部署一台主机，CLI 依次完成：渲染发布并构建二进制 → 在 control 上取这台主机要用的调用方密钥、外部调用方密钥集并重新签发主机网关
证书 → `SyncHostPlacements` 同步 Admin 里这台主机的部署记录（页面上设置的启用/停用状态保留）→ 上传发布包并运行安装器。安装器持有维护锁，
解出发布目录，装入密钥和证书，生成缺少的本机密钥，停止要替换的组件，切换 `current`，启动组件（暂停的组件不启动）；启动失败时切回
上一个发布并重新启动这些组件。

发布包不包含数据、日志；密钥和证书只在安装时装进 `secrets/`、`certs/`，随后从发布目录删除。组件目录内嵌在 Admin、Monitor、CLI、
主机网关和外部接入中，组件目录有变化时这几个组件要一起发布。

只部署一个服务时不必重新部署整台主机：`deploy-service` 只重新部署该组件。**EventBus 凭据刚轮换时先完成
[`eventbus-credentials.md`](eventbus-credentials.md) 的 fan-out**，因为 `deploy-service` 不会更新同一主机上其他组件的凭据。

## 暂停、恢复与启停

```bash
./bin/moox-cli setup pause   --file ./moox.toml --host control --components collector,strategy
./bin/moox-cli setup resume  --file ./moox.toml --host control --components collector,strategy
./bin/moox-cli setup service status  --file ./moox.toml --host control
./bin/moox-cli setup service restart --file ./moox.toml --host control --components monitor
```

暂停写入标记 `run/paused/<组件>` 并停止进程；标记在发布目录之外，重新部署后仍然有效，`start.sh`、`healthcheck.sh` 和安装器都不会拉起被暂停的
组件，直到 `resume`。`service start|restart --force` 忽略暂停标记。停止组件不写暂停标记，每分钟的 `healthcheck.sh` 仍会把它拉起，
需要长时间停着就用 `pause`。

## 修改 moox.toml 之后

用户修改 `moox.toml`（例如 `[storage_retention]` 的保留期或 `[storage_view]`）时，用 `moox-cli config` 发布：先
`moox-cli config plan --file ./moox.toml`，把每个目标的状态、变化的键名、要重新部署的组件、告警和 `retention_shrinks` 给用户看，用户确认后才执行
`moox-cli config publish --file ./moox.toml --yes`。缩短保留期会在下一次 Storage 清理时删除数据，只有用户明确接受时才加
`--allow-retention-shrink`。不要手工编辑主机上的配置文件。

## 需要停机的切换

整体升级（替换网关、迁移数据目录等）按 `docs/部署与运维.md` 的「停机切换」做：先 `moox-cli setup export-state` 导出 SCF 函数状态和各主机防火墙规则
（回滚时使用），暂停采集，一致性备份，逐台部署，调整防火墙和 SCF，恢复采集并确认暂停期间没有新批次。

## 认证与健康检查

浏览器控制请求使用 24 小时 JWT 会话加每个请求的 HMAC；部署时在控制主机首次生成并保存 JWT 密钥到 `secrets/admin-jwt.env`（0600）。服务之间的
调用使用带 nonce 防重放的服务签名。专用的健康端点使用独立的 health HMAC，部署写入 `secrets/health-auth.env`，未签名的探测返回 `401`。

SCF 采集函数的发布见 `docs/模块/采集.md`。Factor Engine 运行在操作员机器上，用 `scripts/deploy/deploy-factor-engine.sh` 安装，见 `docs/模块/因子.md`。
