# MooX Skill K 线 RPC 查询设计

## 背景

MooX Skill 需要把采集数据查询包装成自然语言能力。用户应能表达“获取 BTC-USDT 的 1m K 线”，由 Skill 调用 `moox-cli` 返回 Storage 中已经采集的数据，不依赖管理台登录态。

现有 `moox-cli data rows export` 只携带 Storage `AuthInfo`，并直连仅监听 loopback 的 Storage HTTP 服务。Node Service Gateway 已提供原生 tRPC HMAC、Caller ACL、时间戳和 nonce 校验，因此本功能复用 `:11003` 原生 RPC Gateway，不新增 HTTP 数据接口。

## 目标

1. 增加面向行情查询的 CLI 命令：

   ```bash
   moox-cli data kline get \
     --data-type crypto \
     --exchange binance \
     --symbol BTC-USDT \
     --interval 1m \
     --limit 100
   ```

2. CLI 通过原生 tRPC Gateway 调用 `PrimaryStore.ReadTimeSeriesRows`。
3. Skill 打包时从根目录 `moox.toml` 获取已配置的 RPC Gateway 地址和目标节点，并生成独立的 `0600` 数据访问配置。
4. Skill 将自然语言中的标的、周期、数量和时间范围映射为 CLI 参数。
5. 使用专用只读身份，不能复用权限较大的内部服务或现有 `moox-cli` 身份。

## 非目标

- 不使用 Admin JWT、登录会话或浏览器请求签名。
- 不新增公网 HTTP JSON 数据接口。
- 不为本次查询引入 tRPC TLS。行情数据属于非敏感数据，沿用现有 `:11003` HMAC 链路。
- 不把 `moox.toml`、SSH 密码、腾讯云密钥、Gateway 根密钥或 Storage Primary 根密钥放入 Skill 包。
- 首版只支持 Binance 现货 `1m` K 线，不声称支持尚未注册的周期或数据集。
- Skill 包继续依赖仓库 `bin/moox-cli` 或 `PATH` 中的 `moox-cli`，本次不打包跨平台 CLI 二进制。

## CLI 契约

新增命令：

```text
moox-cli data kline get [flags]
```

参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--data-type` | 无 | 必填，例如 `crypto`；必须存在于配置目录中 |
| `--exchange` | 数据类型配置的默认交易所 | 可选；`crypto` 首版默认 `binance` |
| `--symbol` | 无 | 必填，例如 `BTC-USDT` |
| `--interval` | `1m` | 首版仅接受 `1m` |
| `--limit` | `100` | 返回条数，范围 `1..1000` |
| `--start-time` | 空 | 可选 RFC3339 起始时间 |
| `--end-time` | 空 | 可选 RFC3339 结束时间 |
| `--config` | Skill 约定路径或 `MOOX_SKILL_CONFIG` | 独立数据访问配置 |
| `--output` | 空 | 为空时输出 stdout，否则写入 `0600` JSON 文件 |

CLI 不根据字符串拼接猜测 Space、Dataset 或 series tag，而是使用独立配置中的数据目录解析 `data-type + exchange + interval`。首版目录包含以下映射：

| CLI 概念 | Storage 请求值 |
| --- | --- |
| Data type | `crypto` |
| Exchange | `binance` |
| Space | `crypto` |
| Dataset | `dataset_binance_spot_kline_1m` |
| Subject | `--symbol` |
| Frequency | `1m` |
| Series tag | `venue:binance` |
| Order | `SORT_ORDER_DESC` |
| Page size | `--limit` |

CLI 输出沿用 `ReadTimeSeriesRowsRsp` 的 protobuf JSON 结构。默认按最新时间倒序返回，避免为了展示进行二次排序或引入新的输出协议。错误写入 stderr，进程返回非零退出码；stdout 只包含成功结果，便于 Agent 和脚本解析。

## RPC 与鉴权

调用链：

```text
Skill
  -> moox-cli data kline get
  -> storagegen.NewPrimaryStoreClientProxy
  -> gatewayauth.NewTRPCClientOptions
  -> Node Service Gateway :11003
  -> PrimaryStore.ReadTimeSeriesRows
```

请求使用两层现有 HMAC：

1. Gateway HMAC：`moox-skill` 的独立 `key_id/caller/secret`，签名覆盖 RPC 名称、序列化请求体、时间戳、nonce 和 target node。
2. Storage HMAC：请求体携带 `app_id=moox-skill` 和预先派生的 `app_key=HMAC-SHA256(primary_root_secret, app_id)`。

独立配置只保存派生后的两组最小凭据，不保存任何根密钥。Gateway 路由是实际的只读权限边界，因为 Storage `AuthInfo` 本身不区分读写方法。

## Gateway ACL

从现有 PrimaryStore Gateway 路由中移出 `ReadTimeSeriesRows`，新增同一 `service_id`、同一 `service_path` 的独立路由：

```yaml
gateway_methods: [ReadTimeSeriesRows]
gateway_callers:
  [admin-gateway, collector, factor, monitor, archive, storage-view, moox-skill]
