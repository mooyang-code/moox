# 默认初始化元数据与健康检查命名设计

## 1. 目标

MooX 的新系统初始化需要满足以下结果：

1. `examples/` 下的初始化配置集中到一个固定目录，避免按市场、模块继续拆出大量 YAML。
2. 默认业务空间只包含 A 股 `stockcn` 和加密货币 `crypto`。
3. 默认配置同时定义可用的 DataSource、FieldGroup、Field、Dataset、DatasetColumn、View 和 ViewColumn。
4. `moox-cli setup init` 接受配置目录，一次完成 Admin Space 与 Storage Metadata 初始化。
5. 初始化可安全重复执行；已有契约不一致时失败，不静默覆盖。
6. 满足 Storage 激活条件的默认 Dataset 在初始化末尾被显式激活。
7. 管理台顶部空间选择器能看到 A 股和加密货币两个空间；切换后首页统计、数据集页和字段页读取对应空间数据。
8. 删除 `Pipeline` 术语。现有代码内置项表达的是模块健康检查，不是流水线；YAML 表达的是 Dataset 健康策略，也不是流水线配置。

本项目不保留旧文件名、环境变量、指标标签、Proto 字段或命令内部类型的兼容入口。

## 2. 配置目录

默认配置固定放在：

```text
config/setup/
├── README.md
├── metadata.yaml
├── dataset-health-policy.yaml
└── service-deployments.yaml
```

三个 YAML 分别由不同边界消费，不增加总清单文件，也不扫描目录中任意 YAML：

- `metadata.yaml`：由 `moox-cli setup init` 读取，包含业务元数据和 `mooxsys` 监控元数据。
- `dataset-health-policy.yaml`：由 Monitor 读取，定义实时 TimeSeries 健康判定阈值。
- `service-deployments.yaml`：由 Admin 部署导入流程读取，定义服务部署清单。

删除空的 `platform-local.seed.yaml`。原来的三份 Metadata seed 合并为 `metadata.yaml`；服务部署和 Dataset 健康策略因为 Schema、消费者和生命周期不同，保持独立。

发布包继续把整个 `examples/` 目录打包，同时把健康策略复制到运行目录
`config/dataset-health-policy.yaml`。部署脚本从新的固定路径读取服务部署清单。

## 3. 默认市场元数据

### 3.1 业务空间

只保留：

| Space | 名称 | Market | Timezone |
| --- | --- | --- | --- |
| `stockcn` | A股市场 | `CN` | `Asia/Shanghai` |
| `crypto` | 加密货币市场 | `crypto` | `UTC` |

`mooxsys` 继续作为 Storage 内部监控空间，但带有
`attributes.scope: internal`，不会同步为管理台业务空间。

港股和美股不进入默认初始化包。后续需要时应作为独立业务扩展提交，而不是让初始系统携带未使用目录。

### 3.2 A 股

保留当前完整且已有导入闭环的契约：

- DataSource：`quantclass_stock`
- Dataset：`stock_kline`、`index_kline`、`dataset_stockcn_financial_statement_metric`、`dataset_stockcn_financial_summary`
- FieldGroup：标识、市场、行情、成交估值、财务
- Field：证券/指数标识、OHLC、成交量/额、财务指标及 `raw_payload`
- 每个 Dataset 对应显式 DatasetColumn、View 和 ViewColumn

### 3.3 加密货币

保留共享 Dataset 模型：

- DataSource：`crypto`、`binance`、`okx`
- Dataset：`dataset_spot_kline_1h`、`dataset_perpetual_kline_1h`
- FieldGroup：标识、行情、合约指标
- Field：OHLC、成交量、成交额、成交笔数、资金费率及 `raw_payload`
- Binance/OKX 使用同一 Dataset，通过 scalar `series_tag=venue:<exchange>` 区分

默认小时频率统一使用 Storage canonical 值 `1H`，View filter、样例 CSV、监控策略和 Collector 写入保持一致。

Subject、SubjectSymbol 和 DatasetSubject 属于运行目录，由采集或数据导入流程登记，不在默认 YAML 中枚举大量标的。

### 3.4 Dataset 激活

Seed 中 Dataset 保持 `disabled`，避免绕过 Storage 的 DataNode 和 Schema 检查。
`setup init` 完成元数据 create-or-verify 后，逐个执行：

1. `CheckDatasetActivation`
2. 若未就绪则失败并返回具体检查摘要
3. 若已就绪则使用返回的 revision 调用 `ActivateDataset`
4. 已激活且绑定锁定的 Dataset 记为 unchanged

跨 Admin 与 Storage 不伪造分布式事务。任一阶段失败后，调用方修复环境并重跑同一命令即可收敛。

## 4. `moox-cli setup init`

命令形式：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

参数：

- `--file`：现有只读 `moox.toml`，继续要求文件名固定、当前用户持有、权限 `0600`，命令期间内容和文件身份不变。
- `--config-dir`：默认 `./config/setup`，从固定文件名 `metadata.yaml` 读取默认元数据。
- `--storage-host`：必填，必须是 `moox.toml` 中已登记且部署了 Storage 的主机。

执行顺序：

1. 严格解析 `metadata.yaml`，拒绝未知字段、重复 Space、缺失依赖、非法 FieldGroup 层级和保留内部空间滥用。
2. 读取并校验 `moox.toml`。
3. 通过私有 Admin Setup RPC create-or-verify 管理员、腾讯云凭据、主机以及业务 Space。
4. 验证 Admin Setup 状态为 completed。
5. 登录验证管理台入口。
6. 通过 Storage 主机 SSH 隧道执行严格 Metadata apply。
7. 激活默认 Dataset。
8. 再次读取 Admin Setup 状态以及 Storage 元数据，输出分阶段汇总。

