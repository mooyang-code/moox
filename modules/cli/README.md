# moox-cli

运维命令行：初始化、部署、SCF 发布、元数据与历史数据导入、数据读取和诊断。

设计文档：[命令行工具](../../docs/模块/命令行工具.md)

## 构建与测试

```bash
./scripts/build/build.sh cli
./bin/moox-cli --help
go test -count=1 ./modules/cli/...
```

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

CloudNode 的节点、账号、代码包登记、发布、批次查询与函数调用均使用同一 SSH 网关入口。相关命令的 `--file` 默认 `./moox.toml`，用于选择可信 SSH 主机；COS 文件上传仍使用返回的预签名 HTTPS 地址。函数调用采用服务目录中的超时预算，命令取消后的发布清理可继续复用隧道，命令退出统一关闭连接。Admin 的租约与密钥方法继续随 D2c 迁移。

Admin 的密钥、发布租约、Setup、部署诊断和浏览器测试空间管理复用命令持有的 SSH 网关客户端，不再转发 11107、11109 或 11110 的 HTTP 监听。`doctor bootstrap/diagnose --file <moox.toml>` 从同一操作员身份访问 SysDeploy；Monitor 调用在 D2e 迁移，Storage 管理入口在 D2f 迁移。发布和部署回滚在操作取消后仍可用独立清理上下文复用连接；命令结束时统一释放。
