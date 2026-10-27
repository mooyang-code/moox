# zstddict

训练、评估和审查 `packages/wirecompress` 使用的 zstd 字典。完整流程见
[统一压缩算法（zstd字典）执行计划](../../docs/计划/统一压缩算法（zstd字典）执行计划.md) 第 6 节。

```bash
go run ./tools/zstddict train -corpus <训练语料> -out default-v2.zdict -id 0x4D580003 -size 224
go run ./tools/zstddict eval  -corpus <评估语料> -dict default-v2.zdict
go run ./tools/zstddict scan  -dict default-v2.zdict
```

## 语料

目录按来源分子目录，每个样本一个 `.bin` 文件（未压缩的原始字节）：

| 来源子目录 | 内容 |
|---|---|
| `rpc-write` | 写请求：`CommitTimeSeriesBatch`、`UpsertFields` 等 |
| `rpc-read` | 读请求与响应：`Read*`、`Query*`、`Count*` |
| `rpc-meta` | `Metadata` 服务的请求与响应 |
| `rpc-ctl` | 其他控制面调用与写调用的响应 |
| `evt-rows` | 数据行事件信封 |
| `evt-metrics` | 指标快照事件信封（内层已不压缩） |
| `evt-misc` | 周期完成、因子结果、视图就绪、主机快照 |
| `plan` | 采集计划 JSON |

取样方式：

- tRPC 样本：在各主机回环口（主机网关 11002、各服务上游口）和 Access 明文口抓包，按 tRPC 帧切出请求体和响应体。
  **只取白名单服务**（存储、因子、归档、策略、监控、采集、出口代理、EventBus），不取 admin、cloudfunction、host-agent、trade；
  丢弃正文含 `secret|passw|token|webhook|api_key|credential|hmac|authorization` 的样本；按内容去重。
- 事件样本：EventBus 的 JetStream 文件流块文件（压缩方式 none）可以直接只读拷贝，解析记录得到信封，不需要任何凭据。
- 语料放在本机临时目录，**不进仓库**；用完删除。
- 专用字典只取该空间的样本；默认字典取所有没有专用字典的空间。
- 训练集和评估集按时间切分，不要拿训练过的样本评估。

## 采用门槛

- 专用字典在该空间的留出集上，相对默认字典**加权总字节至少再省 10%** 才采用。
- `-normalize` 把摘要、UUID、时间戳和整段标的列表替换成占位串，让字典留给结构骨架。是否值得做，先用同一份语料对比有无归一化再决定。

## 依赖

训练需要 zstd 命令行（`--train`，因子机 192.168.0.103 已有 1.5.7）。`zstd.BuildDict` 需要调用方自己提供字典主体，不做片段选择，所以不用它。
