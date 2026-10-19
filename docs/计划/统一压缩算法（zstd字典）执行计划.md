# 统一压缩算法（zstd + 训练字典）执行计划

状态：已经 Codex 审核并修订（5 项重大问题均已处理），待确认后进入第 0 阶段；未编码。前置背景见 [网关跨主机连接保活与写重试方案](网关跨主机连接保活与写重试方案.md) 第 12～14 节。

## 1. 目标与边界

**目标**：系统内部所有需要压缩的传输与编码统一为一套算法（zstd + 训练字典），实现集中在一个共享包里，各模块只引用这个包，不再各自调用 `compress/gzip`。

**决策前提**（用户确认）：

- 项目未上线，不考虑兼容、不做平滑迁移：gzip 路径直接删除，所有组件同一提交一次性发布。
- SCF→Access 也纳入，统一压缩。
- 只有浏览器流量不纳入。
- 先实测，再在“通用字典”和“分类字典”之间二选一（第 3 节）。

**纳入范围**：

| # | 位置 | 现状 | 改后 |
|---|---|---|---|
| 1 | tRPC 跨主机调用（`packages/gatewayclient` 全部模式：经主机网关、control 中转、SSH 隧道、External/Access） | gzip，默认开，配置 `compression: gzip/none` | 共享包注册的 zstd 字典压缩类型；跨主机恒定开启，本机回环恒定不压缩 |
| 2 | 服务端解压（`packages/gatewayauth/nativewire/compress.go`，所有引用 gatewayauth 的进程：主机网关、Access、各业务组件） | 替换框架 gzip 为有上限的 `boundedGzip`，其他内置算法拒绝解压 | 由共享包注册有上限的 zstd 解压器；gzip 与其他内置算法一律拒绝解压 |
| 3 | SCF→Access（`modules/collector/internal/marketfetch/access.go`，环境变量 `MOOX_GATEWAY_COMPRESSION`） | 显式 `none` | 与 #1 相同，环境变量删除 |
| 4 | EventBus 事件信封（`packages/events/compress.go`） | 发布端显式开启（仅存储 DataNode，≥4KB），`+gzip` | 所有发布端默认开启，内容类型 `+zstd`，按事件类型选字典 |
| 5 | 指标快照（`packages/report/handler.go` 产生，`modules/monitor/internal/metrics/parser.go` 解析，经 EventBus 传输） | 快照内层 gzip（`metricspb.Compression_COMPRESSION_GZIP`），信封层不压缩 | 删除内层压缩，交给 #4 的信封层压缩（只压一层） |
| 6 | 采集计划 SCF 环境变量（`modules/collector/internal/marketfetch/plan.go`，`MOOX_MARKET_FETCH_PLAN`） | JSON → gzip（BestCompression）→ base64 | JSON → 共享包（计划字典）→ base64 |
| 7 | 出口代理回传（`modules/egressproxy` 的 `gzip_response`/`body_gzip`，`modules/collector/internal/httpclient/egress_transport.go`） | 业务层 gzip，外面还套着 #1 的帧压缩（双层） | 删除业务层压缩及协议字段，只靠 #1 的帧压缩 |

**不纳入**（保持原样）：

- 浏览器流量：`modules/admin/internal/console/forward.go`、`modules/consoleproxy/internal/engine/render.go`（Caddy 的 zstd/gzip 内容编码由浏览器协商）。
- 文件格式：
  - 归档导出与暂存读取的 `.csv.gz`（`modules/archive/internal/worker/export`、`staging/partitions.go`），这是对外文件格式，不是传输。
  - CLI 单元包的 `tar.gz`（`modules/cli/internal/setup/unitpackage`）。
- 与外部系统的 HTTP：出口代理向交易所发出的 `Accept-Encoding: gzip, deflate, br` 及其解压（`proxy.go` 第 96、205 行），由对端决定。
- Prometheus 抓取（`hostgateway/internal/health` 的 `DisableCompression`）。
- NATS 自身的连接层压缩：不开启，避免与信封层重复压缩。

## 2. 基线数据（已测）

用一次整分钟突发的 42 个真实 `CommitTimeSeriesBatch` 请求体（合计 289KB，中位 7KB）：

