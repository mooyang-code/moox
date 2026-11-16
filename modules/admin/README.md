# moox-admin

控制面：登录认证、业务空间、服务目录、主机登记与部署、密钥、控制台终端（SSH）和系统初始化；管理台 API 的唯一入口，并把登记的主机、部署与公钥下发给各主机的网关（路由由网关自己算）。

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

`moox-admin-cli bootstrap` 不依赖运行中的服务，参数为 `--topology-file`、`--db-path`、`--encryption-key-file`、`--pki-dir`、`--output-dir`、`--operator-public-key`，
以及可选的 `--current-sign-public-key`、`--current-scf-collector-public-key`（目标机上现有私钥的公钥）。拓扑输入是部署 CLI 提取的 0600 JSON（版本 1），只含 `control_host_id` 和 `hosts`；每台主机包含 `host_id`、`address`、可选的 `private_address`、`region`、`description` 及完整业务 `components` 列表。主机组件由 DAO 自动维护，不能出现在该列表中。

命令建立 schema，按与在线 API 相同的校验在一个事务内写入拓扑、登记操作电脑的 `moox-cli` 公钥（只在还没登记时写入，重复执行不改）、决定控制机的签名密钥，验证或生成 MooX CA，提交后发布控制机的身份包。
签名密钥的规则：目标机上现有私钥的公钥和登记的一致就原样沿用，包里没有私钥；不一致或没有时生成一把新的 ed25519 私钥、登记公钥，私钥只写进这一次的身份包，Admin 不保留。
部署了 collector 的主机另管 `scf-collector` 的私钥，规则相同。输出只有包目录、公钥、证书摘要和逐文件摘要等元数据，包中的 `bundle.json` 保存同一份清单。主密钥与 CA 始终留在持久目录。
Admin 启动时将 `MOOX_ADMIN_ENCRYPTION_KEY_FILE` 指向同一份持久主密钥，`MOOX_ADMIN_HOST_KEY_FILE` 指向控制机的主机签名私钥（管理台以 `console` 的身份用它签名）。

重跑复用 CA、主密钥和已登记的公钥，保留主机与组件启停状态；每次部署重新签发主机证书。已存在加密记录却丢失主密钥，或已经登记过签名公钥却丢失 CA 时，必须恢复原件。
数据库提交后的输出错误可直接重跑：这次生成的私钥没有送到目标机，下一次目标机上的公钥和登记的对不上，会再签发一把。失败暂存目录不会成为可安装包。

后续主机部署先通过 `SyncHostDeployments` 登记，再经 control 的可信 SSH 执行 `moox-admin-cli host-bundle --host-id <目标主机> --control-host-id <控制主机> --db-path <现有数据库> --encryption-key-file <持久主密钥> --pki-dir <持久CA目录> --output-dir <私密输出父目录> [--current-sign-public-key <公钥>] [--rotate-sign-key]`。
该命令只使用现有数据库和 CA，从持久拓扑读取目标地址与完整部署，按上面的规则决定这台主机的签名密钥（`--rotate-sign-key` 显式更换），再为本次部署重签主机证书并发布新的 0700 包；所有文件为 0600。
停用主机的材料也可签发，启停状态保持不变。未登记主机、错误控制主机、丢失或损坏的 CA 都会失败，不发布任何包。

身份包里只有：`host.key`（新签发时）、`host.crt`、`host.tls.key`、`moox-ca.crt`，控制机另有 `scf-collector.key`（新签发时），以及清单 `bundle.json`。
CA 私钥和 Admin 主密钥不进入任何输出包。`bundle.json` 的 `files` 覆盖全部载荷文件（自身除外），记录 SHA256 与长度，供部署端传输时核对。
`moox-admin-cli keys set|list|delete` 登记、列出、删除不属于主机的身份的公钥（`moox-cli`、`scf-collector`、`moox-skill`、`collector-operator`）；组件名与 `console` 不能登记成身份。

离线恢复使用 `host set-status` 或 `deployment set-status`，两者均要求 `--db-path`、`--control-host-id`、`--host-id`、`--status enabled|disabled`；后者还要求 `--component-id`。数据库必须是已有的 0600 普通文件，恢复命令不会建表。受保护对象及目录校验规则与在线 API 相同。

`moox-cli setup deploy` 经 SSH 从控制机取回身份包，校验摘要后把身份与配置装到目标机，并安装操作员身份；部署操作见[部署与运维](../../docs/部署与运维.md)。代码能力不代表任一环境已经完成部署，实际状态以部署记录与就绪检查为准。

## 证书巡检

Admin 启动时及每天检查 MooX CA 和数据库中全部主机的公有证书清单（包括停用主机），检查有效期、CA 签名、服务端用途及主机 ID/公网/私网 SAN。证书在 90 天内到期时发出 warning；缺失、损坏、过期或不匹配时发出 critical，同轮发现按级别汇总。EventBus CA 仍独立检查。

`MOOX_ADMIN_PKI_DIR` 指向 bootstrap 的持久 `--pki-dir`；未设置时使用 Admin 主密钥文件同目录下的 `pki/`，未提供主密钥文件路径时默认为 `../secrets/pki`。主机证书巡检只读取 `ca.crt` 与 `hosts/<host-id>.crt`，不读取私钥或签名密钥，不创建或修复主机证书。同一个定时任务还检查 9527 的网页证书：这一步会读 CA 私钥 `ca.key`，需要时重签并写入数据目录的 `web-cert/`（不动 `hosts/` 与主机证书）。首次初始化时其他主机可能尚未签发证书，这类发现会告警并保留定时巡检，允许控制面启动，以便后续部署和换发证书。

Admin 的 Auth、Ssh、SpaceMgr、SecretMgr、SysDeploy、Setup、GatewayControl 与 ActivityLog 八个内部服务仅在 loopback 提供原生 tRPC，端口为 11100、11106～11110、11112、11113；Setup 启动预检同时校验此协议。健康接口使用 HTTP。浏览器入口是 Admin 自己监听的 HTTPS 9527（`internal/webserver`：管理台页面、`/ca/*` 根证书下载与 `/api/admin/*`；控制机地址 `MOOX_ADMIN_WEB_ADDRESS`，监听地址 `MOOX_ADMIN_WEB_LISTEN`，网页证书由 `MOOX_ADMIN_PKI_DIR` 的 MooX CA 签、存在数据目录的 `web-cert/`）。浏览器调用由生成的 handler 在进程内分派，机器调用经主机网关校验各自身份。
