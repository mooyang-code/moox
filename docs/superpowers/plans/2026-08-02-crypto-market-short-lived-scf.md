# Crypto Market Short-Lived SCF Implementation Plan

> **历史计划，实时调度部分已被替代：** 短时函数、64MB/15s、一次聚合写 Storage 和 CLS 明细仍有效；“Collector 每分钟调用全部 realtime SCF”改由 [SCF 定时触发行情采集执行计划](2026-08-04-scf-timer-market-fetch.md) 接管。不要重新实现本文的 realtime `InvokeFunction` 链路。

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 将行情采集 SCF 彻底改造成 crypto_market 空间专属、按批次短时运行的函数，并删除常驻 Worker、心跳、Keepalive、SCF Sentinel/Watchdog 及遗留数据、监控链路。

**Architecture:** Collector 本地定时器扫描 crypto_market 规则，确定性分片后直接调用海外 SCF。函数只做一次 market_fetch 或出口探测、内存聚合、一次 Storage 写入、一个完成事件和一次 CLS 批量上报后返回。CLS 失败不影响行情结果。

**Tech Stack:** Go 1.25、Tencent SCF Go SDK、Tencent CLS Go SDK、tRPC-Go（仅保留 Storage/EventBus/timer 用途，不用于 CLS）、SQLite、JetStream、Vue 3、Vitest。

---

## 边界与验收

### 必须实现

- 所有业务空间、数据集、规则、Collector 状态、CloudNode 节点/包、函数名、EventBus subject 和 Monitor 检查统一为 crypto_market；不再保留 crypto 别名。
- SCF 只接受 market_fetch 和 egress_probe；保持 64 MB / 15s，Storage RPC 5s。
- 本地 Collector 在每分钟同一时刻调度 1m K 线。Scheduler 按活跃函数数轮询分片；函数内以 max_inflight_requests 分波并发。
- 每个标的无论成功或失败都生成一条结构化 CLS 记录；一轮函数调用只发一次 CLS 请求。
- 重试仍由 Collector 批次/重试表和 Timer 负责；SCF 不消费 JobItem、不保存跨调用状态、不在函数内等待重试。
- Monitor 保留批次、Dataset、K 线和出口探测新鲜度；删除心跳和 Sentinel 检查。

### 不做

- 不迁移、双写或兼容旧 crypto 数据。上线前删除旧空间、函数、目录数据后重建。
- 不实现常驻 SCF、WebSocket、错峰、分布式锁或 SCF 内部持久队列。
- 不让 CLS 成为采集成功的前置条件。日志尾部预算固定 800ms，超时后只写本地错误日志。
- 不复制 go-log-cls 的 tRPC Plugin、异步后台 Producer、全局初始化或 trpc_go.yaml；只借鉴结构化字段和 SDK 接入思路。

### 完成标准

1. 十个海外函数均为 64 MB / 15s，没有 timer、心跳或 Sentinel 环境变量。
2. 同一 1m 周期按函数数均匀轮询；单函数并发有上限，Storage UpsertFields 每批最多一次。
3. CLS 可按 batch_id 查到每个标的的 market_fetch_item，CLS 不可用不使批次失败。
4. 搜索 keepalive、ReportHeartbeat、scf_sentinel、scf_watchdog、trpc-log-cls 在运行时代码中无命中。
5. 清空旧 crypto 数据后，crypto_market 完成真实 Symbol 和连续三轮 1m K 线采集。

## 目标契约

    Collector Timer -> Scheduler crypto_market -> CloudNode InvokeFunction
       -> crypto_market SCF -> Binance HTTPS
       -> Storage RPC (one aggregate write)
       -> EventBus batch completed -> Collector completion/retry -> Monitor
       -> CLS (one structured upload)

### SCF 事件

    type CloudFunctionEvent struct {
        Action                  EventAction
        Data                    map[string]interface{}
        Timestamp               string
        RequestID               string
        StorageRPCGatewayTarget string
    }

data.space_id 必须与 MOOX_SPACE_ID=crypto_market 相等。Storage target 只在当前调用中使用；不得写入任何全局运行时配置。