| 算法 | 压缩比 | 每请求耗时 |
|---|---|---|
| gzip（现状） | 4.0x | 约 100µs |
| zstd 默认级别，无字典 | 4.2x | 约 22µs |
| zstd + 字典（同一突发前一半训练、后一半评估） | 5.6x | — |

字典收益在小消息上最大（帧头、字段名、键前缀全部可被字典引用）。上表的字典与评估样本来自同一分钟，偏乐观。第 3 节要用跨天样本复测，才能作为决策依据。

## 3. 第 0 阶段：分类实测与字典方案决策

### 3.1 语料采集

每类语料单独成目录，一个样本一个文件（原始未压缩字节）。语料放在本机临时目录，**不进仓库**；用完删除，只保留统计结果。

| 类别 | 内容 | 采集方式 |
|---|---|---|
| storage-write | `CommitTimeSeriesBatch` 等写请求体 | 存储机回环口（主机网关 → primary，明文）抓包，沿用 `extract_bodies.py` 的 tRPC 帧切分 |
| storage-read | 因子引擎、策略、归档、View 的读请求与响应体 | 同上，抓存储机网关到 view/primary 的回环端口，请求与响应都要 |
| scf-access | SCF 经 Access 的请求与响应 | 海外机 Access 到存储机网关之前的本机回环口（Access 解压后转发） |
| control-plane | 控制面组件之间的小调用（采集调度、云节点、因子管理、主机代理等） | 控制机回环口抓包 |
| egress | 出口代理返回的交易所响应 | 海外机回环口（主机网关 → egressproxy） |
| events | 各类事件信封（行事件、周期完成、因子结果、指标快照） | EventBus 开了 TLS，抓不到明文。改为在 EventBus 所在主机，用现有服务凭据起一个不确认、临时的只读 JetStream 消费者，按主题导出信封并解开现有 gzip。凭据只经环境变量传入，不打印、不落盘 |
| plan | 采集计划 JSON | 从 collector 库读取当前全部计划，按 `plan.go` 的编码逻辑生成 JSON |

**安全约束**：

- 不采集 Admin、SecretMgr，以及任何请求或响应体可能含密钥、密码、Webhook 地址的服务（按服务目录过滤服务路径）。
- 抓包只保留业务请求体文件，pcap 立即删除。
- 字典最终会以文件形式进仓库，字典内容是样本里的高频子串。提交前要扫描字典里的可打印串，确认没有凭据、密钥 ID、Webhook、内网之外的地址。

**时间跨度**：

- 至少两天。第一天训练，第二天评估，用来量化字典老化。
- 覆盖整分钟突发、补采、因子结果批量写入和读高峰。

### 3.2 测量项

测量程序放在 scratchpad（第 0 阶段不改仓库）。用 `github.com/klauspost/compress/zstd`，版本与仓库依赖一致，升级到 v1.18.6 统一。每类计算以下候选：

1. zstd 默认级别，无字典（对照组）。
2. **方案 A**：一个通用字典，混合全部类别、按真实流量占比抽样训练。
3. **方案 B**：每类一个字典。
4. A、B 各取字典大小 32KB / 64KB / 112KB。

输出指标：

- 每类的总字节和压缩比。
- 按小于 1KB、1～16KB、大于 16KB 分桶的压缩比。
- 编码和解码耗时（p50、p99）。
- 编码器和解码器的常驻内存。
- 第二天样本相对第一天的压缩比衰减。

**加权**：用跨主机链路的实际流量占比加权，重点是出口受限的海外机 ↔ 存储机链路。得到“每天跨主机总字节”这一个决策数。

### 3.3 决策规则

方案 B 的代价：

- tRPC 侧每类要占一个压缩类型编号，由客户端按服务选择（第 4.3 节）。
- 字典数量多，训练和发布成本都更高。

选择规则：

- **默认选 A。**
- 只有同时满足以下两条才选 B：
  - B 在加权跨主机总字节上比 A 再省 **≥10%**。
  - 至少一个大流量类别（storage-write 或 storage-read）在 A 下的压缩比比 B 低 **≥15%**。
- 选 B 时类别不超过 4 个，其余流量归入通用字典。

非 tRPC 路径（事件、计划环境变量）由我们的代码直接调用编码函数，按类别选字典没有额外成本：

