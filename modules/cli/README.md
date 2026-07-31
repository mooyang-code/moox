# moox-cli

MooX 命令行工具，用于本地运维与数据初始化：用户注册、Storage 元数据 seed 导入、历史 CSV 导入、采集代码包辅助、腾讯云 Lighthouse 防火墙等。

## 命令概览

```bash
moox-cli metadata import ...        # 导入 Storage 元数据 seed
moox-cli metadata apply ...         # 创建并校验 Storage 元数据契约（不覆盖已有不兼容资源）
moox-cli setup init ...             # 一次初始化 Admin 空间、Storage 元数据与 Dataset
moox-cli storage import ...         # 导入历史 CSV 到已登记 Dataset
moox-cli data rows export ...       # 导出行数据
moox-cli collector function ...     # 采集 SCF 代码包打包/发布/部署辅助
moox-cli ops tencent lighthouse ... # 腾讯云 Lighthouse 防火墙规则
moox-cli setup ...                  # 初始化控制面、发布服务包、部署 Storage、导入元数据
```

中文别名：`认证`、`注册`、`存储`（见各子命令 `--help`）。

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
```

`deploy-storage` 同机部署 `storage-primary` 和统一的 `storage-view`，并更新控制面的 Storage 服务
位置。在 macOS 上发布 Linux Storage 时，CLI 自动通过 `compile_host` 构建 CGO
二进制后再打包。`setup init` 固定从配置目录读取 `metadata.yaml`，把
`stock_cn`、`crypto` 写入 Admin，把它们和内部 `moox_system` 元数据写入
Storage。已有资源逐字段一致时记为 unchanged，不一致时停止且不覆盖。

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
  --spaces crypto

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
  --space crypto \
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