### CLS 环境变量

| 变量 | 必填 | 含义 |
|---|---:|---|
| MOOX_CLS_ENABLED | 是 | true 启用，其他值为 Noop。 |
| MOOX_CLS_ENDPOINT | 启用时 | CLS API 上传域名。 |
| MOOX_CLS_LOGSET_ID | 启用时 | 初始化阶段创建并只读解析出的 Logset ID；用于部署核验和诊断。 |
| MOOX_CLS_TOPIC_ID | 启用时 | 集中 Topic ID。 |
| MOOX_CLS_SECRET_ID / MOOX_CLS_SECRET_KEY | 启用时 | 发布 CLI 注入，永不写入 zip。 |
| MOOX_CLS_TIMEOUT_MS | 否 | 默认 3000，范围 100..3000。 |
| MOOX_CLS_SOURCE | 否 | 空时由 Handler 使用函数名。 |

所有记录附加 space_id、function_name、region、request_id、batch_id、event_type=market_fetch_item；标的记录至少有 symbol、dataset_id、frequency、elapsed_ms、success、rows、error_kind、error_message。

### 时间预算

    Binance 工作窗口 = 15s - Storage 5s - completion 3s - CLS 3s - response 500ms
    Storage 聚合写入 = 最多 5s
    EventBus 完成事件 = 最多 3s（首次 TLS 连接也必须能完成）
    CLS 同步提交 = 最多 3s，且必须留下 500ms response reserve；跨地域 CLS 首次连接超时只记录，不改变 response

CLS 使用同步 SendLogList，一次调用只发送一次。单函数最多 64 个实时 item，远低于 SDK 的 10,000 条/5MB 限制。

## 文件结构

| 路径 | 责任 |
|---|---|
| packages/clsreporter/ | 新独立 Go module；显式 Config、环境变量读取、同步 CLS 批量上报和 Noop。 |
| modules/collector/cmd/scf/crypto_market/ | 唯一加密行情 SCF 入口。 |
| modules/collector/internal/serverless/crypto_market/ | 仅处理两个短时 action。 |
| modules/collector/internal/marketfetch/ | 有界并发、Storage 聚合、完成事件、CLS item 明细。 |
| modules/collector/configs/scf/crypto_market/ | 空间专属配置；无 tRPC logger、timer、watchdog。 |
| modules/cli/internal/ 和 scripts/build-collector-scf-package.sh | 入口选择、无密钥 zip、受管 SCF 环境变量。 |
| modules/cloudnode/ | 保留异步部署/调用；删除 resident-SCF 控制面。 |
| modules/monitor/ 和 web/src/ | 删除心跳/Sentinel 展示，保留批次和数据新鲜度。 |
| docs/architecture/scf-short-lived-market-fetch.md | 成本决策与维护边界。 |

## 实施任务

### Task 1: 固定决策记录和绿色重建边界

**Files:**
- Create: docs/architecture/scf-short-lived-market-fetch.md
- Create: docs/superpowers/specs/2026-08-02-crypto-market-short-lived-scf-design.md
- Modify: docs/README.md
- Test: git diff --check

- [ ] **Step 1: 写成本决策文档**

记录费用按内存和执行时长计算；常驻函数、60 秒 Keepalive、JetStream Consumer 等待任务、Sentinel 轮询都会使按分钟采集变成持续运行。记录短时批处理、一次 Storage 写入、一次完成事件、一次 CLS 批量上报；明确 Monitor 只对批次和数据新鲜度负责。

- [ ] **Step 2: 写删除契约**

设计规格列出 ReportHeartbeat、c_running_version、c_supported_workloads、c_last_heartbeat_at、MOOX_SCF_WATCHDOG_*、MOOX_SCF_CANARY_*、scf:heartbeat、external:scf_sentinel:*。保留 c_status 作为通用目录生命周期字段，但它不再表示短时函数在线状态。

- [ ] **Step 3: 链接并检查**

在 docs/README.md 增加决策文档链接。旧 docs/2026-08-01-scf-short-lived-market-fetch-execution-plan.md 只作为历史计划，不再改写。