- 选 A：这些路径也用通用字典。
- 选 B：事件按事件类型、计划按“plan”类选字典。

字典大小取压缩比曲线拐点，预计 64KB。每个字典都会嵌进所有二进制，包括 SCF 代码包。

第 0 阶段结束时，把实测表格和决策写进本文档第 11 节，再进入编码。

## 4. 共享包设计：`packages/wirecompress`

### 4.1 职责

- 内嵌字典（`//go:embed dicts/*.zdict`）。
- 构造全局编码器和解码器。
- 注册 tRPC 压缩类型。
- 给非 tRPC 路径提供编解码函数。
- 统一解压上限。
- 导出压缩指标。

只依赖 `klauspost/compress/zstd`、`trpc-go/codec` 和 Prometheus。

### 4.2 API（草案）

```go
package wirecompress

type Class string // 字典类别；方案 A 只有 ClassDefault

const ClassDefault Class = "default"

// Encode 用类别对应的当前字典压缩；空输入返回空。
func Encode(class Class, src []byte) []byte

// Decoder 是固定解压上限的解码器，按帧头里的字典 ID 自动选字典。
// 每个使用点在初始化时按自己的上限构造一个，进程内全局复用、可并发调用。
type Decoder struct{ /* *zstd.Decoder */ }

func NewDecoder(limit int) *Decoder

// Decode 解压；解压过程中累计输出超过构造时的上限立即返回 ErrTooLarge，不会先展开再校验。
func (d *Decoder) Decode(src []byte) ([]byte, error)

// CompressType 返回类别对应的 tRPC 压缩类型编号，供 gatewayclient 使用。
func CompressType(class Class) int

var ErrTooLarge = errors.New(...)
```

- **编码器**：
  - 每个类别一个全局 `*zstd.Encoder`，用 `EncodeAll`，可并发调用。
  - 选项：`WithEncoderLevel(SpeedDefault)`、`WithEncoderDict(当前字典)`、`WithEncoderCRC(false)`（TCP/TLS 已保证完整性，每帧省 4 字节）、`WithWindowSize(1MB)`、`WithLowerEncoderMem(true)`。
  - 并发数取 `min(GOMAXPROCS, 4)`，控制常驻内存。SCF 函数内存小，在第 0 阶段测量里要确认。
- **解码器**：按上限分实例，不用一个全局解码器加事后校验。
  - 原因：`WithDecoderMaxMemory` 是解码器级别的上限，`DecodeAll` 会按帧声明的原始长度预分配并展开到这个上限。如果全局解码器按 64MB 构造、调用方事后再按 1MB 校验，攻击帧仍能先让进程展开 64MB。
  - 每个 `NewDecoder(limit)` 内部是一个独立的 `*zstd.Decoder`，选项为：
    - `WithDecoderDicts(全部内嵌字典...)`
    - `WithDecoderMaxMemory(limit)`
    - `WithDecoderMaxWindow(min(limit, 1MB))`
    - `WithDecoderConcurrency(min(GOMAXPROCS, 4))`
  - 帧声明长度超限时 `DecodeAll` 直接拒绝。不声明长度、多帧拼接时，klauspost 在解码过程中按累计输出校验（`framedec.go` 的 `maxDecodedSize` 检查）。两种情况都要有测试覆盖。
  - 字典 ID 不在已知集合时直接报错。
  - 使用点与上限是固定的少数几个，进程内最多 3～4 个实例：
    - tRPC 帧：`MaxFrameBytes`，64MB。
    - 事件信封：64MB。
    - 采集计划：1MB。
- **字典 ID**：用 zstd 规定的用户区间（≥32768），固定前缀加序号，例如 `0x4D580001`。
  - 字典注册表只追加：退役的字典继续留在解码集合里，至少保留到 JetStream 最长保留期（当前 168h）之后。
  - 编码器只用每个类别的“当前”字典。

### 4.3 tRPC 压缩类型注册

