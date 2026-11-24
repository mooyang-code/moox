# zstddict

训练、评估和审查 `packages/wirecompress` 使用的 zstd 字典。

从仓库根目录运行。评估、审查已注册的字典：

```bash
go run ./tools/zstddict eval -corpus <评估语料> -dict packages/wirecompress/dicts/default-v1.zdict
go run ./tools/zstddict scan -dict packages/wirecompress/dicts/default-v1.zdict
```

训练新版本（输出先放在仓库外，评估通过后再登记）：

```bash
go run ./tools/zstddict train -corpus <训练语料> -out <临时目录>/default-v<N>.zdict -id <新字典 ID> -size 224
go run ./tools/zstddict eval  -corpus <评估语料> -dict <临时目录>/default-v<N>.zdict
go run ./tools/zstddict scan  -dict <临时目录>/default-v<N>.zdict
```

`-size` 的单位为 KiB（默认 224），`-cap` 是每类来源的抽样上限 MiB（默认 3）；`-id` 必填，是写入帧头的十六进制字典 ID，必须至少为 `0x8000`。

## 登记规则

字典登记在 `packages/wirecompress/dicts/registry.yaml`，现在只有 `default-v1`（文件 `default-v1.zdict`，字典 ID `0x4D580001`，压缩类型号 32）。

- 已登记的版本不可变：不删、不改文件、不改压缩类型号。训练新字典不要覆盖 `default-v1.zdict`。
- 新版本的名字、文件、字典 ID、压缩类型号都不能与已有版本重复，否则加载注册表时报错；`-id` 传的值要与注册表里写的 `id` 相同（加载时会核对字典文件帧头里的 ID）。按现有编号，下一个版本是 `default-v2`、`0x4D580002`。
- 分两次发布：先只加版本，全部组件（含 SCF 函数）升级后再把 `default` 指向它。

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

- tRPC 样本：在各主机回环口（主机网关 11002、各服务上游口）和主机网关的外部入口（11004 明文口）抓包，按 tRPC 帧切出请求体和响应体。
  **只取白名单服务**（存储、因子、归档、策略、监控、采集、出口代理、EventBus），不取 admin、host-agent、trade；
  丢弃正文含 `secret|passw|token|webhook|api_key|credential|hmac|authorization` 的样本；按内容去重。
- 事件样本：EventBus 的 JetStream 文件流块文件（压缩方式 none）可以直接只读拷贝，解析记录得到信封，不需要任何凭据。
- 语料放在本机临时目录，**不进仓库**；用完删除。
- 只有一个默认字典，取所有空间的样本。
- 训练集和评估集按时间切分，不要拿训练过的样本评估。

## 采用门槛

- 新版本字典在留出集上，相对当前版本**加权总字节至少再省 10%** 才采用。
- `-normalize` 把摘要、UUID、时间戳和整段标的列表替换成占位串，让字典留给结构骨架。是否值得做，先用同一份语料对比有无归一化再决定。

## 依赖

训练需要本机 PATH 中可执行的 zstd 命令行（`--train`）；工具通过 `exec.LookPath("zstd")` 检查，不代装依赖。`zstd.BuildDict` 需要调用方自己提供字典主体，不做片段选择，所以不用它。