Run: git diff --check

Expected: exit 0；没有 TODO/TBD 或双模式兼容表述。

- [ ] **Step 4: Commit**

    git add docs/architecture/scf-short-lived-market-fetch.md \
      docs/superpowers/specs/2026-08-02-crypto-market-short-lived-scf-design.md docs/README.md
    git commit -m "docs: record short-lived crypto market SCF design"

### Task 2: 新建不依赖 tRPC 的 CLS Reporter

**Files:**
- Create: packages/clsreporter/go.mod
- Create: packages/clsreporter/reporter.go
- Create: packages/clsreporter/env.go
- Create: packages/clsreporter/reporter_test.go
- Create: packages/clsreporter/env_test.go
- Modify: go.work
- Modify: modules/collector/go.mod
- Modify: modules/collector/go.sum

- [ ] **Step 1: 写环境变量失败测试**

覆盖关闭时返回 Noop；开启但缺 Endpoint、Topic 或任一密钥时报错；99 与 1001 报错；800 解析为 800ms。

    cfg, enabled, err := ConfigFromEnv(func(key string) string { return values[key] })
    require.True(t, enabled)
    require.Equal(t, 800*time.Millisecond, cfg.Timeout)
    require.Equal(t, "topic-a", cfg.TopicID)

- [ ] **Step 2: 实现显式 Config 与接口**

    type Config struct {
        Endpoint, TopicID, SecretID, SecretKey, Source string
        Timeout time.Duration
    }
    type Entry struct {
        Timestamp time.Time
        Fields    map[string]string
    }
    type Reporter interface {
        Report(Entry)
        Flush(context.Context) error
    }
    func New(Config) (Reporter, error)
    func Noop() Reporter

New 使用 CLS SDK SyncProducerClient；Report 在 mutex 中复制 fields；Flush 只调用一次 SendLogList。超过 10,000 条或 5MB 返回错误。公共包不得 import trpc.group，也不得读取 YAML 或全局配置。

- [ ] **Step 3: 实现 ConfigFromEnv**

读取 MOOX_CLS_ENABLED、ENDPOINT、TOPIC_ID、SECRET_ID、SECRET_KEY、SOURCE、TIMEOUT_MS。禁用时直接返回 Noop 标记；启用时所有必填值都必须存在。

- [ ] **Step 4: 写同步和并发测试**

使用 package-private fake sender，断言 64 个 goroutine Report 后仅一次发送；调用方事后修改 map 不影响缓存；Flush context 取消时错误原样返回；Noop 不发网络请求。

- [ ] **Step 5: 加入工作区并移除 Plugin 依赖**

    go work use ./packages/clsreporter
    cd modules/collector
    go mod edit -require=github.com/mooyang-code/moox/packages/clsreporter@v0.0.0-00010101000000-000000000000
    go mod edit -replace=github.com/mooyang-code/moox/packages/clsreporter=../../packages/clsreporter
    go mod edit -droprequire=trpc.group/trpc-go/trpc-log-cls
    go mod tidy

- [ ] **Step 6: 验证并提交**

Run: go test ./packages/clsreporter && (cd modules/collector && go test ./cmd/scf/... ./internal/marketfetch)

Expected: PASS；Collector 不再直接 require trpc-log-cls。

    git add go.work packages/clsreporter modules/collector/go.mod modules/collector/go.sum
    git commit -m "feat: add framework-free CLS reporter"

### Task 3: 收敛为 crypto_market 专属短时入口

**Files:**
- Create: modules/collector/cmd/scf/crypto_market/main.go
- Create: modules/collector/cmd/scf/crypto_market/main_test.go
- Create: modules/collector/internal/serverless/crypto_market/handler.go
- Create: modules/collector/internal/serverless/crypto_market/handler_test.go
- Modify: modules/collector/internal/model/types.go
- Modify: modules/collector/internal/model/types_test.go
- Modify: modules/collector/internal/marketfetch/handler.go
- Modify: modules/collector/internal/marketfetch/handler_test.go
- Delete: modules/collector/cmd/scf/main.go and main_test.go
- Delete: modules/collector/internal/serverless/handler.go and handler_test.go
- Delete: modules/collector/internal/serverless/bootstrap/services.go and services_test.go
- Delete: modules/collector/internal/serverless/market_canary.go and market_canary_test.go
- Delete: modules/collector/internal/serverless/watchdog.go and watchdog_test.go
- Modify/Delete: modules/collector/internal/app/runtime/global.go and global_test.go