- 框架内置类型占 0～7。本包从 **32** 起编号：方案 A 只有 32；方案 B 每类一个号（32、33…）。号码与类别的对应写死在本包常量里，不可配置。
- **注册**：在 `wirecompress` 的 `init` 里 `codec.RegisterCompressor(n, compressor{class})`。
  - `Compress` 走对应类别的编码器。
  - `Decompress` 走按帧上限构造的解码器，上限为进程级帧上限 `nativewire.MaxFrameBytes`（64MB）。帧上限常量移进 `wirecompress`，nativewire 引用它，避免循环依赖。
- **替换 `nativewire/compress.go`**：
  - 删除 `boundedGzip`。
  - `unsupportedDecompress` 覆盖范围扩大到 gzip（`CompressTypeGzip`）在内的全部内置类型，拒绝解压。压缩方向也一并拒绝：本系统不再有任何调用方使用它们。
  - nativewire 的 `init` 导入 `wirecompress`，保证所有引用 gatewayauth 的进程（主机网关、Access、业务组件）都注册了 zstd 类型。
- **响应方向**：tRPC 服务端用请求的压缩类型压缩响应（`server/service.go` 约第 366 行），读接口的大响应自然用同一个字典。所以方案 B 的分类字典必须用请求和响应两侧样本一起训练。
- **主机网关**：
  - 网关是 Noop 序列化转发。入站帧由框架按请求类型解压，转发给本机上游时不带压缩（`router/upstream.go` 不设压缩类型）。
  - 回给调用方时，框架按原请求类型压缩响应。
  - control 中转（`gatewayclient/relay`）按连接转发字节，不解析帧，不受影响。
  - 网关本身不需要改代码，只要随 gatewayauth 导入注册即可。第 7 节测试会覆盖“压缩请求 → 网关 → 未压缩上游 → 压缩响应”。

### 4.4 指标

注册到进程默认 Prometheus 注册表，由 `packages/report` 采集上报：

- `moox_wirecompress_bytes_total{class, op="encode|decode", kind="raw|compressed"}`：用来算各类实际压缩比。
- `moox_wirecompress_errors_total{class, op, reason="too_large|unknown_dict|corrupt"}`。

监控页不新增面板，只在发布验证和字典老化判断时查询。可选：压缩比低于发布时基线 80% 告警，第 9 节。

## 5. 各替换点的具体改动

### 5.1 `packages/gatewayclient`

- 删除 `Compression` 配置项、`CompressionNone`、`ValidCompression` 及校验（`config.go` 第 21、36、51、83 行附近，`client.go` 第 65 行附近）。
  - 跨主机端点恒定压缩，本机回环恒定不压缩。
  - 理由：统一算法后不再有“关掉压缩”的业务需要，少一个会配错的开关。
- `trpc.go` 第 210 行：由 `codec.CompressTypeGzip` 改为 `wirecompress.CompressType(class)`。
  - 方案 A：类别恒定为 `ClassDefault`。
  - 方案 B：类别由服务目录给出，见下。
- 方案 B 才需要：服务目录（`packages/servicecatalog` 的 catalog.yaml）给服务加可选字段 `compress_class`，`validate.go` 校验它必须是已知类别。gatewayclient 选端点时一并取出类别。方案 A 不加此字段。
- 测试：
  - `compression_test.go` 改为断言跨主机用 zstd 类型、回环不压缩。
  - 保留“极小请求也拿到压缩响应”的用例，含空 body。
  - 删除 `none` 相关用例。

### 5.2 配置与模板（随 5.1 删除 `compression` 字段）

配置是严格解析的，多一个字段进程就起不来，所以模板和生产已安装的配置要在同一次发布里一起清理：

- 删除 `modules/archive/config/worker/app.yaml` 第 9 行的 `compression: gzip`。
- 编译机部署脚本直接生成 factor-engine、archive-worker 的配置，同样写死了 `compression: gzip`，一并删除：
  - `scripts/deploy/deploy-factor-engine.sh` 第 124 行、`deploy-factor-engine.ps1` 第 111 行。
  - `scripts/deploy/deploy-archive-worker.sh` 第 127 行、`deploy-archive-worker.ps1` 第 111 行。
  - 两者都严格解析配置（`modules/factor/internal/engine/config.go` 第 150 行、`modules/archive/internal/worker/config/config.go` 第 146 行）。不删的话，用现有脚本重新部署会被拒绝。
  - 对应的脚本契约测试同步更新。增加一条校验：脚本生成的配置能被新二进制的配置解析加载。