`setup apply` 保留现有只初始化用户、凭据和主机的用途；`setup init` 是新系统完成默认业务初始化的高层命令。

输出不包含密码、Token、SSH 命令、完整请求或数据行。结果至少包含：

- Admin action 与 Space 数量
- Storage planned/applied/unchanged 资源数量
- Dataset planned/activated/unchanged 数量
- 初始化后的业务 Space ID

## 5. Admin 与 Storage 双写

管理台空间选择器读取 Admin `t_spaces`，Storage 元数据使用独立的 Storage `t_spaces`。同一个 `metadata.yaml` 是两边业务 Space 的唯一初始化事实源：

- `attributes.scope=internal` 的 Space 只写 Storage。
- 其他 Space 同时写 Admin 和 Storage。
- Admin Setup RPC 在同一 Admin 事务中 create-or-verify 用户、凭据、主机和业务 Space。
- Admin 已有同 ID Space 时逐字段校验名称、描述、owner、market、timezone、status 和 attributes；不一致返回 `setup_conflict`。
- Storage 使用 `metadata apply` 的严格契约校验；不再使用“存在即跳过”的宽松 import 语义。

Setup Proto 为 Apply/Status 请求增加 Space 列表，为响应增加 Space 计数。CLI 普通 `setup apply/status` 发送空 Space 列表，`setup init` 发送从默认元数据提取的业务 Space。

## 6. 健康检查命名

彻底删除该子系统中的 `Pipeline` 命名。

### 6.1 模块健康检查

代码内置注册表改为：

```go
type ModuleHealthCheck struct {
    ID                    string
    Module                string
    Enabled               bool
    MaxLag                time.Duration
    CheckFreshness        bool
    CheckWatermark        bool
    ObservabilityDeferred bool
}
```

删除没有生产读取方的 `SpaceID`、`InputDataset` 和 `OutputDataset`。

主要命名：

- `BuiltInModuleHealthChecks`
- `HealthCheckIDsForModule`
- `ModuleHealthSignals`
- `EvaluateModuleHealth`
- Prometheus label：`health_check`
- Doctor check ID：`module.health_check:<module>:<health-check-id>`
- Doctor Proto：`health_check_ids`、`health_check_id`
- 恢复动作：`inspect_health_check_input`

现有稳定 ID 如 `collector-market-data`、`factor-calculation` 保留；它们是健康检查 ID，不再称为流水线 ID。

### 6.2 Dataset 健康策略

YAML 类型独立为：

```go
type DatasetHealthPolicy struct {
    Version            int
    RealtimeTimeSeries RealtimeTimeSeriesPolicy
    Checksum           string
}
```

主要入口：

- `LoadDatasetHealthPolicy`
- `ValidateDatasetHealthEnvironment`
- `MOOX_DATASET_HEALTH_POLICY`
- `MOOX_DATASET_HEALTH_POLICY_HASH`

只有 Monitor 消费 Dataset 判定阈值。其他模块直接从代码注册表获取本模块健康检查 ID，不再为了一个未使用的 YAML 读取和校验策略文件。

共享 healthz 字段改为 `dataset_health_policy_hash`。Doctor 只要求 Monitor 身份携带并匹配该哈希。

当前 YAML 中未被任何运行路径消费的 `canary_subject_id`、`market_price_change_ratio` 和 `market_volume_ratio` 删除。市场 canary 已由 Monitor 自身的 typed config 和运行逻辑负责，不在 Dataset 健康策略中保留第二份无效配置。

## 7. 错误与幂等

- YAML 使用 `yaml.Decoder.KnownFields(true)`，只接受单文档且限制文件大小。
- 默认资源第一次执行返回 created/applied/activated。
- 完全相同的第二次执行返回 unchanged。
- Admin 或 Storage 任一既有资源契约不同均失败，不进行更新。
- Metadata apply 必须覆盖 ViewColumn 的读取与等价校验，保证完整默认 seed 可重复执行。
- Dataset 激活使用 revision CAS；并发修改导致 conflict，命令失败而不是重试覆盖。
- 初始化不会删除已有空间、数据集、字段、数据行或凭据。

## 8. 验证

### 8.1 自动测试

- `packages/report`：健康检查注册表、指标 label、健康判定、Dataset 策略严格加载和哈希。
- Monitor/CLI Proto：新 `health_check_id(s)` 字段、边界和去重校验。
- Admin Setup：Space 首次创建、重复不变、字段冲突、事务回滚和 Status 计数。
- CLI Metadata：严格 YAML、Admin Space 提取、Storage 完整 apply 两次、ViewColumn 冲突。
- CLI Setup：`setup init` 参数、阶段顺序、失败短路、Dataset 激活和脱敏 JSON。
- Storage/Monitor/CLI 模块测试、race 测试、工作区测试、Proto 检查和 release/deploy contract。

### 8.2 真实环境

在 `106.53.107.122` 部署包含新 Setup Proto 的 Admin/Monitor/CLI 配置后执行：

```bash
moox-cli setup init \
  --file ./moox.toml \
  --config-dir ./config/setup \
  --storage-host control
```

真实 Playwright 验收：

1. 登录 `https://106.53.107.122:9527/`
2. 顶部选择器同时出现 `A股市场` 和 `加密货币市场`
3. 切换 `stockcn`，首页显示其 Dataset 数量，数据集页显示 A 股 Dataset，字段页显示 A 股字段
4. 切换 `crypto`，首页显示其 Dataset 数量，数据集页显示 Crypto Dataset，字段页显示 Crypto 字段
5. 浏览器请求体和 `X-Space-Id` 与当前选择一致

最终还需独立 `codeCR` 审查配置事实源、Admin/Storage 双写、幂等、激活、命名清理、敏感信息和远端验收覆盖。