- [ ] **Step 1: 写专属 Handler 测试**

构造 market_fetch 事件，断言 StorageRPCGatewayTarget 直接传给 marketfetch.Handler.NewStorage；拒绝 keepalive、watchdog、market_canary、service_deployments。让 Reporter Flush 失败，断言已成功的 Storage/完成事件仍返回成功。

- [ ] **Step 2: 实现入口和调用级 Reporter**

cmd/scf/crypto_market/main.go 只加载业务配置并调用 cloudfunction.Start，要求 MOOX_SPACE_ID=crypto_market；移除 trpc.NewServer、CLS blank import、NodeInfo/readiness 初始化。

Handler 从 function context 读取函数名、地域和 request id；用 clsreporter.ConfigFromEnv(os.Getenv) 创建 Reporter，注入公共字段。结束时创建不超过 CLS timeout 的 flush context；Flush 错误写 cls_report_flush_failed，但不得覆盖业务 response。

- [ ] **Step 3: 删除动态部署/心跳模型**

CloudFunctionEvent 收敛为本计划事件契约。删除仅由旧路径使用的 HeartbeatPayload、TaskSummary、NodeInfo、NodeMetrics、ProbeResponse、HeartbeatInfo、ServiceDeployment、TaskExecuteEvent；删除前用 rg 确认无非 SCF 调用方。删除 runtime 的 service gateway、node identity、readiness 与 Storage target 全局状态。

- [ ] **Step 4: 留足 CLS 尾部预算**

    const completionPublishReserve = 3 * time.Second
    const clsFlushReserve = 3 * time.Second

    func executionReserves(storage time.Duration) (commit, publish, cls time.Duration) {
        return storage + completionPublishReserve + clsFlushReserve,
            completionPublishReserve, clsFlushReserve
    }

Executor.CommitReserve 使用完整 commit reserve；NewStorage 只读取当前 event target，拒绝空地址。

- [ ] **Step 5: 验证并提交**

Run: cd modules/collector && go test ./cmd/scf/... ./internal/serverless/... ./internal/marketfetch ./internal/model ./internal/app/runtime

Expected: PASS；已删除 package 无引用。

    git add modules/collector
    git commit -m "refactor(collector): isolate crypto market short-lived SCF"

### Task 4: 用 GoAndWait 实现有界并发和 CLS 标的明细

**Files:**
- Modify: modules/collector/internal/marketfetch/executor.go
- Modify: modules/collector/internal/marketfetch/executor_test.go
- Modify: modules/collector/internal/marketfetch/handler.go
- Modify: modules/collector/internal/marketfetch/handler_test.go

- [ ] **Step 1: 写波次并发测试**

九个阻塞 fake item，Concurrency=3，断言最大同时执行数为 3，前三项结束前第四项不开始，最终结果顺序仍与请求一致。这防止把全部 item 一次传给 GoAndWait 的伪限流实现。

- [ ] **Step 2: 替换 semaphore 和 WaitGroup**

    for start := 0; start < len(req.Items); start += concurrency {
        end := min(start+concurrency, len(req.Items))
        handlers := make([]func() error, 0, end-start)
        for index := start; index < end; index++ {
            index, item := index, req.Items[index]
            handlers = append(handlers, func() error {
                result, rows, regs, symbols := e.executeItem(workCtx, req, item)
                record(index, result, rows, regs, symbols)
                return nil
            })
        }
        if err := trpc.GoAndWait(handlers...); err != nil { return nil, err }
    }

record 在 mutex 中写独立槽位，波次结束后主 goroutine 按 index 聚合。取消时未启动项写 deadline_exhausted，仍发送 completion。