- 删除 `modules/collector/config/app.yaml` 第 92 行的 `scf_access.compression: none`。
- 同步删除：
  - `modules/collector/internal/bootstrap/fetch_plan_config.go` 第 336～340 行的压缩映射。
  - 环境变量 `MOOX_GATEWAY_COMPRESSION`。
- 清理其他引用：
  - `modules/cli/internal/setup/config` 相关字段和测试数据 `testdata/scf_plan_rendered.yaml`。
  - `packages/cloudprovider/tencent/scf_environment.go` 第 46 行的校验。
  - `modules/cloudnode/internal/rpc/runtime_config.go` 第 284 行元数据里的 `gateway_compression`。
  - `modules/collector/internal/marketfetch/access.go` 第 25～26 行。
- 文档：`docs/模块/外部接入.md` 的压缩小节改写。

### 5.3 `packages/events`

- `compress.go` 改写：
  - `ContentTypeGzip` 改为 `ContentTypeZstd = ContentType + "+zstd"`。
  - `compressBody` 和 `inflateBody` 改调 `wirecompress.Encode`，以及包级 `wirecompress.NewDecoder(maxInflatedBytes)` 实例（上限沿用 64MB）。
  - 保留“压缩后没有明显变小（≥90%）就按原样发布”的判断。
- 压缩改为发布器默认行为：
  - 删除 `SetCompression`，阈值固定为包内常量。
  - 阈值由第 0 阶段测出：有字典时小消息也划算，预计 256B。
  - 删除存储 outbox `publisher.go` 第 105～111 行的显式开启和 `compressEventMinBytes`。
- 字典选择：
  - 方案 A 用通用字典。
  - 方案 B 时按事件类型（`events.Event`）映射到类别，映射表放在 events 包内。
- `decode.go` 第 32 行的分支改为 `ContentTypeZstd`，未知的 `+xxx` 内容类型一律报错。
- 存储 Pebble 里的标记、库存、outbox 保存的是**未压缩**信封（`DecodeRaw` 用 `events.ContentType` 校验），不受影响。

### 5.4 指标快照（`packages/report`、`packages/metricspb`、`modules/monitor`）

- 内层不再压缩：
  - `metrics.proto` 删除 `Compression` 枚举和字段，`Data` 恒为原始 Prometheus 文本。
  - 保留 `UncompressedSha256`，改名 `DataSha256`。
- `report/handler.go`：
  - 删除第 324～336 行的 gzip。
  - `config.go` 删除 `GzipLevel` 和 `MOOX_METRICS_GZIP_LEVEL`。
  - 删除 `MaxCompressedBytes`，只保留 `MaxUncompressedBytes`。
  - 信封层压缩由 5.3 默认完成。
- `monitor/internal/metrics/parser.go`：
  - `DecodeSnapshot` 去掉解压分支，只校验原始大小上限和校验和。
  - `Limits` 去掉 `MaxCompressedBytes`。
- 风险核对：快照原始上限 16MB，压缩后的信封必须小于 NATS `max_payload_bytes`（8MB）。
  - 按 4～6 倍压缩比远低于上限。
  - 发布端在压缩后仍超限时直接报错，不发布；现状也是 NATS 拒绝。

### 5.5 采集计划环境变量（`modules/collector/internal/marketfetch/plan.go`）

- 第 94～104 行：gzip（BestCompression）改为 `wirecompress.Encode(计划类别, json)`，再 base64。
- 第 120～129 行：解码改为包级 `wirecompress.NewDecoder(1MB)` 实例。上限与现有读取阶段的 1MB 限制（第 129 行）一致，在解压过程中生效。
- `plan_runtime.go` 和 `plan_reconciler.go` 只经这两个函数，不改。
- 这里预计有额外收益：计划 JSON 同构性高，字典能明显降低环境变量体积，`managed_environment_budget_bytes` 的余量更大。第 0 阶段测出具体数字。

### 5.6 出口代理（`modules/egressproxy`、`modules/collector/internal/httpclient`）

- `egress.proto`：
  - 删除 `gzip_response = 8` 和 `body_gzip = 5`，不保留 `reserved`。
  - 重新生成 `egressgen`。
