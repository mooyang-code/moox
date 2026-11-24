# moox-cli

运维命令行：初始化、部署、SCF 发布、元数据与历史数据导入、数据读取和诊断，以及各模块的管理命令（采集、因子、策略、交易、归档、事件中心、监控、生命周期）；没有专用命令的接口用 `gateway call`。

设计文档：[命令行工具](../../docs/模块/命令行工具.md)

## 构建与测试

```bash
./scripts/build/build.sh cli
./bin/moox-cli --help
go test -count=1 ./modules/cli/...
make test-deployment-packages
```

`setup deploy --host <主机> [--component a,b]` 把一台主机部署成 `moox.toml` 描述的样子，`setup install` 按顺序装完整个系统，`setup update` 覆盖更新已装好的系统（数据保留），`setup wipe` 卸载；`setup package-components` 给发布脚本按服务目录打每个组件一个包。详见[命令行工具](../../docs/模块/命令行工具.md)与[部署与运维](../../docs/部署与运维.md)。

## 配置

读取仓库根目录的 `moox.toml`；默认业务配置在 `config/setup/`。

经网关的命令（K 线读取等）使用 `gatewayclient` 隧道模式：先经 control 的 SSH 隧道读取 Directory，再按主机 ID 复用到对应主机 `127.0.0.1:11002` 的 SSH 隧道。SSH 地址与主机密钥取自 `moox.toml` 和操作员的已知主机库；Directory 中的公网地址不用于直连。关闭命令时释放目录连接、RPC 连接与 SSH 隧道，失效的 SSH 连接在下一次解析时重建。

操作员身份是 `~/.config/moox/moox-cli.key`：`moox-cli` 的 ed25519 签名私钥（0600 普通文件），只在这台电脑上生成。`setup operator` 生成它（已有就复用），
`setup install` 的第一步就是它；公钥在部署控制机时由 bootstrap 登记，已经装好的系统换电脑用 `setup operator --register`（经 SSH 在控制机上替换登记的公钥，原电脑随即不能用）。
同目录下的 `gateway-bootstrap.json`、`moox-ca.crt` 是部署控制机时记下的 MooX CA 指纹与根证书，之后取别的主机的身份包都按它校验。`setup wipe` 清空全部主机后把这三个文件一起删掉。

`data kline get --file /absolute/path/moox.toml --config /absolute/path/data-access.yaml ...` 使用前者选择 SSH 主机，后者提供数据集目录；`data skill kline get --config ...` 不走 SSH，用同一份文件里的外部入口与 `moox-skill` 身份（`gateway_client`）访问。`setup export-skill-config` 在本机生成 `moox-skill` 的签名私钥（存在 `~/.config/moox/moox-skill.key`，反复导出是同一把）、把公钥登记到控制机，并把私钥抄一份到输出文件旁边的 `.moox-skill-keys/`。Collector 清单使用对象调用，任务 CRUD 使用原始 JSON 转发，两者复用命令持有的同一个隧道客户端。

云账号、代码包与云函数的命令（`collector account`、`collector package`、`collector function`）调 Collector 的 CollectMgr，走同一条 SSH 隧道进主机网关。相关命令的 `--file` 默认 `./moox.toml`，用于选择可信 SSH 主机；COS 文件上传仍使用返回的预签名 HTTPS 地址。读回、删除与出口探测采用服务目录中的方法超时，命令退出统一关闭连接。

Admin 的密钥、Setup 与部署诊断复用命令持有的隧道客户端。`doctor bootstrap/diagnose --file <moox.toml>` 从同一操作员身份访问 SysDeploy；Monitor 与 Storage 管理入口同样复用这一客户端，命令结束时统一释放。

Doctor 的 Monitor 与 SysDeploy 查询共用命令持有的隧道客户端；本地 CLI 配置只保存 Doctor 选项，严格拒绝未知字段、重复键和额外 YAML 文档。

Metadata 的 `import/apply` 用 `--seed` 选择本地 seed 文件，历史数据导入 `moox-cli import` 用 `--path` 或 `--pack` 选择数据文件，两者都用 `--file ./moox.toml` 选择 SSH 主机；`data rows export --file ./moox.toml` 与 Doctor 同样经操作员网关访问原生 RPC。Storage 没有自己的调用方凭据，只靠网关的签名与访问控制。建连失败且请求未发出时最多重试一次；请求可能已发出后的传输重试只允许只读方法与服务目录明确列出的幂等写方法。其他写请求结果未知时返回错误，由操作员核对状态后再决定是否重跑。

部署登记通过 SysDeploy `SyncTopology` 把 `moox.toml` 的全部主机与部署（含启停状态）整份覆盖给 Admin，主机组件由 Admin 自动补齐。Doctor 只读取主机和组件部署，健康地址来自服务目录。
