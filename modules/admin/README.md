# moox-admin

控制面：登录认证、业务空间、服务目录、密钥、SSH、系统初始化和采集发布租约；管理台 API 的唯一入口，并为各节点网关下发路由。

设计文档：[管理后台](../../docs/模块/管理后台.md)

## 构建与测试

```bash
./scripts/build/build.sh admin
./scripts/build/build.sh admin-cli
go test -count=1 ./modules/admin/...
```

## 配置

`config/trpc_go.yaml`（服务与端口）、`config/app.yaml`、`config/console.yaml`（控制台）；schema 在 `schema/admin.sql`。

## 离线初始化与恢复

`moox-admin-cli bootstrap` 不依赖运行中的服务，参数为 `--topology-file`、`--db-path`、`--encryption-key-file`、`--pki-dir`、`--output-dir`。拓扑输入是部署 CLI 提取的 0600 JSON（版本 1），只含 `control_host_id` 和 `hosts`；每台主机包含 `host_id`、`address`、可选的 `private_address`、`region`、`description` 及完整业务 `components` 列表。主机组件由 DAO 自动维护，不能出现在该列表中。

命令建立 schema，按与在线 API 相同的校验在一个事务内写入拓扑和全部调用方密钥，验证或生成 MooX CA，提交后发布完整的 control 网关配置、证书与签名文件。输出只有包目录、相对路径、KeyID 和证书摘要等元数据。主密钥与 CA 始终留在持久目录；输出中的 `operator/caller-moox-cli.key` 只供部署端取回。Admin 启动时将 `MOOX_ADMIN_ENCRYPTION_KEY_FILE` 指向同一份持久主密钥。

重跑复用 CA、主密钥和调用方密钥，保留主机与组件启停状态；每次部署重新签发叶证书。已存在加密记录却丢失主密钥，或已有网关密钥却丢失 CA 时，必须恢复原件。数据库提交后的输出错误可直接重跑，失败暂存目录不会成为可安装包。

离线恢复使用 `host set-status` 或 `placement set-status`，两者均要求 `--db-path`、`--control-host-id`、`--host-id`、`--status enabled|disabled`；后者还要求 `--component-id`。数据库必须是已有的 0600 普通文件，恢复命令不会建表。受保护对象及目录校验规则与在线 API 相同。

bootstrap 已生成新主机网关格式；运行端接入和实际首次部署流程仍由执行计划 C/G/J 验收。

## 证书巡检

Admin 启动时及每天检查 MooX CA 和数据库中全部主机的公有证书清单（包括停用主机），检查有效期、CA 签名、服务端用途及主机 ID/公网/私网 SAN。证书在 90 天内到期时发出 warning；缺失、损坏、过期或不匹配时发出 critical，同轮发现按级别汇总。EventBus CA 仍独立检查。

`MOOX_ADMIN_PKI_DIR` 指向 bootstrap 的持久 `--pki-dir`；未设置时使用 Admin 主密钥文件同目录下的 `pki/`，未提供主密钥文件路径时默认为 `../secrets/pki`。巡检只读取 `ca.crt` 与 `hosts/<host-id>.crt`，不读取私钥或签名密钥，不创建或修复文件。首次初始化时其他主机可能尚未签发证书，这类发现会告警并保留定时巡检，允许控制面启动，以便后续部署和换发证书。
