# moox-cli

MooX 命令行工具，用于本地运维与数据初始化：用户注册、Storage 元数据 seed 导入、历史 CSV 导入、采集代码包辅助、腾讯云 Lighthouse 防火墙等。

## 命令概览

```bash
moox-cli metadata import ...        # 导入 Storage 元数据 seed
moox-cli metadata apply ...         # 创建并校验 Storage 元数据契约（不覆盖已有不兼容资源）
moox-cli setup init ...             # 一次初始化 Admin 空间、Storage 元数据与 Dataset
moox-cli storage import ...         # 导入历史 CSV 到已登记 Dataset
moox-cli storage repair-view ...    # 清理 View durable consumer 积压并触发 A/B 重建
moox-cli storage reset-view-consumers ... # 删除全部 View durable/消息/索引并从 Primary 回溯重建
moox-cli factor clear-queue ...     # 清空 Factor durable consumer 历史积压并重启 Factor
moox-cli data rows export ...       # 导出行数据
moox-cli collector function ...     # 采集 SCF 代码包打包/发布/部署辅助
moox-cli ops tencent lighthouse ... # 腾讯云 Lighthouse 防火墙规则
moox-cli setup ...                  # 初始化控制面、发布服务包、部署 Storage、导入元数据
```

中文别名：`认证`、`注册`、`存储`（见各子命令 `--help`）。

### View 自助修复

项目尚未上线或需要彻底丢弃 View 历史时，可先预览再执行一次性分区清理：

```bash
moox-cli storage reset-view-consumers \
  --storage-conf /home/ubuntu/moox/storage/config/storage.yaml \
  --package-root /home/ubuntu/moox/storage \
  --lookback 24h --dry-run
moox-cli storage reset-view-consumers \
  --storage-conf /home/ubuntu/moox/storage/config/storage.yaml \
  --package-root /home/ubuntu/moox/storage \
  --lookback 24h --yes
```

命令停止整个 Storage 生命周期（Primary、DataNode、View），删除旧的
`storage_view_period_v1`/`storage_view` 与新的 Kline、metrics、other durable，并清理
所有配置 Dataset 的精确 Storage subjects。该命令是破坏性“新一代”操作：Record/Bleve
View 也会删除 A/B 索引和元数据，历史记录不保留；重启后只接收清理完成后的新事件。时序 View
时序 View 会按 Storage 配置中的 `rebuild_lookback_periods` 从 Primary 回溯（默认所有频率 `1000` 根；
根目录 `custom.toml` 的 `[storage_view] rebuild_lookback_periods` 可统一配置），未达到对应根数不会激活；`--lookback` 仅覆盖没有
frequency 的旧 View 兼容兜底。默认不删 Primary 事实数据；只有明确传
`--reset-all-storage-data` 才会停止全 Storage 并删除 Primary/DataNode Pebble 数据，此模式
只等待服务健康，不要求不存在的历史回溯水位。若 purge 或索引清理中途失败，命令会保留 Storage
停止状态并返回非零，避免在已部分删除历史后自动启动一个不一致的 View；修复原因后重新执行命令。

当 Storage View 因 durable consumer 积压、重建失败或旧索引占用空间而停止追赶时，
可在 Storage 主机上执行：

```bash
moox-cli storage repair-view \
  --storage-conf /home/ubuntu/moox/storage/config/storage.yaml \
  --package-root /home/ubuntu/moox/storage \
  --space-id crypto_market \
  --view-id binance_spot_kline_1m_factor \
  --yes
```

默认流程会停止并重启 `storage-view`、删除指定 durable consumer、
备份 Metadata SQLite，并递增 View desired revision，让服务走正常的 A/B 构建和切换；
active 索引不会被删除。NATS 删除 consumer 需要 EventBus internal-admin 凭据，使用
`--credential-file` 或 `MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE` 指定，命令不会输出凭据。
先执行 `--dry-run` 可只检查 View 和将要执行的动作。

### Factor 队列积压清理

当 Factor 因历史 `ViewSourcePeriodReady` 事件积压而长期重算旧周期时，可在部署根目录执行：

```bash
moox-cli factor clear-queue \
  --package-root /home/ubuntu/moox/prod \
  --credential-file /home/ubuntu/.config/moox/eventbus/internal-admin.yaml \
  --yes
```

命令会先停止 Factor，读取并删除 `MOOX_STORAGE/factor_view_ready_v1` durable consumer，
再启动 Factor。新建 consumer 使用 `DeliverNew`，因此只处理清理完成后的新事件；命令不会删除
View、Factor 结果或其他数据。可先用 `--dry-run` 检查参数，若不希望自动重启可传
`--restart=false`。若部署环境无法从 `moox-factor-cli` 自动定位根目录，请显式传
`--package-root`；NATS 管理凭据也可通过 `MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE` 提供。