- `proxy.go`：删除第 169～176 行和 `gzipBytes`（第 182 行起）。
- `egress_transport.go`：
  - 删除第 140 行的 `GzipResponse: true` 和第 154 行起的解压分支。
  - 原 32MiB 解压上限改为对响应体长度的同等校验。
- 理由：采集器（控制机）调出口代理（海外机）本就是跨主机 tRPC 调用，帧层已经压缩，业务层再 gzip 是重复压缩。

### 5.7 SCF 代码包

SCF 函数由 collector 模块构建，`marketfetch` 引用 `gatewayclient` 和 `wirecompress`，字典随代码包内嵌。

代码包与环境变量要成对更新：

- 新代码读不了旧的 gzip 计划。
- 旧代码读不了新的 zstd 计划。

发布顺序见第 8 节。

## 6. 字典训练工具与维护流程

- **工具**：`tools/zstddict`（独立 Go 程序，不进任何服务二进制）。
  - 输入：语料目录（每类一个子目录）、字典大小、字典 ID。
  - 输出：`packages/wirecompress/dicts/<class>-v<N>.zdict`，并打印评估表（同第 3.2 节的指标）。
  - 训练用 `zstd.BuildDict`。
  - 评估集与训练集按时间切分。
  - 训练前过滤：丢弃小于 32 字节的样本；单个样本截断到 64KB，避免大样本主导。
- **语料**：第 3.1 节的采集脚本整理进 `tools/zstddict/README.md`，只写步骤和过滤规则，不提交语料。
- **字典审查**：`tools/zstddict` 输出字典里长度不小于 8 的可打印串摘要，人工确认无敏感内容后再提交。
- **何时重训**：
  - 某类线上压缩比连续 24 小时低于发布时基线的 80%（第 4.4 节指标）。
  - 或者请求格式有较大变化，比如精简写请求格式。
- **上线后的重训发布**（以后才会遇到，这次不需要）：分两次发布。
  1. 新字典只加入解码集合。
  2. 全部组件发布完成后，再把编码器切到新字典。

  字典 ID 写在帧头里，解码端无需协调。

## 7. 测试计划

单元测试（`packages/wirecompress`）：

- 每个字典的往返一致性，含空输入、1 字节、刚好等于上限、超上限。
- 解压炸弹：构造 1KB → 1GB 的 zstd 帧，断言返回 `ErrTooLarge`，且内存峰值不超过上限加常数。
- 帧头声明的原始长度超上限时直接拒绝；不声明长度的帧、多帧拼接在解压过程中超限即停止，内存峰值不超过该实例上限加常数。
- 上限隔离：同一份 2MB 的合法帧，1MB 实例拒绝、64MB 实例接受。
- 未知字典 ID、损坏帧、截断帧都返回错误，不 panic。
- 并发 `Encode`/`Decode`（`-race`）。
- tRPC 注册：
  - `codec.GetCompressor(32)` 能取到。
  - gzip 和其他内置类型解压都被拒绝。
  - 用 `codec.Compress`/`codec.Decompress` 走一遍。

集成测试：

- `packages/gatewayclient`：
  - 跨主机端点（网关、中转、隧道、External）的调用带 zstd 类型，回环不带。
  - 服务端按请求类型返回压缩响应。
  - 沿用 `compression_test.go` 的假服务端。
- 主机网关：压缩请求 → 网关 → 本机上游收到未压缩体 → 调用方收到压缩响应，扩展 `hostgateway` 现有 e2e。
- `packages/events`：
  - 发布、消费往返。
  - 小于阈值不压缩。
  - 不可压缩数据按原样发布。
  - `+gzip` 和未知内容类型被拒绝。
- 指标：report 生成快照 → events → monitor 解析，校验和一致。
- 采集计划：编码、解码往返；超上限拒绝；现有计划在新编码下的环境变量体积断言（防止回退）。
- 出口代理：大响应经帧压缩往返，删除字段后的协议测试。

全量：`go test ./...`（go.work 下全部模块）、`go vet`、`git diff --check`。

## 8. 发布计划（一次性整体发布）

不兼容新旧组件，发布窗口内会有一次短暂中断，用暂停调度加补采兜底。

