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

`setup package --profile <host|control|storage|access|egress-proxy|trade> --binary-dir <目标制品目录> --output <包.tar.gz>` 打包已编译的 Linux 软件和 Git 跟踪的配置模板；`setup package inspect <包.tar.gz>` 校验组件、平台、文件与摘要。这两个命令不加载 `moox.toml`。当前软件包尚待安装器接入运行配置、身份材料和启停脚本，详见[按组件边界打包](../../docs/模块/命令行工具.md#13-按组件边界打包)。

## 配置

读取仓库根目录的 `moox.toml`；默认业务配置在 `config/setup/`。

K 线读取和 SCF 金丝雀的 Storage 核验使用 `gatewayclient` 隧道模式：先经 control 的 SSH 隧道读取 Directory，再按主机 ID 复用到对应主机 `127.0.0.1:11002` 的 SSH 隧道。SSH 地址与主机密钥取自 `moox.toml` 和操作员的已知主机库；Directory 中的公网地址不用于直连。关闭命令时释放目录连接、RPC 连接与 SSH 隧道，失效的 SSH 连接在下一次解析时重建。

操作员身份安装为 `~/.config/moox/gateway-client.yaml` 与 `caller-moox-cli.key`，均为 0600 普通文件。Admin bootstrap 的 `operator/` 目录一并导出这两个文件；实际取回安装由部署阶段完成。配置中的 KeyID 必须使用 Admin 返回的值：

```yaml
caller: moox-cli
key_id: <Admin 分配的公开 KeyID>
key_file: caller-moox-cli.key
```

`data kline get --file /absolute/path/moox.toml --config /absolute/path/data-access.yaml ...` 使用前者选择 SSH 主机，后者只包含原 Storage 读取角色凭据与数据集目录。`setup export-skill-config` 不再读取或导出网关签名密钥；旧配置中的 `gateway` 字段必须通过重新导出移除。Collector 清单使用对象调用，任务 CRUD 使用原始 JSON 转发；两者复用金丝雀命令持有的同一 SSH 网关客户端，不再访问旧 CollectMgr HTTP 路由。其他 CLI 路径按相应 D2/G 阶段迁移。

CloudNode 的节点、账号、代码包登记、发布、批次查询与函数调用均使用同一 SSH 网关入口。相关命令的 `--file` 默认 `./moox.toml`，用于选择可信 SSH 主机；COS 文件上传仍使用返回的预签名 HTTPS 地址。函数调用采用服务目录中的超时预算，命令取消后的发布清理可继续复用隧道，命令退出统一关闭连接。Admin 的租约与密钥方法使用同一 SSH 客户端。

Admin 的密钥、发布租约、Setup、部署诊断和浏览器测试空间管理复用命令持有的 SSH 网关客户端，不再转发 11107、11109 或 11110 的 HTTP 监听。`doctor bootstrap/diagnose --file <moox.toml>` 从同一操作员身份访问 SysDeploy；Monitor 与 Storage 管理入口同样复用这一客户端。发布和部署回滚在操作取消后仍可用独立清理上下文复用连接；命令结束时统一释放。

Doctor 的 Monitor 与 SysDeploy 查询共用命令持有的 SSH 原生网关客户端；本地 CLI 配置只保存 Doctor 选项，严格拒绝旧 RPC 目标、未知字段、重复键和额外 YAML 文档。

Metadata 的 `import/apply` 与 Storage `import` 用 `--file` 选择本地输入文件，另用 `--manifest ./moox.toml` 选择 SSH 主机；`data rows export --file ./moox.toml`、Doctor、Storage 验证和 Factor 初始化均经操作员网关访问原生 RPC。旧 `--metadata-url`、`--access-url`、`--storage-url` 和直连回退已删除。导入、导出及 Collector 清理仍使用独立 Storage 角色鉴权；通过 `--storage-auth-file` 或 `MOOX_STORAGE_PRIMARY_AUTH_SECRET` 提供，不复用网关签名密钥。写请求只发送一次，结果未知时返回错误，由操作员核对状态后再决定是否重跑。