```

原路由继续承载其他读写方法，但不包含 `moox-skill`。这样专用凭据即使泄露，也只能通过 Gateway 调用 K 线范围读取接口。

部署打包流程增加并注册 `moox-skill` Gateway 派生密钥。不得把现有 `gateway-service.key`、`gateway-moox-cli.key` 或内部服务密钥作为 Skill 凭据。

## 独立配置

打包阶段生成 `config/data-access.yaml`。同一文件同时保存 RPC 连接、派生凭据和可查询的数据目录：

```yaml
version: 1
gateway:
  target: ip://203.0.113.10:11003
  target_node: control
  key_id: moox-skill
  caller: moox-skill
  secret: <derived-gateway-secret>
storage:
  app_id: moox-skill
  app_key: <derived-storage-app-key>
data_types:
  crypto:
    default_exchange: binance
    exchanges:
      binance:
        space_id: crypto
        series_tag: venue:binance
        kline_datasets:
          1m: dataset_binance_spot_kline_1m
```

`data_types`、交易所名称和周期键统一使用小写 ASCII 标识。CLI 对 `--data-type`、`--exchange` 和 `--interval` 做 trim 与小写规范化，再执行精确目录查找。配置不支持的组合必须返回明确错误，不能退化为猜测 Dataset ID。首版只写入 `crypto/binance/1m`；后续增加交易所或周期时扩展目录，不改变 CLI 命令形状。

生成流程：

1. `scripts/build/package-skill.sh` 创建临时 staging 目录，不直接修改 `skills/moox` 源目录。
2. 脚本调用新的 `moox-cli setup export-skill-config --file ./moox.toml --space crypto --output <staging>/moox/config/data-access.yaml`。
3. CLI 严格解析 `moox.toml`，从 `scf_fetcher.spaces[space_id=crypto]` 读取 `storage_rpc_gateway_target` 和 `storage_gateway_node_id`。
4. CLI 使用 `moox.toml` 的控制机 SSH 信息，在远端读取专用 `gateway-moox-skill.key`，并从 Storage Primary 根密钥派生 `moox-skill` AppKey。CLI 只把派生值写入输出配置，并立即丢弃根密钥内容。
5. 配置文件和最终 `dist/moox-skill.tar.gz` 权限均设为 `0600`。
6. staging 在成功或失败时都删除。归档不得包含 `moox.toml`、共享密钥或临时文件。

如果专用 Gateway 密钥尚未部署、目标 Space 缺少 RPC Gateway 配置、远端根密钥不可用或文件权限不安全，打包立即失败并给出明确错误，不生成不完整归档。

## Skill 自然语言契约

`skills/moox/SKILL.md` 增加数据查询入口，并引用独立说明 `references/data-query.md`。

自然语言映射示例：

| 用户表达 | CLI 调用 |
| --- | --- |
| 获取加密货币 BTC-USDT 的 1m K 线 | `data kline get --data-type crypto --symbol BTC-USDT --interval 1m`；交易所使用配置默认值 `binance` |
| 获取币安 BTC-USDT 最新 20 根 1m K 线 | 增加 `--exchange binance --limit 20` |
| 获取指定时间范围 | 增加 `--start-time` 和 `--end-time` |

Skill 必须从用户表达中提取数据类型；用户未指定交易所时使用该数据类型在配置中的默认交易所。Skill 必须原样使用用户给出的 symbol，不猜测交易对；必须拒绝配置中不存在的数据类型、交易所或周期。成功时总结 data type、exchange、symbol、interval、行数和时间范围，但不得输出配置中的 HMAC 凭据。

## 错误处理

- 配置缺失、权限不是 `0600`、字段未知或版本不支持：启动前失败。
- data type 或 symbol 为空、数据目录组合不存在、limit 越界、时间格式错误或起止时间倒置：发出参数错误，不发送 RPC。
- Gateway 返回鉴权、ACL、路由或网络错误：保留稳定错误类别，不输出 secret、签名或完整请求体。
- Storage `ret_info` 非成功：CLI 返回非零退出码并输出业务错误。
- 查询结果为空：返回成功和空 rows，不把“没有数据”误报为调用失败。

## 测试与验收

实现遵循测试先行：

1. CLI 单元测试覆盖命令树、参数校验、数据类型目录、默认/显式交易所、DESC、limit、时间范围、配置权限和错误输出。
2. 使用 fake tRPC Gateway 或可注入 PrimaryStore proxy 验证 Gateway options、target node、双层 HMAC和请求体。
3. Gateway 路由测试证明 `moox-skill` 可调用 `ReadTimeSeriesRows`，不能调用 `UpsertFields` 或其他 Storage 方法。
4. 打包契约测试证明配置生成于 staging、权限为 `0600`，归档不包含 `moox.toml`、根密钥、SSH 密码或腾讯云凭据。
5. Skill 文档契约测试覆盖自然语言到 CLI 参数的映射。
6. 运行 CLI 模块测试、Gateway/Admin 路由测试、脚本契约测试、`git diff --check` 和 `make package-skill`。

本次交付只证明代码、测试和本地 Skill 归档。除非另行要求，不把本地验证表述为远端部署或真实行情查询验收。
