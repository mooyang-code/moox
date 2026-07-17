# moox-cli

MooX 命令行工具，用于本地运维与数据初始化：用户注册、Storage 元数据 seed 导入、历史 CSV 导入、采集代码包辅助、腾讯云 Lighthouse 防火墙等。

## 命令概览

```bash
moox-cli metadata import ...        # 导入 Storage 元数据 seed
moox-cli metadata apply ...         # 创建并校验 Storage 元数据契约（不覆盖已有不兼容资源）
moox-cli storage import ...         # 导入历史 CSV 到已登记 Dataset
moox-cli data rows export ...       # 导出行数据
moox-cli collector function ...     # 采集 SCF 代码包打包/发布/部署辅助
moox-cli ops tencent lighthouse ... # 腾讯云 Lighthouse 防火墙规则
moox-cli setup ...                  # 初始化控制面、发布单个二进制、部署 Storage、导入元数据
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
```

单独发布任意已接入生命周期脚本的二进制服务时，先构建远端 Linux 目标，再由 CLI 通过 SSH 上传、原子替换、重启并执行健康检查：

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh admin
moox-cli setup deploy-binary \
  --file ./custom.toml \
  --host control \
  --service admin \
  --binary ./bin/moox-admin
```

`--name` 可用于覆盖远端 `bin` 目录中的文件名；默认取本地二进制文件名。Web Host
也可以使用专用便捷命令：

```bash
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh web-host
moox-cli setup deploy-web-host \
  --file ./custom.toml \
  --host control \
  --binary ./bin/moox-web-host
```

命令不会输出或拼接 SSH 密码；密码只在 CLI 进程内读取和使用。默认远端目录为
`~/moox/prod`，可通过 `--deploy-dir` 覆盖。

首次连接未知 SSH 主机时，先通过独立渠道核验命令报告的 SHA256 指纹，
再执行 `setup trust-host --host <name> --fingerprint <SHA256:...>`。初始化命令只在
进程内读取凭据；部署包、JSON 输出和命令参数均不携带这些凭据。`custom.toml`
是用户维护的只读输入，CLI 不修改或删除该文件。

控制面初始化完成后，再单独选择 Storage 主机和业务元数据。Admin、Gateway、
Web 固定部署在 `control_host`；Storage 的四个初始组件作为一个单元部署到明确
选择的 `control_host` 或 `other_hosts` 主机：

```bash
# 只输出主机名、地址、端口、用户名和角色，不输出密码
moox-cli setup hosts --file ./custom.toml

# --host 必须显式指定 custom.toml 中的主机名
moox-cli setup deploy-storage --file ./custom.toml --host compute

# 展示默认 seed 中可选的业务空间
moox-cli metadata spaces --file ./examples/metadata-quant-initial.seed.yaml

# 用户确认空间后，通过 Storage 主机的 SSH 隧道导入完整依赖闭包
moox-cli setup metadata-import \
  --file ./custom.toml \
  --storage-host compute \
  --seed ./examples/metadata-quant-initial.seed.yaml \
  --spaces stock_cn,crypto
```

`deploy-storage` 同机部署 `storage-access` 和统一的 `storage-view`，并更新控制面的 Storage 服务
位置。业务空间选择不写入 `custom.toml`；用户可以导入全部、部分或暂不导入。
自然语言理解由 MooX Skill 负责，CLI 始终接收明确的主机名和稳定 Space ID。

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
  auth_target: "127.0.0.1:11100"   # admin Auth HTTP 端口

storage:
  target: "127.0.0.1:20102"        # Storage Access tRPC；HTTP 元数据一般为 :20200
```

经网关访问时使用 `:11000`；直连服务时使用各进程 HTTP/tRPC 端口（见各模块 README）。

## 常用示例

### 元数据 seed 导入

```bash
moox-cli metadata import \
  --file ../../examples/metadata-quant-initial.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists \
  --spaces crypto

moox-cli metadata import --file ../../examples/metadata-quant-initial.seed.yaml --dry-run

moox-cli metadata apply \
  --file ../../examples/metadata-monitor-metrics.seed.yaml \
  --metadata-url http://127.0.0.1:20200
moox-cli metadata apply \
  --file ../../examples/metadata-monitor-metrics-local-route.seed.yaml \
  --metadata-url http://127.0.0.1:20200
```

### 历史 CSV 导入

```bash
moox-cli storage import \
  --format csv \
  --file ~/data/ARB-USDT.csv \
  --access-url http://127.0.0.1:20201 \
  --metadata-url http://127.0.0.1:20200 \
  --space crypto \
  --view ar_usdt_close_view \
  --dataset binance_spot_kline_1h \
  --subject ARB-USDT \
  --data-source binance \
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