全量丢弃 View 历史时优先使用上面的 `reset-view-consumers`。它直接从保留的 Primary
事实数据回溯构建，不依赖 JetStream 旧消息，也不会误删 Primary。

## 控制面初始化

在仓库根目录根据 `custom.toml.example` 创建权限为 `0600` 的
`custom.toml`，然后依次执行：

```bash
moox-cli setup validate --file ./custom.toml
moox-cli setup deploy-control --file ./custom.toml
moox-cli setup apply --file ./custom.toml
moox-cli setup status --file ./custom.toml
moox-cli setup e2e-eventbus --file ./custom.toml
```

`[tencent_cloud]` 中的 `secret_id`/`secret_key` 是腾讯云 API 凭据，也用于
访问 CLS；不要在仓库或部署包中重复保存 SecretKey。CLS Logset/Topic 是初始化后
由云端生成的资源，不写入 `custom.toml`。启用 CLS 的发布会运行
`moox-cli ops tencent cls prepare`，并在部署根目录生成只读的
`config/resources.env`，供各服务统一读取：

```dotenv
MOOX_CLS_ACCOUNT_ID='<cloud account id>'
MOOX_CLS_REGION='ap-guangzhou'
MOOX_CLS_HOST='ap-guangzhou.cls.tencentyun.com'
MOOX_CLS_LOGSET_ID='<resolved logset id>'
MOOX_CLS_TOPIC_ID='<resolved topic id>'
```

该文件不包含 SecretId/SecretKey，服务启动脚本会自动加载；凭据仍只通过受保护的
`secrets/cls.env` 或云账户凭据链提供。需要诊断资源时使用
`moox-cli ops tencent cls resolve --region ap-guangzhou`，不要把返回的 ID 回写到
`custom.toml`。

`setup deploy-control` also installs the managed Caddy edge, selects the
certificate trust model, performs HTTPS acceptance, and installs the
healthcheck that keeps Caddy available for automatic renewal. Public IP/DNS
targets use Let's Encrypt certificates trusted by normal browsers; private or
loopback targets use Caddy internal CA. The command's sanitized JSON includes
`certificate.mode`, `certificate.issuer`, and `certificate.automatic_renewal`.
No certificate private key is printed or copied into the release package.

`[eventbus]` 只填写 Collector SCF 能访问的公网 IPv4/DNS、端口和
`tls_enabled = true`。EventBus 用户名、token、私有 CA 和
`cloudnode-worker.yaml` 由部署流程生成，不写入 `custom.toml`。控制面部署单元包含
Admin、Gateway、Web、EventBus、CloudNode 和 Collector。

`[monitoring].wecom_webhook` 填写企微群机器人 HTTPS webhook；留空时 Monitor
仍采集和计算状态，但不发送站外告警。标准服务、健康 URL 和实时 Dataset 清单不写入
`custom.toml`：标准服务由 SysDeploy 维护，启用中的 TimeSeries Dataset + Frequency
由运行时自动对账。需要 CPU、内存和磁盘监控的每台机器仍需部署 HostAgent。

`deploy-control` 默认保留控制面数据。仅在允许删除 Admin、EventBus 等全部控制面
数据并重新初始化时使用 `--reset-data`；凭据目录和部署密钥仍会保留。
`e2e-eventbus` 从本机经公网 TLS 连接 EventBus，验证 CloudNode worker 只能绑定、
拉取和确认既有作业消费者，不能创建消费者或发布作业事件。

`setup validate` performs the full Tencent Cloud STS identity check. The
`deploy-control` and `deploy-storage` commands only repeat immutable-config
and SSH host validation; copying and starting MooX binaries does not require a
Tencent Cloud API call. Tencent credentials are required when applying the
cloud account or running other cloud-resource operations.

服务发布以 ZIP 包为单位，包中包含二进制、配置和生命周期脚本。示例包目录必须至少包含
`bin/`、`config/`、`start.sh`、`stop.sh` 和 `healthcheck.sh`，使用仓库脚本打包：

```bash
./scripts/package-service.sh \
  --service-dir ./release/service-package \
  --output ./release/moox-admin-linux-amd64.zip
moox-cli setup deploy-service \
  --file ./custom.toml \
  --host control \
  --service admin \
  --package ./release/moox-admin-linux-amd64.zip
```

发布成功后，CLI 会以幂等方式把服务写入 Admin 的 `t_service_deployments`。
Monitor 会从该目录同步系统服务检查，因此服务总览无需再手工创建服务记录。

