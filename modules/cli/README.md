# moox-cli

运维命令行：初始化、部署、SCF 发布、元数据与历史数据导入、数据读取和诊断。

设计文档：[命令行工具](../../docs/模块/命令行工具.md)

## 构建与测试

```bash
./scripts/build/build.sh cli
./bin/moox-cli --help
go test -count=1 ./modules/cli/...
make test-deployment-packages
```

`setup deploy --host <主机> [--component a,b]` 把一台主机部署成 `moox.toml` 描述的样子，`setup install` 按顺序装完整个系统；`setup package-components` 给发布脚本按服务目录打每个组件一个包。详见[命令行工具](../../docs/模块/命令行工具.md)与[部署与运维](../../docs/部署与运维.md)。

## 配置

读取仓库根目录的 `moox.toml`；默认业务配置在 `config/setup/`。

K 线读取和 SCF 金丝雀的 Storage 核验使用 `gatewayclient` 隧道模式：先经 control 的 SSH 隧道读取 Directory，再按主机 ID 复用到对应主机 `127.0.0.1:11002` 的 SSH 隧道。SSH 地址与主机密钥取自 `moox.toml` 和操作员的已知主机库；Directory 中的公网地址不用于直连。关闭命令时释放目录连接、RPC 连接与 SSH 隧道，失效的 SSH 连接在下一次解析时重建。

操作员身份是 `~/.config/moox/moox-cli.key`：`moox-cli` 的 ed25519 签名私钥（0600 普通文件），只在这台电脑上生成。`setup operator` 生成它（已有就复用），
`setup install` 的第一步就是它；公钥在部署控制机时由 bootstrap 登记，已经装好的系统换电脑用 `setup operator --register`（经 SSH 在控制机上替换登记的公钥，原电脑随即不能用）。
同目录下的 `gateway-bootstrap.json`、`moox-ca.crt` 是部署控制机时记下的 MooX CA 指纹与根证书，之后取别的主机的身份包都按它校验。`setup wipe` 清空全部主机后把这三个文件一起删掉。

`data kline get --file /absolute/path/moox.toml --config /absolute/path/data-access.yaml ...` 使用前者选择 SSH 主机，后者只包含 Storage 读取角色凭据与数据集目录；`setup export-skill-config` 在本机生成 `moox-skill` 的签名私钥（存在 `~/.config/moox/moox-skill.key`，反复导出是同一把）、把公钥登记到控制机，并把私钥抄一份到输出文件旁边的 `.moox-skill-keys/`。Collector 清单使用对象调用，任务 CRUD 使用原始 JSON 转发，两者复用金丝雀命令持有的同一 SSH 网关客户端。

云账号、代码包与云函数的命令（`collector account`、`collector package`、`collector function`）调 Collector 的 CollectMgr，使用同一 SSH 网关入口。相关命令的 `--file` 默认 `./moox.toml`，用于选择可信 SSH 主机；COS 文件上传仍使用返回的预签名 HTTPS 地址。读回、删除与出口探测采用服务目录中的方法超时，命令退出统一关闭连接。

Admin 的密钥、Setup、部署诊断和浏览器测试空间管理复用命令持有的 SSH 网关客户端。`doctor bootstrap/diagnose --file <moox.toml>` 从同一操作员身份访问 SysDeploy；Monitor 与 Storage 管理入口同样复用这一客户端。发布和部署回滚在操作取消后仍可用独立清理上下文复用连接；命令结束时统一释放。

Doctor 的 Monitor 与 SysDeploy 查询共用命令持有的 SSH 原生网关客户端；本地 CLI 配置只保存 Doctor 选项，严格拒绝未知字段、重复键和额外 YAML 文档。

Metadata 的 `import/apply` 与 Storage `import` 用 `--file` 选择本地输入文件，另用 `--manifest ./moox.toml` 选择 SSH 主机；`data rows export --file ./moox.toml`、Doctor 和 Factor 初始化均经操作员网关访问原生 RPC。Storage 请求不再带自己的口令，只靠网关的签名与访问控制。建连失败且请求未发出时最多重试一次；请求可能已发出后的传输重试只允许只读方法与服务目录明确列出的幂等写方法。其他写请求结果未知时返回错误，由操作员核对状态后再决定是否重跑。

部署登记通过 SysDeploy `SyncHostDeployments` 同步主机的完整业务组件列表，主机组件由 Admin 自动补齐并保留既有启用/停用状态。Doctor 只读取主机和组件部署，健康地址来自服务目录。