- [ ] **Step 3: 注入小型 ItemReporter**

    type ItemReporter interface { Report(clsreporter.Entry) }
    type Executor struct { /* existing fields */ Reporter ItemReporter }

成功记录 success=true、rows、latest time；失败记录 success=false、network_error/storage_error/source_error/deadline_exhausted 和截断错误。保留当前本地成功/失败日志。

- [ ] **Step 4: 写字段和 Storage 失败测试**

断言每个 item 精确一条 Reporter entry，字段含 batch、symbol、elapsed、success、error_kind。Storage 聚合失败时，原成功 item 必须改为 storage_error 后上报。

- [ ] **Step 5: 验证并提交**

Run: cd modules/collector && go test ./internal/marketfetch ./internal/sources/binance -race

Expected: PASS；无 race，单批最多一次 Storage 写。

    git add modules/collector/internal/marketfetch
    git commit -m "refactor(collector): bound SCF fetch waves with trpc wait"

### Task 5: 按 crypto_market 入口打包和发布

**Files:**
- Create: modules/collector/configs/scf/crypto_market/config.yaml
- Create: modules/collector/configs/scf/crypto_market/sources/market/binance.yaml
- Create: modules/collector/configs/scf/crypto_market/observability.env.example
- Modify: modules/cli/internal/setup/config/config.go and config_test.go
- Modify: modules/cli/internal/collectorpackager/scf.go and scf_test.go
- Modify: modules/cli/internal/command/collector.go and collector_test.go
- Modify: scripts/build-collector-scf-package.sh and its test
- Modify: scripts/build.sh
- Modify: custom.toml.example
- Delete: modules/collector/configs/scf/crypto/

- [ ] **Step 1: 写入口 manifest 测试**

增加 Entrypoint。接受：

    space_id = "crypto_market"
    entrypoint = "crypto_market"
    package_config_dir = "scf/crypto_market"
    package_name = "moox-collector-crypto-market"
    function_prefix = "moox-fetcher-crypto-market"

拒绝空入口、路径穿越、非 [a-z][a-z0-9_]{0,63}、或仍包含 crypto 的路径/包名前缀。

- [ ] **Step 2: 清理 runtime config**

configs/scf/crypto_market/config.yaml 仅保留 Binance source、Storage RPC、服务鉴权等实际配置。删除 dnsproxy、timer、SCF watchdog、market canary、tRPC logger。环境样例只保留 CLS、Storage、Gateway、EventBus、space/fetch 参数。

- [ ] **Step 3: 入口化构建**

packageCollectorFunction/buildCollectorLinuxBinary 和脚本接收 entrypoint，构建：

    go build -ldflags "-s -w -X main.Version=<version>" \
      -o <binary> ./cmd/scf/<entrypoint>

脚本要求 SCF_ENTRYPOINT=crypto_market，并校验安全目录名。scripts/build.sh 产物名改为 moox-collector-scf-crypto-market。

- [ ] **Step 4: 删除 trpc_go.yaml 渲染**

从 BuildSCFPackageOptions 删除 CLSTopicID，删除 addServerlessTRPCConfig/renderTRPCConfigForServerless。zip 必须有 main/config/sources，且不得含 trpc_go.yaml、example_trpc_go.yaml、CLS Secret 或明文 Topic。

- [ ] **Step 5: 受管环境变量注入**

collectorFunctionEnvironment 固定注入：

    MOOX_CLS_ENABLED=true
    MOOX_CLS_ENDPOINT=<resolved public ingest endpoint>
    MOOX_CLS_LOGSET_ID=<resolved logset>
    MOOX_CLS_TOPIC_ID=<resolved topic>
    MOOX_CLS_TIMEOUT_MS=3000
    MOOX_CLS_SECRET_ID=<resolved id>
    MOOX_CLS_SECRET_KEY=<resolved key>

将其加入 managed map，拒绝 --env 覆盖，删除 MOOX_CLS_HOST。发布只读解析初始化阶段已有的 CLS Logset/Topic；COS bucket 仍由 CloudNode 的已注册云账户提供给部署 API，SCF 运行时不使用 COS，因此不注入额外 bucket 或云凭据。