包内路径必须是相对路径，不能包含 `data/`、`logs/`、`run/`、`secrets/` 或 `certs/`。
凭据不得打入 ZIP 包；远端已有的凭据和运行数据由 CLI 保留。默认远端目录为
`~/moox/prod`，可通过 `--deploy-dir` 覆盖。

命令不会输出或拼接 SSH 密码；密码只在 CLI 进程内读取和使用。

首次连接未知 SSH 主机时，先通过独立渠道核验命令报告的 SHA256 指纹，
再执行 `setup trust-host --host <name> --fingerprint <SHA256:...>`。初始化命令只在
进程内读取凭据；部署包、JSON 输出和命令参数均不携带这些凭据。`custom.toml`
是用户维护的只读输入，CLI 不修改或删除该文件。

控制面初始化完成后，再选择 Storage 主机并导入默认业务元数据。Admin、Gateway、Web、
EventBus、CloudNode 和 Collector 固定部署在 `control_host`；Storage 的四个初始组件作为一个单元部署到明确
选择的 `control_host` 或 `other_hosts` 主机：

```bash
# 只输出主机名、地址、端口、用户名和角色，不输出密码
moox-cli setup hosts --file ./custom.toml

# --host 必须显式指定 custom.toml 中的主机名
moox-cli setup deploy-storage --file ./custom.toml --host compute

# 同步 Admin 业务空间和 Storage 元数据，并激活通过检查的 Dataset
moox-cli setup init \
  --file ./custom.toml \
  --config-dir ./examples/setup/default \
  --storage-host compute

# 仅导入/更新因子，不重新校验或写入 Storage 元数据
moox-cli setup factors --file ./custom.toml
```

如果 `custom.toml` 启用了 `[factors]`，同一个 `setup init` 还会从
`factors.source_dir` 读取 Python 因子，调用 FactorMgr 导入定义、建立绑定并启用因子。
仓库的 `custom.toml.example` 已给出 `bias`、`cci` 到
`crypto_market/binance_spot_kline_1m_view` 的默认配置；修改
`[[factors.items]]` 的 `space_id`、`source_view_id`、`freq` 和参数即可切换默认关联。
重复执行时同源文件和同运行契约会报告 unchanged；如果源码或输入/输出/参数契约不同，命令会停止而不会静默覆盖已有因子。修改同一因子的默认 View 或频率后再次执行，会删除此前由 `setup factors` 创建的旧绑定。

`deploy-storage` 同机部署 `storage-primary` 和统一的 `storage-view`，并更新控制面的 Storage 服务
位置。在 macOS 上发布 Linux Storage 时，CLI 自动通过 `compile_host` 构建 CGO
二进制后再打包。`setup init` 固定从配置目录读取 `metadata.yaml`，把
`stock_cn`、`crypto_market` 写入 Admin，把它们和内部 `moox_system` 元数据写入
Storage。已有资源逐字段一致时记为 unchanged，不一致时停止且不覆盖。

如果 Storage 已经初始化过且当前 seed 与线上元数据不同，`setup init` 会按设计停止；此时使用
`setup factors` 可以只补齐因子定义和 View 绑定，不会触碰现有 Storage 元数据。

`deploy-storage` 成功启动 Storage 后会自动安装并启用每 10 秒检查一次的
`systemd` watchdog；如需只补装或更新 watchdog，可执行：

```bash
moox-cli setup install-storage-watchdog --file ./custom.toml --host compute
```

`metadata spaces` 和 `setup metadata-import` 保留给只导入部分业务空间的高级操作；
标准新系统初始化不需要逐个选择 YAML 或 Space。

### Storage Schema v6 验证

当前 scalar `series_tag` 使用 Schema v6。只有在用户明确确认可破坏性重建的环境后才允许
使用 `--reset-storage-data`：

```bash
moox-cli setup deploy-storage \
  --file ./custom.toml \
  --host compute \
  --reset-storage-data
```

该选项默认关闭，并会清除远端 `~/moox/storage/data` 后重新初始化 Storage；远端
`secrets/` 保留。它不是生产迁移或日常重部署选项，也不会修改 `custom.toml`。

部署完成后可以使用三条边界明确的验证命令：

```bash
moox-cli setup verify-storage --file ./custom.toml --host compute
moox-cli setup e2e-storage --file ./custom.toml --host compute --namespace codex-storage
moox-cli setup browser-e2e-storage --file ./custom.toml --host compute --repo-root .
```

`verify-storage` 通过 CLI 管理的 SSH 隧道检查组件就绪、Schema v6、二进制哈希、
签名 DataNode 身份以及 Dataset 汇总，并只输出脱敏的状态、ID、数量和版本信息。
`e2e-storage` 使用调用方提供的短命名空间创建禁用 Dataset，执行激活自检和 revision
激活，再通过支持的接口清理临时 Space、DataSource 和 Dataset；即使断言失败也会报告
清理结果。命名空间必须是安全的短标识符。