**准备**：

1. 第 0 阶段决策和字典提交后，同一提交构建全部组件，包括：
   - 存储（CGO）。
   - SCF 代码包。
   - 编译机的 factor-engine 和 archive-worker。
2. 核对生产各主机已安装配置里的 `compression:`，以及 systemd 环境里的 `MOOX_METRICS_GZIP_LEVEL`，准备好删除这些字段的配置变更（严格解析）。只用 SSH 公钥。
3. 记录发布前基线：
   - 各链路 65 秒抓包字节，方法同网关文档第 13 节。
   - 整机和进程 CPU。
   - 最近 1 小时的采集台账。

**执行**（避开整点和整分钟，每个重启落在每分钟第 10～35 秒）：

**第 1 步：持久化暂停采集。**

- 对每个采集空间调用 collector 的 `SetFetchPlanPaused(paused=true)`，开关持久化在 collector 库里。
- 轮询 `timers_disabled`，直到全部为 true。管理器约 1 分钟同步一次空间集合，暂停的空间会先停用定时器，再退出计划对账协程（`bootstrap/fetch_plan.go` 第 82 行）。
- 暂停期间 collector 即使重启，也不会对这些空间对账或重新启用定时器。这是第 5、6 步能安全分开执行的前提。

**第 2 步：建立事件排空屏障，然后清空流。**

存储 outbox 拿到 broker 确认就删除记录（`outbox/relay.go` 第 219～259 行），不等消费者处理。所以不能在发布端还在运行时检查积压，再停服务、清空流，中间产生的消息会丢。正确顺序：

1. 冻结全部业务事件来源：
   - 采集已在第 1 步暂停。
   - 等在途的因子计算、策略触发、补采、归档任务完成。
   - 确认不再有新写入进入存储。
2. 保持全部消费者运行。直到以下条件都满足，并维持至少 2 分钟：
   - 存储 outbox 积压为 0。
   - 各 JetStream 消费者的 `num_pending` 和 `num_ack_pending` 都为 0。
3. 停全部消费者和发布端，再清空各流（新代码不再能解 `+gzip`）。
   - 指标快照事件由各组件周期发布，无法冻结。清空时最多丢一个周期的指标，可以接受。

**第 3 步：替换二进制并清理配置。** 逐台逐组件：

- 存储机：主机网关、主机代理、primary/view/node、Access。
- 海外机：主机网关、主机代理、Access、trade、egressproxy。
- 控制机：主机网关、主机代理、admin、monitor、collector、cloudnode、factor-mgr、strategy、archive-mgr、EventBus 所在组件。
- 编译机：factor-engine、archive-worker。

collector 启动后，暂停的空间仍不对账。

**第 4 步：验证服务链路。** 不依赖采集即可验证：

- 跨主机读调用正常。
- 主机网关、Access 日志无解压错误。
- `moox_wirecompress_errors_total` 为 0。

**第 5 步：更新 SCF 代码包，此时定时器保持停用。**

- 通过 cloudnode 现有的代码包上传和节点部署流程（`executeDeployNodeItem` → `updateSCFFunctionCode`），把新代码包部署到全部采集函数。
- 读回每个函数的代码版本，确认已更新、定时器仍为停用。
- 实施时要核实部署流程不会改变定时器状态。如果部署请求必须带环境变量，就用新 collector 生成的新编码计划环境，让代码与环境同时变成新版。

**第 6 步：恢复采集。**

- 调用 `SetFetchPlanPaused(paused=false)`。
- 计划对账协程重新启动后，先执行 `EnableAll`。它在**同一个**运行配置补丁里同时下发新编码的受管环境变量和 `TimerEnabled: true`（`plan_manager.go` 第 179、442 行）。这时代码包已是新版，代码与环境一致后定时器才会触发。
- 读回环境变量哈希和定时器状态。

**第 7 步：观察与补齐。**

- 观察第一个整分钟周期。
- 检查补采：被暂停的分钟由补采补齐，台账无缺口。

**回滚**：回滚也是整体回滚，因为新旧版本互不兼容，要按与发布对称的屏障执行：