- [ ] **Step 6: 写 package/publish 回归测试**

断言 zip 来自专属入口且无 tRPC YAML；CreateItem 的 space/package/prefix/env 都是 crypto_market；新加坡、东京各上传一份并创建五个节点；CLS endpoint/topic/timeout/密钥均受管。

- [ ] **Step 7: 验证并提交**

    cd modules/cli && go test ./internal/setup/config ./internal/collectorpackager ./internal/command
    bash -n scripts/build-collector-scf-package.sh scripts/build-collector-scf-package_test.sh
    bash scripts/build-collector-scf-package_test.sh

Expected: PASS；不存在 configs/scf/crypto。

    git add custom.toml.example modules/collector/configs modules/cli scripts go.work
    git commit -m "feat(cli): package crypto market SCF without trpc CLS"

### Task 6: 删除 CloudNode 的常驻 SCF 控制面

**Files:**
- Modify: modules/cloudnode/schema/cloudnode.sql
- Modify: modules/cloudnode/internal/store/models.go, node.go, node_test.go, catalog_test.go
- Delete: modules/cloudnode/internal/projection/heartbeat_buffer.go and heartbeat_buffer_test.go
- Delete: modules/cloudnode/internal/rpc/heartbeat_maintainer.go and heartbeat_maintainer_test.go
- Delete: modules/cloudnode/internal/observability/scf_metrics.go and scf_metrics_test.go
- Modify: modules/cloudnode/internal/bootstrap/bootstrap.go
- Modify: modules/cloudnode/internal/rpc/server.go, node.go, invocation.go
- Modify: modules/cloudnode/internal/store/invocation.go
- Modify: modules/cloudnode/proto/cloudnode.proto
- Regenerate: modules/cloudnode/proto/cloudnodegen/
- Modify: modules/cloudnode/README.md

- [ ] **Step 1: 写 schema/catalog 回归测试**

schema.AllSQL 不含 c_running_version、c_supported_workloads、c_last_heartbeat_at。market_fetcher 节点只按 c_is_deleted 和 metadata deployment_ready 被 Scheduler 列出。ONLINE/TIMEOUT 不再读取心跳时间；通用 c_status 只表示目录生命周期。

- [ ] **Step 2: 删除心跳存储和维护器**

删除三个 schema/model 字段，以及 scfHeartbeatTimeout、ListSCFEventNodes、UpdateHeartbeat、SCFHeartbeatStatus、deriveSCFHeartbeatStatus、HeartbeatBuffer、HeartbeatMaintainer、SCF Metrics。UpdateNodeDeployment 不再镜像 supported workloads。

- [ ] **Step 3: 删除 RPC、Proto 和启动 timer**

删除 ReportHeartbeat RPC/message，以及 CloudNode 的 running_version、supported_workloads、heartbeat_interval、last_heartbeat。删除 WithHeartbeatSink、bootstrap heartbeat timer、资源关闭和 imports。串行执行：

    make -C modules/cloudnode/proto all

- [ ] **Step 4: 删除 Sentinel 选择分支**

从 rpc/node.go 删除 scf_watchdog_enabled、requestedSCFWatchdog、ensureSingleSCFWatchdog、Watchdog 环境补全及部署校验。market_fetcher metadata 只保留 biz type、deployment readiness 与调度配置。

- [ ] **Step 5: 隔离旧 JobItem 路径**

检查 store/invocation.go/rpc/invocation.go。删除依赖心跳/工作负载的 SCF 筛选；若通用 JobItem 仍服务其他模块，明确排除 market_fetcher，确保它只能由 scfinvoker.Client.ListMarketFetchers 选择。

- [ ] **Step 6: 验证并提交**

    make -C modules/cloudnode/proto all
    cd modules/cloudnode && go test ./internal/store ./internal/rpc ./internal/bootstrap ./schema

Expected: PASS；生成 pb 无 ReportHeartbeat 或已删除字段访问器。

    git add modules/cloudnode
    git commit -m "refactor(cloudnode): remove resident SCF heartbeat control"

### Task 7: 删除 Monitor、告警和 Web 的心跳/Sentinel