`browser-e2e-storage` 只启动远端 Storage 管理台 Playwright 用例，覆盖桌面和 390px
移动视口的 DataNode/Dataset 页面、详情、Info 提示和激活自检。登录材料由 setup CLI
通过子进程 stdin 传给 global setup，只在验证进程内存中使用；不会出现在 argv、日志、
临时文件、截图、trace、video 或 Playwright `storageState` 中。三个命令都要求显式
指定 Storage 主机，`custom.toml` 只能由 setup CLI 读取且始终保持不变。

## 构建

```bash
# 模块目录
make build

# 仓库根目录
./scripts/build.sh cli
# 产物：bin/moox-cli
```

```bash
go run ./cmd/moox-cli --help
```

## 配置

配置文件按优先级加载：

1. 环境变量 `MOOX_CONFIG`
2. `./config/cli.yaml`、`./cli.yaml`
3. `../config/cli.yaml`
4. `/etc/moox/cli.yaml`
5. `~/.moox/cli.yaml`

示例 `config/cli.yaml`：

```yaml
moox:
  auth_target: "127.0.0.1:11100" # admin Auth HTTP 端口

storage:
  target: "127.0.0.1:20102" # Storage Access tRPC；HTTP 元数据一般为 :20200
```

经网关访问时使用 `:11000`；直连服务时使用各进程 HTTP/tRPC 端口（见各模块 README）。

## 常用示例

### 元数据 seed 导入

```bash
moox-cli metadata import \
  --file ../../examples/setup/default/metadata.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists \
  --spaces crypto_market

moox-cli metadata import --file ../../examples/setup/default/metadata.yaml --dry-run

moox-cli metadata apply \
  --file ../../examples/setup/default/metadata.yaml \
  --metadata-url http://127.0.0.1:20200
```

DataNode 的注册和 `service_target` 由 `setup deploy-storage` 的部署流程完成；元数据
seed 只声明 Dataset 的直接绑定，不再单独维护节点或路由 seed。

### 历史 CSV 导入

> `--series-tag` 使用当前 scalar tag 契约；空值精确写入默认序列，Storage 不解析
> `venue:binance` 等字符串。

```bash
moox-cli storage import \
  --format csv \
  --file ~/data/ARB-USDT.csv \
  --access-url http://127.0.0.1:20201 \
  --metadata-url http://127.0.0.1:20200 \
  --space crypto_market \
  --view ar_usdt_close_view \
  --dataset spot_kline_1h \
  --subject ARB-USDT \
  --data-source crypto_market \
  --series-tag venue:binance \
  --freq 1h \
  --time-column candle_begin_time
```

### 腾讯云防火墙（开放 MooX 端口）

直接使用本地腾讯云密钥：

```bash
export TENCENTCLOUD_SECRET_ID="..."
export TENCENTCLOUD_SECRET_KEY="..."

moox-cli ops tencent lighthouse firewall add \
  --region ap-guangzhou \
  --public-ip <lighthouse-ip> \
  --ports 11000,20200,20201 \
  --protocol TCP \
  --dry-run
```

通过控制面读取云账户凭证：

```bash
moox-cli ops tencent lighthouse firewall open \
  --control-url http://<control-host>:11000 \
  --service-access-key moox-service \
  --service-secret-key <secret> \
  --public-ip <lighthouse-ip> \
  --ports 11000,10080,20200,20201,20202 \
  --dry-run
```

`open` 命令会通过 `/api/service/cloudnode/*` 读取云账户凭证，再复用 `firewall add` 的腾讯云调用逻辑。

## 目录结构

```text
cmd/
  moox-cli/main.go      入口
  auth.go               认证
  metadata.go           元数据导入
  storage.go            storage 子命令
  storage_import.go     CSV 导入
  data.go               行导出
  collector.go          采集代码包
  tencent_ops*.go       腾讯云运维与控制面凭证模式
internal/
  config/               配置加载
  setup/                安全初始化、SSH、部署和 Admin 私有客户端
  adminclient/          Admin / CloudNode HTTP 客户端
config/cli.yaml         默认配置
```

## 依赖关系

- Storage 元数据/写入：`modules/storage/proto/storagegen`
- Admin 认证：`modules/admin/proto/admingen`
- 采集打包：CLI 内部 `internal/collectorpackager` 生成 collector SCF zip，不直接依赖 `modules/collector` 实现包

Go 1.24+，基于 Cobra + tRPC-Go 客户端。