1. 持久化暂停采集，确认定时器已停用。
2. 用**新版本**建立同样的事件排空屏障（第 2 步）：冻结来源，新消费者处理完全部 `+zstd` 消息，`num_pending`/`num_ack_pending` 归零后，停服务、清空流。
   - 不能直接启动旧消费者：旧代码遇到解不开的消息会 `TERM`（`factor/internal/trigger/eventconsumer/handler.go` 第 90 行），投递终止，重新部署也不会恢复。
3. 如果新版本无法完成排空（例如解码缺陷），清空前先用 `nats stream backup` 保全各流。修复后用新版本恢复、重放，再继续回滚或重新发布。
4. 恢复每台每组件的 `bak-<旧提交>` 二进制和配置备份，与以往发布相同。
5. SCF：定时器停用状态下，通过 cloudnode 部署回旧代码包。然后恢复采集，由旧 collector 的 `EnableAll` 把旧编码环境和定时器一起下发。

## 9. 发布后验证

- **流量**：与发布前同窗口对比各链路字节，重点是海外机 ↔ 存储机和控制机 ↔ 存储机的 11003。期望在 gzip 基础上再降 25%～40%（以第 0 阶段的加权结果为准）。
- **压缩比**：查 `moox_wirecompress_bytes_total`，各类实际压缩比与第 0 阶段评估值的偏差应在 15% 以内。这个值同时作为日后重训的基线。
- **CPU**：整机和各进程 CPU 不高于发布前（zstd 比 gzip 快 4～5 倍）。SCF 记录单次执行时长和内存。
- **正确性**：
  - 采集台账连续 2 小时无缺口。
  - 事件消费无解码错误（`moox_wirecompress_errors_total` 为 0）。
  - 监控页指标正常刷新。
  - 计划环境变量体积下降。
- **抓包**：只抓明文回环口验证帧头压缩类型编号；用完删除，不保留含密钥的流量。
- **记录**：结果写入本文档第 11 节。

## 10. 风险与对策

| 风险 | 影响 | 对策 |
|---|---|---|
| 字典老化（新标的、新字段、时间戳变化） | 压缩比逐步回落，最差退化到无字典的 4.2x，仍优于现状 | 第 0 阶段跨天评估量化老化速度；4.4 节指标加 80% 阈值触发重训 |
| 字典里混入敏感串 | 字典进仓库、进所有二进制 | 采集时按服务过滤；字典提交前做可打印串审查（第 6 节） |
| 解压炸弹 / 恶意帧 | 解压发生在验签之前，可耗尽内存 | 每个使用点按自己的上限构造解码器（`WithDecoderMaxMemory` 在解压过程中生效），不做事后校验；专门的炸弹测试（第 7 节） |
| 编码器、解码器常驻内存 | 每个类别一套编码器，SCF 内存小 | 低内存选项、限制并发、1MB 窗口；第 0 阶段测 SCF 内存 |
| JetStream 里残留旧格式或旧字典的消息 | 消费端解码失败后 `TERM`，投递终止；在排空前清空会丢事件 | 发布与回滚都先冻结来源，等 outbox 和消费者积压归零后再清空流（第 8 节）；无法排空时先 `nats stream backup`；字典注册表只追加，退役字典保留超过最长保留期（168h） |
| 一次性发布中断 | 窗口内丢分钟 | 暂停调度、补采兜底；发布避开整点 |
| 配置严格解析 | 残留 `compression` 字段导致进程起不来（archive-worker 有前例） | 模板、编译机部署脚本（sh/ps1）一并删除并加契约测试；发布前逐台核对已安装配置；先换配置再重启 |
| SCF 代码包与环境变量不同步 | 函数解不开计划 | 持久化暂停（定时器停用、对账协程退出）期间先换代码包；恢复时 `EnableAll` 在同一补丁里下发新环境与定时器（第 8 节第 5、6 步） |
| 二进制体积增大 | 每个字典约 64KB | 可忽略；方案 B 最多 4 个字典 |
| 方案 B 的服务目录类别配错 | 只影响压缩比，不影响正确性（解码按帧头字典 ID） | `validate.go` 校验类别合法；指标按类别观测 |

## 11. 实测结果与决策记录

（第 0 阶段完成后填写：各类语料规模、A/B 对比表、字典大小、老化衰减、最终方案、阈值取值。）