**Files:**
- Modify: modules/monitor/internal/bootstrap/bootstrap.go, business_freshness.go, default_alerts.go
- Delete: modules/monitor/internal/bootstrap/external_health.go and external_health_test.go
- Modify: modules/monitor/internal/observability/overview.go
- Modify: modules/monitor/internal/rpc/observability.go
- Modify: modules/monitor/proto/monitor.proto
- Regenerate: modules/monitor/proto/monitorgen/
- Modify: modules/monitor/internal/alerting/webhook.go and tests
- Modify: web/src/api/cloud-node.ts
- Modify: web/src/views/collector/cloud-node/cloud-node.vue
- Modify: web/src/views/collector/cloud-node/cloud-node-model.ts and its test
- Modify: web/src/views/collector/cloud-node/cloud-node-batch-service.ts

- [ ] **Step 1: 写 Monitor 正向测试**

断言 bootstrap 只注册 market-fetch completion/freshness、Dataset/业务 Canary；不创建 default:scf:heartbeat 或 default:external:scf_sentinel:*。保留最近成功批次过期时的中文 market-fetch 告警。

- [ ] **Step 2: 删除外部 Sentinel 和心跳摘要**

删除 external health route、scf_sentinel observer、SCF heartbeat metrics summary、业务新鲜度心跳结果及中文诊断。Overview Proto 删除 ScfObservabilitySummary，不保留空对象。

- [ ] **Step 3: 收敛 Webhook 和 Web**

Webhook 删除 Sentinel/heartbeat 特判，保留 batch 失败、批次过期、待重试数量、Dataset/Frequency 和短错误摘要。Web 删除 running_version、supported_workloads、heartbeat_interval、last_heartbeat 的 API、表格列、详情及编辑表单；仍显示空间、地域、函数、包版本和部署状态。

- [ ] **Step 4: 生成、验证并提交**

    make -C modules/monitor/proto all
    cd modules/monitor && go test ./internal/bootstrap ./internal/observability ./internal/rpc ./internal/alerting
    pnpm --dir web test -- cloud-node-model.test.ts
    pnpm --dir web build:dev

Expected: PASS；页面不存在心跳字段。

    git add modules/monitor web
    git commit -m "refactor(monitor): remove SCF heartbeat and sentinel checks"

### Task 8: 清空 crypto 并重建 crypto_market

**Files:**
- Modify: ignored custom.toml（部署时，不提交）
- Modify: custom.toml.example
- Modify: 初始化 seed、部署文档中所有 crypto 空间值
- Test: 真实 CLI、Storage、SCF、CLS、Monitor 验证

- [ ] **Step 1: 停止和盘点旧资源**

暂停 Collector timer。只读导出 crypto 的 Admin Space、Storage datasets/subjects、Collector rules/instances/batches/retries、CloudNode packages/nodes/batches、Monitor checks/results 和全部旧 SCF。用 moox-cli 异步删除旧函数并等待 job 完成，不在控制台逐个手工删除。

- [ ] **Step 2: 删除旧数据而非改 ID**

删除 crypto 的关联记录和时间序列。SQLite 为绿地重建：停服务后删除旧数据库文件或重建目标库，不写 ALTER TABLE 兼容迁移；重启后由新 schema 创建无心跳列 catalog。

- [ ] **Step 3: 初始化元数据和任务**

创建 Admin Space crypto_market，注册 Binance spot Symbol Dataset 和引用它的 1m Kline Dataset/规则。手动 Symbol task 可带 allowlist；普通 Kline task 必须引用 Symbol Dataset。所有 subject、series tag、EventBus SubjectID、Monitor check 和 rule 都使用 crypto_market。

- [ ] **Step 4: 配置十个海外函数**

    [scf_fetcher]
    enabled = true

    [[scf_fetcher.spaces]]
    space_id = "crypto_market"
    entrypoint = "crypto_market"
    package_config_dir = "scf/crypto_market"
    package_name = "moox-collector-crypto-market"
    function_prefix = "moox-fetcher-crypto-market"
    memory_size = 64
    timeout_seconds = 15
    storage_timeout_ms = 5000
    max_inflight_requests = 32

