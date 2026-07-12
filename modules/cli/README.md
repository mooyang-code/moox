# moox-cli

MooX 命令行工具，用于本地运维与数据初始化：用户注册、Storage 元数据 seed 导入、历史 CSV 导入、采集代码包辅助、腾讯云 Lighthouse 防火墙等。

## 命令概览

```bash
moox-cli auth register              # 交互式用户注册（经 admin Auth 服务）
moox-cli metadata import ...        # 导入 Storage 元数据 seed
moox-cli metadata apply ...         # 创建并校验 Storage 元数据契约（不覆盖已有不兼容资源）
moox-cli storage import ...         # 导入历史 CSV 到已登记 Dataset
moox-cli data rows export ...       # 导出行数据
moox-cli collector function ...     # 采集 SCF 代码包打包/发布/部署辅助
moox-cli ops tencent lighthouse ... # 腾讯云 Lighthouse 防火墙规则
```

中文别名：`认证`、`注册`、`存储`（见各子命令 `--help`）。

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
  --file ../../examples/metadata-crypto.seed.yaml \
  --metadata-url http://127.0.0.1:20200 \
  --if-not-exists

moox-cli metadata import --file ../../examples/metadata-crypto.seed.yaml --dry-run

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
  adminclient/          Admin / CloudNode HTTP 客户端
config/cli.yaml         默认配置
```

## 依赖关系

- Storage 元数据/写入：`modules/storage/proto/storagegen`
- Admin 认证：`modules/admin/proto/admingen`
- 采集打包：CLI 内部 `internal/collectorpackager` 生成 collector SCF zip，不直接依赖 `modules/collector` 实现包

Go 1.24+，基于 Cobra + tRPC-Go 客户端。