保留 ap-singapore 五个、ap-tokyo 五个；每个地域使用独立 Cloud Account/COS 包。

- [ ] **Step 5: 发布和出口验证**

    go run ./modules/cli/cmd/moox-cli setup validate --file ./custom.toml
    go run ./modules/cli/cmd/moox-cli collector function publish submit \
      --file ./custom.toml --space-id crypto_market
    go run ./modules/cli/cmd/moox-cli collector function probe-egress \
      --file ./custom.toml --space-id crypto_market

确认十个函数均为 64MB/15s，环境中有 MOOX_CLS_*，没有 MOOX_SCF_WATCHDOG_* 或 MOOX_SCF_CANARY_*。

- [ ] **Step 6: 启动真实采集并验证**

先启用 Symbol task，等待成功完成事件；再启用 1m rule 并连续运行至少三个周期。每个启用 Dataset + Frequency 必须有两根连续已收盘 K 线，价格/成交量非空，venue:binance tag 正确；批次成功且无永久 retry；分片覆盖十个函数；同一 batch_id 在 CLS 中每标的一条明细。执行一次受控 CLS endpoint 失败测试，确认只出现 cls_report_flush_failed，采集仍成功。Monitor 只出现 market fetch/Dataset 检查。

- [ ] **Step 7: Commit 样例和记录**

    git add custom.toml.example docs/architecture/scf-short-lived-market-fetch.md
    git commit -m "docs: document crypto market SCF rebuild procedure"

不得提交 custom.toml、CLS 密钥、EventBus 凭据或 Storage HMAC。

### Task 9: 全量回归、遗留扫描和独立审查

**Files:**
- Modify: docs/architecture/scf-short-lived-market-fetch.md（仅验证结果需要修正时）
- Test: 下列全部命令

- [ ] **Step 1: 模块级回归**

    go work sync
    ./scripts/test-go-workspace.sh
    cd modules/collector && go test ./...
    cd ../cloudnode && go test ./...
    cd ../monitor && go test ./...
    pnpm --dir ../../web test
    pnpm --dir ../../web build:dev
    git diff --check

不得用 git reset、git checkout 或回退工作区原有改动掩盖失败。

- [ ] **Step 2: 遗留路径扫描**

    rg -n -i 'ReportHeartbeat|HeartbeatBuffer|scf_heartbeat_maintainer|keepalive|scf_sentinel|scf_watchdog|trpc-log-cls|MOOX_CLS_HOST' \
      modules/collector modules/cloudnode modules/monitor packages web scripts

Expected: 无运行时代码命中；历史文档保留时逐条说明。

- [ ] **Step 3: 独立 code review**

使用 codeCR 审查 Collector、CloudNode、Monitor、CLI 和 packages/clsreporter。重点：15 秒预算、CLS 并发安全/密钥泄露、crypto 调度残留、删除 Proto 后的 UI/调用方，以及重新进入 JobItem/Keepalive 的可能性。

- [ ] **Step 4: 修复、提交并推送**

每个 finding 记录文件/行号、修复 commit 和回归命令；无 finding 时记录剩余风险：CLS 是 best-effort，短暂不可达会丢远端明细但不会丢 Storage 数据。

    git status --short
    git add <only-files-owned-by-this-change>
    git commit -m "refactor: rebuild crypto market short-lived SCF fleet"
    git push origin feature/mooyang
    git ls-remote --heads origin feature/mooyang

Expected: 不提交或回退无关工作区改动，远端 feature/mooyang 指向最终 commit。

## 实施顺序与风险控制

严格按 Task 1 到 9 推进。CloudNode 和 Monitor Proto 生成必须独占工作区，不可与 Go 测试并发。Task 8 是唯一破坏性阶段，必须在本地单测、zip 内容测试和 CLI validate 均通过后执行。

回退不恢复旧常驻架构：保留上一版短时 zip 的版本号，暂停新规则、修复后重新发布即可。项目尚未上线，不恢复或迁移旧 crypto 数据与 Sentinel 状态。
