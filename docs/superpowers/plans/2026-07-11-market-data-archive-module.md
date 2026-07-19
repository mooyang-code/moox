# Market Data Archive Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 MooX 中新增纯 Go、单实例运行的行情数据归档模块，可靠消费 Storage 的已闭合 K 线事件，并按市场、数据集、频率、标的和 UTC 月份永久物化为本地宽表 Parquet 文件，可选同步 COS。

**Architecture:** `modules/archive` 先将通过校验的 NATS JetStream 事件以同步 Pebble batch 写入 journal，ACK 与 Parquet/COS 解耦；后台归档写入器以 partition 高水位读取 pending patch，合并既有文件后通过同目录临时文件、校验、原子 rename 和目录 fsync 提交新 generation。Storage Metadata RPC 只登记已提交的本地文件，COS 只复制稳定 generation；本地 Parquet 和尚未物化的 journal 永不由部署或维护命令删除。

**Tech Stack:** Go 1.24、NATS JetStream、`packages/jetstream`、Pebble v1.1.5、`parquet-go` v0.25.1、ZSTD、Storage protobuf/tRPC、腾讯云 COS Go SDK v0.7.70、Prometheus、YAML v3

---

## 1. 开工前约束

- 设计基线：`docs/行情数据归档模块设计.md`。
- 实现目录：`modules/archive`；不得导入任何 `modules/storage/internal` 包。
- Market Space ID 固定为 `stock_cn`、`stock_us`、`crypto_binance`、`crypto_okx`，不得从名称拆分推导市场。
- 文件布局固定为：

```text
{root_dir}/{space_id}/{dataset_id}/{freq}/{subject_id}/
  {space_id}__{dataset_id}__{subject_id}__{freq}__{YYYYMM}.parquet
```

- `subject_id` 目录下直接放 Parquet，不增加年、月目录。
- NATS ACK 的唯一前置条件是整个事件已经通过 `pebble.Sync` 原子写入 journal；Parquet、Metadata、COS 均不得阻塞 ACK。
- 本地永久保存包括 Parquet 与仍承载未物化逻辑行的 archive state。部署的 `--reset-data` 也必须保留 `data/archive` 和 `data/archive-state`。
- CLI 对 Pebble 使用同一独占锁。Server 运行时，修改型 CLI 命令返回清晰错误；运维先停 Archive Server，再执行 `backfill`、`compact` 或 `sync-cos`。JetStream durable 在停机期间保留在线增量。
- 每个任务完成后使用 `go test -count=1` 获取新鲜测试结果，并按任务独立提交。

## 2. 关键数据合同

### 2.1 领域类型

以下类型名和字段名在后续任务中保持不变：

```go
package domain

import (
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type PartitionKey struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
	Month     string `json:"month"`
}

type Scalar struct {
	Type   storagepb.FieldValueType `json:"type"`
	String *string                  `json:"string,omitempty"`
	Int    *int64                   `json:"int,omitempty"`
	Double *float64                 `json:"double,omitempty"`
	Bool   *bool                    `json:"bool,omitempty"`
	Time   *string                  `json:"time,omitempty"`
	JSON   *string                  `json:"json,omitempty"`
	Bytes  *[]byte                  `json:"bytes,omitempty"`
}

type RowPatch struct {
	Partition      PartitionKey      `json:"partition"`
	DataTime       time.Time         `json:"data_time"`
	DimensionsJSON string            `json:"dimensions_json"`
	Attributes     map[string]string `json:"attributes"`
	WrittenAt      time.Time         `json:"written_at"`
	Columns        map[string]Scalar `json:"columns"`
}

type EventBatch struct {
	MessageID string     `json:"message_id"`
	Rows      []RowPatch `json:"rows"`
}

type ArchiveRow struct {
	Partition      PartitionKey
	DataTime       time.Time
	DimensionsJSON string
	Attributes     map[string]string
	WrittenAt      time.Time
	Columns        map[string]Scalar
}

type Manifest struct {
	Path           string    `json:"path"`
	Generation     uint64    `json:"generation"`
	SHA256         string    `json:"sha256"`
	Size           int64     `json:"size"`
	RowCount       uint64    `json:"row_count"`
	MinTime        time.Time `json:"min_time"`
	MaxTime        time.Time `json:"max_time"`
	Columns        []string  `json:"columns"`
	MaterializedAt time.Time `json:"materialized_at"`
}

type COSState struct {
	Status       string    `json:"status"`
	Generation   uint64    `json:"generation"`
	ObjectKey    string    `json:"object_key"`
	LastAttempt  time.Time `json:"last_attempt"`
	LastError    string    `json:"last_error"`
	NextRetry    time.Time `json:"next_retry"`
}
```

`Scalar` 使用指针区分零值与缺失值；`INT=0`、`DOUBLE=0`、`BOOL=false` 均是有效值。Storage 未携带的业务列不会进入 `RowPatch.Columns`，物化合并时保留旧值。

### 2.2 Journal key 与状态机

```text
meta/version                                      -> uint32(1)
meta/next-seq                                     -> uint64 big-endian
message/<message-id>                              -> MessageReceipt JSON
partition/<partition-id>                          -> PartitionState JSON
schema/<space-id>/<dataset-id>/<column-name>       -> FieldValueType int32
pending/<partition-id>/<seq-be>/<row-key-hash>    -> RowPatch JSON
quarantine/<unix-nano>/<message-id>                -> QuarantineRecord JSON
```

`partition-id` 是五个原始身份字段稳定编码后的 SHA-256 hex；`row-key-hash` 是 `data_time UTC nanos + "\n" + dimensions_json` 的 SHA-256 hex。每个新事件占用一个单调 `seq`，message receipt、全部 pending key、schema 变化和 partition dirty 状态在同一个 `pebble.Batch.Commit(pebble.Sync)` 中提交。重投的 `message_id` 命中 receipt 后不再生成 seq 或 patch，Consumer 可直接 ACK，避免旧事件在新修订之后再次覆盖列值。

```go
type PartitionPhase string

const (
	PhaseDirty          PartitionPhase = "dirty"
	PhaseWriting        PartitionPhase = "writing"
	PhaseLocalCommitted PartitionPhase = "local_committed"
	PhaseRegistered     PartitionPhase = "registered"
	PhaseClean          PartitionPhase = "clean"
)

type PartitionState struct {
	Key                domain.PartitionKey              `json:"key"`
	Phase              PartitionPhase                   `json:"phase"`
	HighWaterSeq       uint64                           `json:"high_water_seq"`
	MaterializingSeq   uint64                           `json:"materializing_seq"`
	Generation         uint64                           `json:"generation"`
	Sealed             bool                             `json:"sealed"`
	StartedAt          time.Time                        `json:"started_at"`
	LastMaterializedAt time.Time                        `json:"last_materialized_at"`
	Schema             map[string]storagepb.FieldValueType `json:"schema"`
	Manifest           *domain.Manifest                 `json:"manifest,omitempty"`
	COS                domain.COSState                  `json:"cos"`
}
```

归档写入器调用 `BeginMaterialization` 固定 `MaterializingSeq=HighWaterSeq`。提交完成后只删除 `seq <= MaterializingSeq` 的 pending key；物化期间写入的更高序号保留，partition 回到 `dirty`，因此不会因并发到达的修订而丢失。

### 2.3 物理提交状态

```text
dirty
  -> writing(generation, materializing_seq, started_at)
  -> local_committed(manifest)
  -> registered
  -> clean                       当无更高 pending seq
  -> dirty                       当 high_water_seq > materializing_seq
```

进程崩溃恢复规则：

1. `writing` 且目标文件 metadata 的 generation 等于状态中的 generation：重新验证目标文件，推进到 `local_committed`。
2. `writing` 且只有对应临时文件：删除临时文件并用相同 generation、started_at 和 high water 重写。
3. `local_committed`：不重写 Parquet，只重试 Metadata 登记。
4. `registered`：删除不超过高水位的 pending key，再转为 `clean` 或 `dirty`。

## 3. 目标文件地图

### 新模块

```text
modules/archive/
  go.mod
  go.sum
  config/app.yaml
  cmd/server/main.go
  cmd/cli/main.go
  cmd/cli/main_test.go
  internal/config/config.go
  internal/config/config_test.go
  internal/domain/identity.go
  internal/domain/identity_test.go
  internal/domain/row.go
  internal/domain/row_test.go
  internal/journal/keys.go
  internal/journal/store.go
  internal/journal/store_test.go
  internal/consumer/decode.go
  internal/consumer/decode_test.go
  internal/consumer/handler.go
  internal/consumer/handler_test.go
  internal/consumer/runner.go
  internal/parquetio/schema.go
  internal/parquetio/codec.go
  internal/parquetio/codec_test.go
  internal/writer/writer.go
  internal/writer/writer_test.go
  internal/writer/recovery.go
  internal/writer/recovery_test.go
  internal/writer/scheduler.go
  internal/writer/scheduler_test.go
  internal/registry/client.go
  internal/registry/client_test.go
  internal/backfill/backfill.go
  internal/backfill/backfill_test.go
  internal/cosstore/client.go
  internal/cosstore/client_test.go
  internal/health/state.go
  internal/health/server.go
  internal/health/server_test.go
  internal/bootstrap/app.go
  internal/bootstrap/app_test.go
  test/archive_e2e_test.go
  test/archive_fault_test.go
  test/benchmark_test.go
```

### 修改的工作区与部署文件

```text
go.work
packages/jetstream/consumer.go
packages/jetstream/consumer_test.go
modules/eventbus/config/app.yaml
modules/eventbus/internal/config/config.go
modules/eventbus/internal/config/config_test.go
modules/eventbus/internal/registry/registry_test.go
modules/admin/internal/service/sysdeploy/defaults.go
modules/admin/internal/service/sysdeploy/defaults_test.go
scripts/build.sh
scripts/release.sh
scripts/deploy-moox.sh
```

### 移除或收敛的旧 Storage 归档文件

```text
modules/storage/internal/services/archive/
modules/storage/internal/service/archive/parquet/
modules/storage/cmd/server/main.go
modules/storage/config/trpc_go.yaml
modules/storage/internal/config/loader.go
modules/storage/internal/config/loader_test.go
modules/storage/internal/services/access/options.go
modules/storage/internal/services/access/service.go
modules/storage/config/storage.yaml
modules/storage/config/storage.access.yaml
modules/storage/config/storage.view.yaml
modules/storage/config/storage.view_builder.yaml
modules/storage/config/storage.view_index.yaml
modules/storage/config/storage.view_query.yaml
modules/storage/config/metadata.seed.yaml
modules/storage/README.md
```

### 文档与读取示例

```text
docs/行情数据归档模块设计.md
docs/行情数据归档运维手册.md
examples/archive/read_symbol.py
```

`examples/archive/read_symbol.py` 只用于格式互操作演示；归档、合并、校验、回填和 COS 同步逻辑全部使用 Go。

## 4. 分阶段执行计划

### Task 1: 建立 Archive module 与严格配置

**Files:**
- Create: `modules/archive/go.mod`
- Create: `modules/archive/config/app.yaml`
- Create: `modules/archive/internal/config/config.go`
- Create: `modules/archive/internal/config/config_test.go`
- Modify: `go.work`

- [ ] **Step 1: 写配置默认值和校验失败测试**

```go
func TestLoadDefaultsAndMarketSources(t *testing.T) {
	cfg := loadFixture(t, minimalYAML)
	if cfg.Archive.DeviceID != "parquet-local" || cfg.Health.Addr != "127.0.0.1:11416" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	want := []string{"crypto_binance", "crypto_okx", "stock_cn", "stock_us"}
	if got := cfg.SourceSpaceIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("SourceSpaceIDs() = %v, want %v", got, want)
	}
}

func TestValidateRejectsOverlappingRootAndState(t *testing.T) {
	cfg := Default()
	cfg.Archive.RootDir = "/data/archive"
	cfg.Archive.StateDir = "/data/archive/state"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not overlap") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsCOSWithoutLocation(t *testing.T) {
	cfg := Default()
	cfg.Archive.COS.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "region and bucket") {
		t.Fatalf("Validate() error = %v", err)
	}
}
```

- [ ] **Step 2: 运行测试，确认因配置包不存在而失败**

Run: `cd modules/archive && go test -count=1 ./internal/config`

Expected: FAIL，错误包含 `no required module provides package` 或待定义的 `Default`/`Load` 标识符。

- [ ] **Step 3: 建立 module 依赖与配置类型**

`modules/archive/go.mod` 使用以下直接依赖；执行 `go mod tidy` 生成间接依赖：

```go
module github.com/mooyang-code/moox/modules/archive

go 1.24.0

require (
	github.com/cockroachdb/pebble v1.1.5
	github.com/mooyang-code/moox/modules/storage/proto/gen v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/commonpb v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/healthz v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/jetstream v0.0.0-00010101000000-000000000000
	github.com/mooyang-code/moox/packages/messagepb v0.0.0-00010101000000-000000000000
	github.com/nats-io/nats-server/v2 v2.11.3
	github.com/nats-io/nats.go v1.47.0
	github.com/parquet-go/parquet-go v0.25.1
	github.com/prometheus/client_golang v1.20.4
	github.com/tencentyun/cos-go-sdk-v5 v0.7.70
	google.golang.org/protobuf v1.36.11
	gopkg.in/yaml.v3 v3.0.1
	trpc.group/trpc-go/trpc-go v1.0.3
)

replace github.com/mooyang-code/moox/modules/storage/proto/gen => ../storage/proto/gen
replace github.com/mooyang-code/moox/packages/commonpb => ../../packages/commonpb
replace github.com/mooyang-code/moox/packages/healthz => ../../packages/healthz
replace github.com/mooyang-code/moox/packages/jetstream => ../../packages/jetstream
replace github.com/mooyang-code/moox/packages/messagepb => ../../packages/messagepb
```

`Config` 必须显式包含：

```go
type Config struct {
	Archive ArchiveConfig `yaml:"archive"`
	Health  HealthConfig  `yaml:"health"`
}

type ArchiveConfig struct {
	RootDir     string                  `yaml:"root_dir"`
	StateDir    string                  `yaml:"state_dir"`
	DeviceID    string                  `yaml:"device_id"`
	Sources     map[string]SourceConfig `yaml:"sources"`
	EventBus    EventBusConfig          `yaml:"eventbus"`
	Materialize MaterializeConfig       `yaml:"materialize"`
	StorageRPC  StorageRPCConfig        `yaml:"storage_rpc"`
	COS         COSConfig               `yaml:"cos"`
}

type EventBusConfig struct {
	URLs            []string      `yaml:"urls"`
	Stream          string        `yaml:"stream"`
	Subject         string        `yaml:"subject"`
	Durable         string        `yaml:"durable"`
	FetchBatch      int           `yaml:"fetch_batch"`
	FetchMaxWait    time.Duration `yaml:"fetch_max_wait"`
	AckWait         time.Duration `yaml:"ack_wait"`
	MaxAckPending   int           `yaml:"max_ack_pending"`
	DedupeRetention time.Duration `yaml:"dedupe_retention"`
}
```

`Validate` 必须拒绝：目录相同或互为父子、目录含符号链接、空 device ID、未知/重复 source、空 dataset、durable 含空白、worker 不在 `1..32`、非正 interval、row group 非正、COS 开启但 region/bucket/credential 缺失。COS 凭证来源只允许环境变量 `MOOX_ARCHIVE_COS_SECRET_ID`/`MOOX_ARCHIVE_COS_SECRET_KEY`，或权限不宽于 `0600` 的 `MOOX_ARCHIVE_COS_CREDENTIAL_FILE`。

- [ ] **Step 4: 写入生产默认配置**

```yaml
archive:
  root_dir: ../data/archive
  state_dir: ../data/archive-state
  device_id: parquet-local
  sources:
    stock_cn: {datasets: [equity_kline, etf_kline, index_kline]}
    stock_us: {datasets: [equity_kline, etf_kline, index_kline]}
    crypto_binance: {datasets: [spot_kline, swap_kline]}
    crypto_okx: {datasets: [spot_kline, swap_kline]}
  eventbus:
    urls: [nats://127.0.0.1:4222]
    stream: MOOX_STORAGE
    subject: moox.storage.time_series.rows_updated.v1
    durable: moox_archive_kline_v1
    fetch_batch: 128
    fetch_max_wait: 1s
    ack_wait: 5m
    max_ack_pending: 256
    dedupe_retention: 168h
  materialize:
    interval: 10m
    pending_rows: 10000
    workers: 2
    row_group_rows: 65536
    shutdown_timeout: 2m
  storage_rpc:
    access_target: ip://127.0.0.1:20102
    metadata_target: ip://127.0.0.1:20100
  cos:
    enabled: false
    region: ""
    bucket: ""
    prefix: moox/archive
    sync_interval: 1h
    sync_open_partitions: false
    workers: 2
health:
  addr: 127.0.0.1:11416
```

- [ ] **Step 5: 加入 workspace 并验证**

Run: `go work use ./modules/archive && cd modules/archive && go mod tidy && go test -count=1 ./internal/config`

Expected: PASS；`go.work` 出现 `./modules/archive`，`go.sum` 已生成。

- [ ] **Step 6: 提交**

```bash
git add go.work modules/archive/go.mod modules/archive/go.sum modules/archive/config/app.yaml modules/archive/internal/config
git commit -m "feat(archive): bootstrap module configuration"
```

### Task 2: 定义身份、路径与 Storage 类型转换

**Files:**
- Create: `modules/archive/internal/domain/identity.go`
- Create: `modules/archive/internal/domain/identity_test.go`
- Create: `modules/archive/internal/domain/row.go`
- Create: `modules/archive/internal/domain/row_test.go`

- [ ] **Step 1: 写文件身份与路径安全测试**

```go
func TestPartitionPathCarriesAllIdentityFields(t *testing.T) {
	key := PartitionKey{SpaceID: "crypto_binance", DatasetID: "spot_kline", SubjectID: "BTC-USDT", Freq: "1m", Month: "202606"}
	want := filepath.Join("crypto_binance", "spot_kline", "1m", "BTC-USDT", "crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet")
	got, err := key.RelativePath()
	if err != nil || got != want {
		t.Fatalf("RelativePath() = %q, %v; want %q", got, err, want)
	}
}

func TestIdentityEncodingIsReversibleAndUnambiguous(t *testing.T) {
	raw := "../BTC__USDT/季度"
	encoded := EncodeIdentity(raw)
	if strings.Contains(encoded, "/") || strings.Contains(encoded, "__") || encoded == ".." {
		t.Fatalf("unsafe encoded identity %q", encoded)
	}
	decoded, err := DecodeIdentity(encoded)
	if err != nil || decoded != raw {
		t.Fatalf("DecodeIdentity(%q) = %q, %v", encoded, decoded, err)
	}
}

func TestMonthUsesUTC(t *testing.T) {
	ts := time.Date(2026, 7, 1, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if got := MonthOf(ts); got != "202606" {
		t.Fatalf("MonthOf() = %s, want 202606", got)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/domain`

Expected: FAIL，`PartitionKey`、`EncodeIdentity` 或 `MonthOf` 尚未定义。

- [ ] **Step 3: 实现五字段文件名、解析和根目录边界检查**

实现以下 API：

```go
func EncodeIdentity(raw string) string
func DecodeIdentity(encoded string) (string, error)
func MonthOf(t time.Time) string
func (k PartitionKey) Validate() error
func (k PartitionKey) FileName() (string, error)
func (k PartitionKey) RelativePath() (string, error)
func (k PartitionKey) AbsolutePath(root string) (string, error)
func ParseFileName(name string) (PartitionKey, error)
func PartitionID(k PartitionKey) string
func LogicalRowID(dataTime time.Time, dimensionsJSON string) string
```

编码器逐个 UTF-8 byte 处理，只原样保留字母、数字、单个 `_`、`-` 和普通 `.`；其他 byte 使用大写 `%XX`。连续 `__` 中的下划线编码为 `%5F`，完整 segment 为 `.` 或 `..` 时把点编码为 `%2E`。`AbsolutePath` 在 `filepath.Join` 后使用 `filepath.Rel`，拒绝 `rel == ".."` 或以 `".."+separator` 开头。

- [ ] **Step 4: 写全部逻辑值转换和冲突测试**

```go
func TestScalarFromColumnPreservesZeroValues(t *testing.T) {
	zero := int64(0)
	falseValue := false
	cases := []*storagepb.ColumnValue{
		{ColumnName: "trade_num", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_IntValue{IntValue: zero}}},
		{ColumnName: "closed", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_BoolValue{BoolValue: falseValue}}},
	}
	for _, column := range cases {
		scalar, err := ScalarFromColumn(column)
		if err != nil || scalar.PointerCount() != 1 {
			t.Fatalf("ScalarFromColumn(%s) = %#v, %v", column.GetColumnName(), scalar, err)
		}
	}
}

func TestScalarFromColumnRejectsTypeBranchMismatch(t *testing.T) {
	column := &storagepb.ColumnValue{ColumnName: "close", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "1.25"}}}
	if _, err := ScalarFromColumn(column); err == nil || !strings.Contains(err.Error(), "type mismatch") {
		t.Fatalf("ScalarFromColumn() error = %v", err)
	}
}
```

- [ ] **Step 5: 实现 Canonical JSON、typed scalar 和 patch 合并**

实现以下 API，并拒绝 `UNSPECIFIED`、`LIST`、非法 TIME、非法 JSON、空列名和重复列名：

```go
func CanonicalStringMap(values map[string]string) (string, error)
func ScalarFromColumn(column *storagepb.ColumnValue) (Scalar, error)
func (s Scalar) PointerCount() int
func MergePatch(base ArchiveRow, patch RowPatch) ArchiveRow
func SortedColumnNames(schema map[string]storagepb.FieldValueType) []string
```

`MergePatch` 仅覆盖 patch 中出现的列；row attributes 也按 key 合并，未携带的旧 attribute 保留。`parquetio` 写文件时再把合并后的 attributes 转成 canonical `attributes_json`；`written_at` 采用最后应用 patch 的时间。

- [ ] **Step 6: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/domain`

Expected: PASS。

```bash
git add modules/archive/internal/domain
git commit -m "feat(archive): define partition and row contracts"
```

### Task 3: 建立可恢复的 Pebble journal

**Files:**
- Create: `modules/archive/internal/journal/keys.go`
- Create: `modules/archive/internal/journal/store.go`
- Create: `modules/archive/internal/journal/store_test.go`

- [ ] **Step 1: 写事件原子写入与重启恢复测试**

```go
func TestAppendEventIsAtomicAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	batch := fixtureBatch("message-1", twoPartitions())
	result, err := store.Append(context.Background(), batch)
	if err != nil || result.Seq != 1 || result.Duplicate {
		t.Fatalf("Append() = %#v, %v", result, err)
	}
	store.Close()

	store = openTestStore(t, dir)
	states, err := store.DirtyPartitions(context.Background(), 10)
	if err != nil || len(states) != 2 || states[0].HighWaterSeq != 1 || states[1].HighWaterSeq != 1 {
		t.Fatalf("DirtyPartitions() = %#v, %v", states, err)
	}
}
```

- [ ] **Step 2: 写高水位清理不删除新写入测试**

```go
func TestCompleteOnlyDeletesPendingThroughCapturedHighWater(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	key := fixturePartition()
	first, _ := store.Append(context.Background(), fixtureBatch("m1", []domain.PartitionKey{key}))
	attempt, _ := store.BeginMaterialization(context.Background(), key)
	second, _ := store.Append(context.Background(), fixtureBatch("m2", []domain.PartitionKey{key}))
	if first.Seq != attempt.ThroughSeq || second.Seq <= attempt.ThroughSeq {
		t.Fatalf("sequence order first=%d through=%d second=%d", first.Seq, attempt.ThroughSeq, second.Seq)
	}
	if err := store.Complete(context.Background(), key, attempt.ThroughSeq); err != nil {
		t.Fatal(err)
	}
	pending, _ := store.Pending(context.Background(), key, math.MaxUint64)
	if len(pending) != 1 || pending[0].Seq != second.Seq {
		t.Fatalf("pending after Complete = %#v", pending)
	}
}
```

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/journal`

Expected: FAIL，journal store 尚未实现。

- [ ] **Step 4: 实现 Store API 和版本校验**

```go
type Store struct {
	db *pebble.DB
	mu sync.Mutex
}

type PendingPatch struct {
	Seq   uint64
	RowID string
	Patch domain.RowPatch
}

type AppendResult struct {
	Seq        uint64
	Duplicate  bool
	Partitions []domain.PartitionKey
}

type MessageReceipt struct {
	Seq        uint64    `json:"seq"`
	ReceivedAt time.Time `json:"received_at"`
}

type MaterializationAttempt struct {
	Key        domain.PartitionKey
	Generation uint64
	ThroughSeq uint64
	StartedAt  time.Time
}

func Open(path string) (*Store, error)
func (s *Store) Append(ctx context.Context, event domain.EventBatch) (AppendResult, error)
func (s *Store) Quarantine(ctx context.Context, record QuarantineRecord) error
func (s *Store) DirtyPartitions(ctx context.Context, limit int) ([]PartitionState, error)
func (s *Store) Pending(ctx context.Context, key domain.PartitionKey, through uint64) ([]PendingPatch, error)
func (s *Store) BeginMaterialization(ctx context.Context, key domain.PartitionKey) (MaterializationAttempt, error)
func (s *Store) MarkLocalCommitted(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest) error
func (s *Store) MarkRegistered(ctx context.Context, key domain.PartitionKey) error
func (s *Store) Complete(ctx context.Context, key domain.PartitionKey, through uint64) error
func (s *Store) Status(ctx context.Context) (Status, error)
func (s *Store) PruneMessageReceipts(ctx context.Context, before time.Time) (uint64, error)
func (s *Store) Close() error
```

`Append` 在持有 `mu` 时先查 message receipt；命中时返回原 seq 和 `Duplicate=true`，不检查当前 schema，也不生成 pending。新消息读取并递增 `meta/next-seq`，先合并同一事件内相同逻辑 row 的 patch，再用一个 Pebble batch 写 receipt、全部 partition 和 schema；`Partitions` 返回本事件触达的稳定去重分区列表。任意编码失败必须在 commit 前返回，禁止出现半事件。`PruneMessageReceipts` 只清理早于 168 小时的幂等收据，不删除 pending、partition、manifest、quarantine 或 Parquet；配置校验要求 receipt 保留期不得短于 168 小时，长于 EventBus 当前 72 小时 retention。

增加 `TestAppendDuplicateMessageReturnsReceiptWithoutNewPending`：同一 `message_id` 连续 Append 两次，第二次 `Duplicate=true`、seq 不变、pending 数不变；关闭重开 Pebble 后第三次仍命中 receipt。

- [ ] **Step 5: 实现 quarantine 同步持久化和 schema 类型冲突**

```go
type QuarantineRecord struct {
	MessageID    string    `json:"message_id"`
	Subject      string    `json:"subject"`
	StreamSeq    uint64    `json:"stream_seq"`
	Delivery     uint64    `json:"delivery_count"`
	Reason       string    `json:"reason"`
	RawEnvelope  []byte    `json:"raw_envelope"`
	QuarantinedAt time.Time `json:"quarantined_at"`
}
```

当同一 Space/Dataset 的全局 schema 已登记同名列且类型不同，`Append` 返回可分类的 `ErrSchemaConflict`，不写 pending。新增列同时写入 `schema/` key 和受影响 partition state；Consumer 在 Task 5 将原始消息同步写 quarantine 后 `Term`。

- [ ] **Step 6: 加入故障注入测试**

通过 package 内可替换的 `commitBatch func(*pebble.Batch, *pebble.WriteOptions) error` 注入一次失败，断言 `Append` 返回错误、`next-seq` 未推进、两个 partition 均无 pending；恢复正常 commit 后再次写入得到连续序号。

- [ ] **Step 7: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/journal`

Expected: PASS，包含重启、高水位、schema 冲突和 commit 失败用例。

```bash
git add modules/archive/internal/journal
git commit -m "feat(archive): add durable pebble journal"
```

### Task 4: 解码并原子校验 Storage 行更新事件

**Files:**
- Create: `modules/archive/internal/consumer/decode.go`
- Create: `modules/archive/internal/consumer/decode_test.go`

- [ ] **Step 1: 写正常事件转换测试**

```go
func TestDecoderBuildsMonthlyPatches(t *testing.T) {
	decoder := NewDecoder(fixtureSources())
	event := fixtureRowsUpdated("2026-07-01T00:00:00Z", "2026-06-30T23:59:00Z")
	payload, _ := proto.Marshal(event)
	batch, decision, err := decoder.Decode(fixtureEnvelope(payload))
	if err != nil || decision != DecisionArchive || len(batch.Rows) != 1 {
		t.Fatalf("Decode() = %#v, %s, %v", batch, decision, err)
	}
	if batch.Rows[0].Partition.Month != "202606" || batch.Rows[0].Partition.SubjectID != "BTC-USDT" {
		t.Fatalf("unexpected partition: %#v", batch.Rows[0].Partition)
	}
}
```

- [ ] **Step 2: 写整事件失败和白名单忽略测试**

覆盖以下表格，每个 case 断言 batch 为空：

| 输入 | decision | error |
| --- | --- | --- |
| 未启用 Space/Dataset | `DecisionIgnore` | nil |
| envelope topic 不匹配 | `DecisionReject` | 包含 `topic` |
| event 与 row 的 space/dataset 不一致 | `DecisionReject` | 包含 `identity mismatch` |
| 空 subject/freq/data_time | `DecisionReject` | 包含缺失字段名 |
| 非法 RFC3339 时间 | `DecisionReject` | 包含 `data_time` |
| 重复列名 | `DecisionReject` | 包含 `duplicate column` |
| TypedValue 分支不匹配 | `DecisionReject` | 包含 `type mismatch` |
| 任意一行非法、其他行合法 | `DecisionReject` | batch 行数为 0 |

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/consumer -run Decoder`

Expected: FAIL，Decoder 尚未定义。

- [ ] **Step 4: 实现解码合同**

```go
type Decision uint8

const (
	DecisionArchive Decision = iota + 1
	DecisionIgnore
	DecisionReject
)

type Decoder struct {
	sources map[string]map[string]struct{}
}

func NewDecoder(sources map[string][]string) *Decoder
func (d *Decoder) Decode(message *messagepb.MooxMessage) (domain.EventBatch, Decision, error)
```

`Decode` 校验 outer `protocol_version=1`、topic、kind、content type、outer/inner message ID，并使用 `proto.Unmarshal` 解码 `storagepb.TimeSeriesRowsUpdated`。所有时间归一化为 UTC；dimensions 使用 `domain.CanonicalStringMap`，row attributes 克隆为 patch map 并按 key 合并。事件内相同逻辑 key 的多行按出现顺序合并为一个 patch。`freq` 保留 Storage canonical key 的原值，不把 `1H` 静默改写成 `1h`。

- [ ] **Step 5: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/consumer -run Decoder`

Expected: PASS。

```bash
git add modules/archive/internal/consumer/decode.go modules/archive/internal/consumer/decode_test.go
git commit -m "feat(archive): validate storage update events"
```

### Task 5: 接入 JetStream durable consumer 与 ACK/NAK/Term 语义

**Files:**
- Create: `modules/archive/internal/consumer/handler.go`
- Create: `modules/archive/internal/consumer/handler_test.go`
- Create: `modules/archive/internal/consumer/runner.go`
- Modify: `packages/jetstream/consumer.go`
- Modify: `packages/jetstream/consumer_test.go`

- [ ] **Step 1: 写 delivery disposition 测试**

```go
func TestHandlerAcknowledgesOnlyAfterJournalSync(t *testing.T) {
	order := make([]string, 0, 2)
	store := &fakeJournal{appendFn: func(domain.EventBatch) (journal.AppendResult, error) {
		order = append(order, "sync")
		return journal.AppendResult{Seq: 1}, nil
	}}
	delivery := &fakeDelivery{ackFn: func() error {
		order = append(order, "ack")
		return nil
	}}
	handler := NewHandler(fixtureDecoder(), store, &fakeNotifier{})
	if err := handler.Handle(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"sync", "ack"}) {
		t.Fatalf("order = %v", order)
	}
}
```

同时测试：journal IO 失败调用 `Nak(delay)`、不 ACK 并返回 `ErrRetryScheduled`；无效事件先 quarantine Sync 再 `Term`；quarantine 失败改为 NAK；白名单忽略直接 ACK；outer decode error 使用 `RawData` quarantine 后 Term；receipt 命中的 duplicate 不通知 scheduler 且直接 ACK。

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/consumer -run Handler`

Expected: FAIL，Handler 尚未定义。

- [ ] **Step 3: 定义可测试 delivery 与 journal 边界**

```go
type Delivery interface {
	Envelope() *messagepb.MooxMessage
	RawEnvelope() []byte
	MessageID() string
	Subject() string
	StreamSequence() uint64
	DeliveryCount() uint64
	DecodeError() error
	Ack(context.Context) error
	Nak(context.Context, time.Duration) error
	InProgress(context.Context) error
	Term(context.Context) error
}

type Journal interface {
	Append(context.Context, domain.EventBatch) (journal.AppendResult, error)
	Quarantine(context.Context, journal.QuarantineRecord) error
}

type DirtyNotifier interface {
	Notify([]domain.PartitionKey)
}

type RetryScheduledError struct {
	Delay time.Duration
}

func (e *RetryScheduledError) Error() string {
	return fmt.Sprintf("archive delivery retry scheduled after %s", e.Delay)
}

func NewHandler(decoder *Decoder, journal Journal, notifier DirtyNotifier) *Handler
func sleepContext(ctx context.Context, delay time.Duration) error
```

为 `*jetstream.Delivery` 增加 archive 包内 adapter；公共包只增加下一段所述的 bind 参数透传。`Handle` 对临时错误使用封顶 30 秒的指数 NAK delay；处理超过 `AckWait/3` 时每隔该时长调用 `InProgress`。

`ConsumerRef` 当前不能把 poison delivery 交给 bind-only consumer。先在公共包增加并透传：

```go
type ConsumerRef struct {
	Stream              string
	Durable             string
	FilterSubject       string
	AckWait             time.Duration
	MaxDeliver          int
	MaxAckPending       int
	FetchMaxWait        time.Duration
	DeliverPolicy       nats.DeliverPolicy
	DeliverDecodeErrors bool
}
```

`BindPullConsumer` 构造 `ConsumerConfig` 时复制 `DeliverDecodeErrors`。公共包测试创建 malformed envelope，断言该选项为 true 时 `Fetch` 同时返回带 `DecodeError/RawData` 的 delivery 和 `ErrDecode`，且消息未被 transport 自动 Term。

- [ ] **Step 4: 实现 pull runner**

```go
type PullConsumer interface {
	Fetch(context.Context, int) ([]*jetstream.Delivery, error)
	Close() error
}

func (r *Runner) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		deliveries, err := r.consumer.Fetch(ctx, r.batch)
		if err != nil && len(deliveries) == 0 {
			if errors.Is(err, nats.ErrTimeout) {
				continue
			}
			return fmt.Errorf("fetch archive deliveries: %w", err)
		}
		retryBatch := false
		for i, delivery := range deliveries {
			if handleErr := r.handler.Handle(ctx, adaptDelivery(delivery)); handleErr != nil {
				var retry *RetryScheduledError
				if errors.As(handleErr, &retry) {
					for _, remaining := range deliveries[i+1:] {
						_ = adaptDelivery(remaining).Nak(ctx, retry.Delay)
					}
					if err := sleepContext(ctx, retry.Delay); err != nil {
						return err
					}
					retryBatch = true
					break
				}
				return handleErr
			}
		}
		if retryBatch {
			continue
		}
		if err != nil && !errors.Is(err, jetstream.ErrDecode) {
			return fmt.Errorf("fetch archive deliveries: %w", err)
		}
	}
	return ctx.Err()
}
```

Runner 停止时先停止 fetch，再等待已进入 Handler 的消息完成 disposition。并发上限不超过 `max_ack_pending`，初版 Handler worker 数固定为 1，避免单实例下过度增加磁盘 Sync 并发。

Handler 在非 duplicate Append 成功后调用 `DirtyNotifier.Notify(result.Partitions)`。当 Handler 返回 `ErrRetryScheduled` 时，Runner 对当前 fetch 中尚未处理的 delivery 使用相同 delay 执行 NAK，暂停 fetch 到 delay 到期后再继续；不得越过一次 journal 失败继续处理同 batch 的新事件。AckSync 失败直接返回错误，已持久化 receipt 保证进程或连接恢复后的重投只 ACK、不重复应用 patch。

- [ ] **Step 5: 验证并提交**

Run: `cd packages/jetstream && go test -count=1 ./... && cd ../../modules/archive && go test -count=1 ./internal/consumer`

Expected: PASS。

```bash
git add modules/archive/internal/consumer packages/jetstream/consumer.go packages/jetstream/consumer_test.go
git commit -m "feat(archive): consume storage updates from jetstream"
```

### Task 6: 实现动态宽表 Parquet codec

**Files:**
- Create: `modules/archive/internal/parquetio/schema.go`
- Create: `modules/archive/internal/parquetio/codec.go`
- Create: `modules/archive/internal/parquetio/codec_test.go`

- [ ] **Step 1: 写 schema 与 round-trip 测试**

```go
func TestWideParquetRoundTripPreservesTypesAndRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet")
	rows := fixtureRowsWithAllScalarTypes()
	manifest, err := Write(path, rows, WriteOptions{Generation: 7, MaterializedAt: fixedTime, RowGroupRows: 65536})
	if err != nil {
		t.Fatal(err)
	}
	got, metadata, err := Read(path)
	if err != nil || !reflect.DeepEqual(got, rows) {
		t.Fatalf("Read() = %#v, %#v, %v", got, metadata, err)
	}
	if manifest.RowCount != uint64(len(rows)) || metadata["moox.archive.schema_version"] != "1" || metadata["moox.archive.generation"] != "7" {
		t.Fatalf("manifest=%#v metadata=%v", manifest, metadata)
	}
}
```

- [ ] **Step 2: 写可选新列、类型冲突、排序与重复键测试**

断言新列为 optional、旧行值为 nil；同名列类型变化返回 `ErrSchemaConflict`；输出严格按 `candle_begin_time ASC, dimensions_json ASC`；重复逻辑键只保留最后 patch 合并后的单行。

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/parquetio`

Expected: FAIL，`Write` 和 `Read` 尚未定义。

- [ ] **Step 4: 使用 parquet.Group 构造动态 schema**

```go
func BuildSchema(columns map[string]storagepb.FieldValueType) (*parquet.Schema, error) {
	group := parquet.Group{
		"candle_begin_time": parquet.Timestamp(parquet.Nanosecond),
		"space_id":          parquet.String(),
		"dataset_id":        parquet.String(),
		"subject_id":        parquet.String(),
		"freq":              parquet.String(),
		"dimensions_json":   parquet.String(),
		"attributes_json":   parquet.String(),
		"written_at":        parquet.Timestamp(parquet.Nanosecond),
	}
	for _, name := range domain.SortedColumnNames(columns) {
		node, err := businessNode(columns[name])
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", name, err)
		}
		group[name] = parquet.Optional(node)
	}
	return parquet.NewSchema("moox_archive_v1", group), nil
}
```

`businessNode` 映射：STRING/JSON 为 `parquet.String()`，INT 为 `parquet.Int(64)`，DOUBLE 为 `parquet.Leaf(parquet.DoubleType)`，BOOL 为 `parquet.Leaf(parquet.BooleanType)`，TIME 为 `parquet.Timestamp(parquet.Nanosecond)`，BYTES 为 `parquet.Leaf(parquet.ByteArrayType)`。字符串列采用 RLE dictionary，writer 默认 ZSTD。

- [ ] **Step 5: 实现 writer、reader 和 manifest**

```go
type WriteOptions struct {
	Generation     uint64
	MaterializedAt time.Time
	RowGroupRows   int64
}

func Write(path string, rows []domain.ArchiveRow, opts WriteOptions) (domain.Manifest, error)
func Read(path string) ([]domain.ArchiveRow, map[string]string, error)
func Validate(path string, expected domain.PartitionKey, generation uint64) (domain.Manifest, error)
```

writer 使用 `parquet.NewWriter(file, schema, parquet.Compression(&zstd.Codec{}), parquet.MaxRowsPerRowGroup(opts.RowGroupRows))` 和 `schema.Deconstruct(nil, map[string]any)`；metadata 至少包含 schema version、generation、materialized_at、space、dataset、subject、freq、month。`Validate` 重新打开文件，核对 footer、metadata、行数、排序、唯一键、身份列、min/max 和 SHA-256。

- [ ] **Step 6: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/parquetio`

Expected: PASS。

```bash
git add modules/archive/internal/parquetio
git commit -m "feat(archive): add wide parquet codec"
```

### Task 7: 实现 Archive Writer 的 copy-on-write、部分更新与崩溃恢复

**Files:**
- Create: `modules/archive/internal/writer/writer.go`
- Create: `modules/archive/internal/writer/writer_test.go`
- Create: `modules/archive/internal/writer/recovery.go`
- Create: `modules/archive/internal/writer/recovery_test.go`
- Create: `modules/archive/internal/writer/scheduler.go`
- Create: `modules/archive/internal/writer/scheduler_test.go`
- Modify: `modules/archive/internal/consumer/handler.go`
- Modify: `modules/archive/internal/consumer/handler_test.go`

- [ ] **Step 1: 写既有文件加 pending patch 的合并测试**

测试初始文件有 `open/high/low/close`，pending 只修订 `close` 并增加 `trade_num`；物化后旧 OHLC 保留、close 更新、trade_num 出现、行数不增加。再加入乱序跨月 row，断言只改写其所属月份。

- [ ] **Step 2: 写原子提交和高水位竞态测试**

在 writer 完成临时文件但 rename 前注入新 journal patch。断言本 generation 完成后新 patch 仍存在，partition phase 为 dirty；目标目录没有遗留 `.tmp-` 文件，目标文件可被 `parquetio.Validate` 打开。

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/writer`

Expected: FAIL，Writer 尚未定义。

- [ ] **Step 4: 实现单 partition 物化算法**

```go
func (w *Writer) WritePartition(ctx context.Context, key domain.PartitionKey) (domain.Manifest, error) {
	attempt, err := w.journal.BeginMaterialization(ctx, key)
	if err != nil {
		return domain.Manifest{}, err
	}
	base, err := w.readExisting(key)
	if err != nil {
		return domain.Manifest{}, err
	}
	pending, err := w.journal.Pending(ctx, key, attempt.ThroughSeq)
	if err != nil {
		return domain.Manifest{}, err
	}
	rows, err := mergeRows(base, pending)
	if err != nil {
		return domain.Manifest{}, err
	}
	manifest, err := w.atomicWrite(ctx, key, attempt, rows)
	if err != nil {
		return domain.Manifest{}, err
	}
	if err := w.journal.MarkLocalCommitted(ctx, key, manifest); err != nil {
		return domain.Manifest{}, err
	}
	return manifest, nil
}
```

`mergeRows` 以逻辑 row ID 建 map，按 pending seq 升序应用 patch，再按时间和 dimensions 排序。`atomicWrite` 在目标目录创建 `.<filename>.tmp-<generation>`，写入后依次执行 file Sync、Close、`parquetio.Validate`、`os.Rename`、父目录 Sync。

- [ ] **Step 5: 实现 partition lock、有界 worker 和全部触发条件**

`WriteDirty(ctx, limit)` 从 journal 取 dirty partition，以 `workers` 个 goroutine 处理；同一 partition 的 timer、CLI、recovery 共用 `sync.Map` 中的 mutex。错误保留 phase/pending 并记录 last error，不删除目标旧文件。

`Scheduler` 接收 Consumer AppendResult 的 partition 通知；pending 数达到阈值立即入队，否则由 interval ticker 扫描。UTC month 改变时调用 `SealClosedMonths`：对上月 clean manifest 直接更新 state/ArchiveFile 为 sealed 并加入 COS 队列，对 dirty partition 先完成物化再 sealed。迟到 patch 到达 sealed partition 时保持 `Sealed=true`、设置 dirty、清除 COS synced generation，重新物化后仍为 sealed。`Flush(ctx)` 用于 SIGTERM，在 timeout 内处理当前全部 dirty partition。

```go
type Scheduler struct {
	journal      Journal
	writer       *Writer
	interval     time.Duration
	pendingRows  uint64
	now          func() time.Time
}

func (s *Scheduler) Notify(partitions []domain.PartitionKey)
func (s *Scheduler) Run(ctx context.Context) error
func (s *Scheduler) SealClosedMonths(ctx context.Context, currentMonth string) error
func (s *Scheduler) Flush(ctx context.Context) error
```

测试覆盖 `10,000` 行阈值、10 分钟 interval、UTC 月切换、优雅停止和 sealed 月迟到修订五个触发路径。

- [ ] **Step 6: 实现五种崩溃点恢复测试和代码**

故障点固定为：写临时文件前、file Sync 后、rename 后、journal local commit 后、Metadata register 后。每个故障点都关闭并重开 journal，调用 `Recover` 两次；断言结果幂等、目标文件恰好一份、行数不重复、pending 最终只清理到 captured high water。

```go
func (w *Writer) Recover(ctx context.Context) error {
	states, err := w.journal.IncompleteMaterializations(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if err := w.recoverPartition(ctx, state); err != nil {
			return fmt.Errorf("recover %s: %w", domain.PartitionID(state.Key), err)
		}
	}
	return w.removeUnownedTempFiles(ctx)
}
```

- [ ] **Step 7: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/writer`

Expected: PASS。

```bash
git add modules/archive/internal/writer
git commit -m "feat(archive): write atomic parquet generations"
```

### Task 8: 登记 Storage ArchiveFile 元数据

**Files:**
- Create: `modules/archive/internal/registry/client.go`
- Create: `modules/archive/internal/registry/client_test.go`
- Modify: `modules/archive/internal/writer/writer.go`
- Modify: `modules/archive/internal/writer/writer_test.go`

- [ ] **Step 1: 写稳定 ID 和字段映射测试**

```go
func TestArchiveFileUsesStableIdentity(t *testing.T) {
	key := fixturePartition()
	manifest := fixtureManifest()
	first := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	manifest.Generation++
	second := BuildArchiveFile("parquet-local", key, manifest, false, domain.COSState{})
	if first.GetArchiveFileId() != second.GetArchiveFileId() {
		t.Fatalf("stable id changed: %s != %s", first.GetArchiveFileId(), second.GetArchiveFileId())
	}
	if first.GetPartitionKey() != "1m/BTC-USDT/202606" || first.GetFileFormat() != "parquet" {
		t.Fatalf("unexpected ArchiveFile: %#v", first)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/registry`

Expected: FAIL，registry client 尚未定义。

- [ ] **Step 3: 实现 tRPC client 与字段合同**

```go
type MetadataClient interface {
	RegisterArchiveFile(context.Context, *storagepb.RegisterArchiveFileReq, ...client.Option) (*storagepb.RegisterArchiveFileRsp, error)
}

func StableArchiveFileID(key domain.PartitionKey) string
func BuildArchiveFile(deviceID string, key domain.PartitionKey, manifest domain.Manifest, sealed bool, cos domain.COSState) *storagepb.ArchiveFile
func (c *Client) Register(ctx context.Context, file *storagepb.ArchiveFile) error
```

stable ID 输入严格为 `space + "\n" + dataset + "\n" + freq + "\n" + subject + "\n" + month`。`file_uri` 使用绝对 `file://` URL；attributes 写 schema_version、generation、materialized_at、COS generation/status/object key。Metadata 返回空 `ret_info` 或非 SUCCESS 都是错误。

- [ ] **Step 4: 接入物化状态机而不重复写文件**

`local_committed` partition 只调用 Registry；成功后 `MarkRegistered` 和 `Complete(throughSeq)`。Registry 暂时失败时保留 local manifest 和 pending，不重写 Parquet；下一轮直接重试登记。

- [ ] **Step 5: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/registry ./internal/writer`

Expected: PASS，registry 重试测试断言 Parquet writer 只调用一次。

```bash
git add modules/archive/internal/registry modules/archive/internal/writer
git commit -m "feat(archive): register materialized archive files"
```

### Task 9: 实现 Storage 回填与维护 CLI

**Files:**
- Create: `modules/archive/internal/backfill/backfill.go`
- Create: `modules/archive/internal/backfill/backfill_test.go`
- Create: `modules/archive/cmd/cli/main.go`
- Create: `modules/archive/cmd/cli/main_test.go`

- [ ] **Step 1: 写回填范围规划测试**

构造 Metadata 返回两个 subjects、Dataset 返回 `freqs=[1m,1h]`，请求限定一个 subject 和一个 freq；断言 plan 只有该组合、按 UTC 月计算分区数。请求未带 `--confirm` 时 stdout 输出 JSON plan 且不调用 Access。

- [ ] **Step 2: 写分页读取和 journal 复用测试**

fake Access 连续返回两页 `ReadTimeSeriesRowsRsp`；断言每页 rows 先转换为与 NATS 相同的 `EventBatch` 再调用 `journal.Append`，message ID 格式为 `backfill/<space>/<dataset>/<subject>/<freq>/<page>/<range-hash>`，最终调用 writer。

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/backfill ./cmd/cli`

Expected: FAIL，backfill 和 CLI 尚未定义。

- [ ] **Step 4: 实现 Storage client 和 Backfill API**

```go
type AccessClient interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
}

type BackfillRequest struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	Freq      string
	Start     time.Time
	End       time.Time
	Confirm   bool
}

func (b *Backfiller) Plan(ctx context.Context, req BackfillRequest) (BackfillPlan, error)
func (b *Backfiller) Run(ctx context.Context, plan BackfillPlan) (BackfillResult, error)
```

Metadata 和 Access 分页大小固定 500，按 `PageResult.HasMore` 推进 page。Read 请求使用单个完整 key、闭区间 time range、ASC。服务返回的任意 row 仍经过 Task 4 Decoder 的行校验函数，禁止回填绕过 schema/path 检查。

- [ ] **Step 5: 实现五个 CLI command**

```text
moox-archive-cli backfill --config config/app.yaml --space <space-id> --dataset <dataset-id> [--subject <subject-id>] [--freq <freq>] [--start <rfc3339>] [--end <rfc3339>] [--confirm]
moox-archive-cli compact --config config/app.yaml [--space <space-id>] [--dataset <dataset-id>] [--subject <subject-id>] [--freq <freq>] [--month YYYYMM]
moox-archive-cli verify --config config/app.yaml [same filters] [--metadata]
moox-archive-cli sync-cos --config config/app.yaml [same filters]
moox-archive-cli status --config config/app.yaml
```

使用标准库 `flag.FlagSet`，stdout 每行一个 JSON 结果，stderr 输出结构化错误并返回非零退出码。`backfill` 缺少 `--confirm` 时只输出 plan，不写 journal；start 必须小于等于 end。所有修改型命令遇到 Pebble lock 错误时提示 `archive server must be stopped for this command`。

- [ ] **Step 6: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/backfill ./cmd/cli`

Expected: PASS。

```bash
git add modules/archive/internal/backfill modules/archive/cmd/cli
git commit -m "feat(archive): add backfill and maintenance cli"
```

### Task 10: 实现可选 COS generation 副本

**Files:**
- Create: `modules/archive/internal/cosstore/client.go`
- Create: `modules/archive/internal/cosstore/client_test.go`
- Modify: `modules/archive/internal/journal/store.go`
- Modify: `modules/archive/internal/journal/store_test.go`

- [ ] **Step 1: 写 object key、header 与 HEAD 校验测试**

```go
func TestSyncUsesRelativeArchivePathAndVerifiesMetadata(t *testing.T) {
	manifest := fixtureManifest()
	fake := &fakeObjectClient{head: ObjectHead{Size: manifest.Size, SHA256: manifest.SHA256}}
	syncer := NewSyncer(fake, "moox/archive")
	if err := syncer.Sync(context.Background(), fixturePartition(), manifest); err != nil {
		t.Fatal(err)
	}
	wantSuffix := "crypto_binance/spot_kline/1m/BTC-USDT/crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet"
	if !strings.HasSuffix(fake.putKey, wantSuffix) || fake.putMetadata["sha256"] != manifest.SHA256 {
		t.Fatalf("put key=%q metadata=%v", fake.putKey, fake.putMetadata)
	}
}
```

另测 HEAD size 不同、SHA 不同、上传 generation 后本地已有新 generation、未封口且 `sync_open_partitions=false`。

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/cosstore`

Expected: FAIL，COS syncer 尚未定义。

- [ ] **Step 3: 实现 COS adapter 和凭证加载**

```go
type ObjectClient interface {
	Put(context.Context, string, io.Reader, int64, map[string]string) error
	Head(context.Context, string) (ObjectHead, error)
}

type ObjectHead struct {
	Size          int64
	SHA256        string
	RowCount      uint64
	SchemaVersion string
}

func LoadCredentialsFromEnvOrFile() (Credentials, error)
func ObjectKey(prefix string, key domain.PartitionKey) (string, error)
func (s *Syncer) Sync(ctx context.Context, key domain.PartitionKey, manifest domain.Manifest) error
```

打开本地文件后保留 file descriptor 作为 generation 快照，上传 metadata `x-cos-meta-sha256`、`x-cos-meta-row-count`、`x-cos-meta-schema-version`。HEAD 只认 size 和自定义 SHA-256，不使用 ETag。成功后再次读取 journal current manifest；只有 generation/hash 未变化才 `MarkCOSSynced`，否则保持 dirty。

- [ ] **Step 4: 实现后台重试状态**

COS state 记录 status、generation、object key、last attempt、last error、next retry。指数退避从 1 分钟开始，封顶 1 小时；COS 失败不改变 archive readiness，也不回滚本地文件。

- [ ] **Step 5: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/cosstore ./internal/journal`

Expected: PASS。

```bash
git add modules/archive/internal/cosstore modules/archive/internal/journal
git commit -m "feat(archive): add optional cos replication"
```

### Task 11: 组装 Server 生命周期、健康检查与指标

**Files:**
- Create: `modules/archive/internal/health/state.go`
- Create: `modules/archive/internal/health/server.go`
- Create: `modules/archive/internal/health/server_test.go`
- Create: `modules/archive/internal/bootstrap/app.go`
- Create: `modules/archive/internal/bootstrap/app_test.go`
- Create: `modules/archive/cmd/server/main.go`

- [ ] **Step 1: 写启动顺序和 readiness 测试**

fake 依赖记录调用顺序，断言严格为 `journal.open -> writer.recover -> writer.drain -> jetstream.connect -> consumer.bind -> workers.start -> ready`。任一前置步骤失败时 ready=false 且后续步骤不启动。

- [ ] **Step 2: 写 shutdown 顺序测试**

取消 context 后断言 `consumer.stop -> inflight.wait -> writer.flush -> cos.stop -> jetstream.close -> journal.close`。物化超过 shutdown timeout 时允许退出，但 journal close 前 pending 必须仍可重启读取。

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd modules/archive && go test -count=1 ./internal/bootstrap ./internal/health`

Expected: FAIL，App 和 health server 尚未定义。

- [ ] **Step 4: 实现健康快照和 Prometheus mux**

```go
func (s *State) Snapshot(ctx context.Context) healthz.Response {
	rsp := healthz.Base("archive", s.instanceID, s.version, s.gitCommit, s.startedAt, s.Ready())
	rsp.Details = map[string]any{
		"nats_ready":                s.natsReady.Load(),
		"journal_ready":             s.journalReady.Load(),
		"dirty_partitions":          s.dirtyPartitions.Load(),
		"pending_rows":              s.pendingRows.Load(),
		"oldest_pending_age_seconds": s.oldestPendingAge.Load(),
		"last_materialized_at":       s.LastMaterializedAt(),
		"cos_enabled":                s.cosEnabled,
		"cos_pending_files":          s.cosPending.Load(),
	}
	return rsp
}
```

HTTP mux 固定暴露 `/healthz` 和 `/metrics`。指标名以 `moox_archive_` 开头，至少包含 deliveries、ack、nak、redelivery、quarantine、pending rows、dirty partitions、materialization seconds/rows/bytes/failures、archive lag、COS pending/upload bytes/verification failures、磁盘 available bytes 和按当前增长率估算的 disk exhaustion seconds。

本目录 package 名为 `health`。引用公共健康协议时固定使用 `healthz "github.com/mooyang-code/moox/packages/healthz"`，bootstrap 引用本目录时使用 `archivehealth` alias，避免两个 health 名称混淆。

- [ ] **Step 5: 实现 App.Run、内部维护循环与 main**

main 接受 `-config config/app.yaml`，使用 `signal.NotifyContext` 处理 SIGINT/SIGTERM，ldflags 字段固定为 `Version`、`BuildTime`、`GitCommit`。App 使用 `jetstream.ConfigFromEnv` 连接，并调用 `BindPullConsumer`，绝不由 archive 自行创建 durable。内部维护循环每天调用一次 `PruneMessageReceipts(now-dedupeRetention)`，每个 COS interval 重试 dirty generation，并按 materialize interval 更新 pending/lag/磁盘指标；这些清理只作用于幂等 receipt 和临时文件，不得删除归档逻辑行或 Parquet。

- [ ] **Step 6: 验证并提交**

Run: `cd modules/archive && go test -count=1 ./internal/bootstrap ./internal/health ./cmd/server`

Expected: PASS，health test 在未 ready 时返回 503，ready 后返回 200。

```bash
git add modules/archive/internal/bootstrap modules/archive/internal/health modules/archive/cmd/server
git commit -m "feat(archive): add service lifecycle and health endpoints"
```

### Task 12: 声明 EventBus durable consumer

**Files:**
- Modify: `modules/eventbus/config/app.yaml:94`
- Modify: `modules/eventbus/internal/config/config.go:153`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/eventbus/internal/registry/registry_test.go`

- [ ] **Step 1: 写默认配置包含 archive durable 的测试**

```go
func TestDefaultIncludesArchiveConsumer(t *testing.T) {
	cfg := Default()
	for _, consumer := range cfg.Consumers {
		if consumer.Stream == "MOOX_STORAGE" && consumer.Durable == "moox_archive_kline_v1" {
			if consumer.FilterSubject != "moox.storage.time_series.rows_updated.v1" || consumer.DeliverPolicy != "all" || consumer.AckWait != 5*time.Minute || consumer.MaxDeliver != -1 {
				t.Fatalf("archive consumer = %#v", consumer)
			}
			return
		}
	}
	t.Fatal("archive durable consumer missing")
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd modules/eventbus && go test -count=1 ./internal/config ./internal/registry`

Expected: FAIL，默认 consumer 列表缺少 `moox_archive_kline_v1`。

- [ ] **Step 3: 同时修改 Default 与 app.yaml**

```yaml
  - stream: MOOX_STORAGE
    durable: moox_archive_kline_v1
    filter_subject: moox.storage.time_series.rows_updated.v1
    ack_policy: explicit
    deliver_policy: all
    replay_policy: instant
    ack_wait: 5m
    max_ack_pending: 256
    max_deliver: -1
```

registry test 断言实际 JetStream consumer 与以上声明一致。Archive 的 `BindPullConsumer` 参数必须逐项相同，防止配置漂移导致启动时静默绑定错误 consumer。

- [ ] **Step 4: 验证并提交**

Run: `cd modules/eventbus && go test -count=1 ./internal/config ./internal/registry`

Expected: PASS。

```bash
git add modules/eventbus/config/app.yaml modules/eventbus/internal/config modules/eventbus/internal/registry/registry_test.go
git commit -m "feat(eventbus): declare archive durable consumer"
```

### Task 13: 增加端到端、故障恢复与基准测试

**Files:**
- Create: `modules/archive/test/archive_e2e_test.go`
- Create: `modules/archive/test/archive_fault_test.go`
- Create: `modules/archive/test/benchmark_test.go`

- [ ] **Step 1: 写嵌入式 JetStream 到 Parquet E2E**

测试启动临时 nats-server JetStream，创建 `MOOX_STORAGE` stream 和 archive durable，启动 Archive App，发布两条包含重复、部分更新、乱序和迟到 row 的真实 MooX envelope。等待 health pending=0 后断言：

```text
crypto_binance/spot_kline/1m/BTC-USDT/
  crypto_binance__spot_kline__BTC-USDT__1m__202606.parquet
  crypto_binance__spot_kline__BTC-USDT__1m__202607.parquet
```

文件中每根 K 线一行，重复时间不增行，修订列正确，文件名与系统列一致。

- [ ] **Step 2: 写 ACK 前后崩溃测试**

场景 A 在 Pebble Sync 前杀掉 App，重启后 JetStream 重投且最终归档一行；场景 B 在 Pebble Sync 后、AckSync 前杀掉 App，重启后重投但最终仍只有一行。测试检查 consumer redelivery count 与 Parquet row count。

- [ ] **Step 3: 写文件提交故障测试**

注入短写、Sync 失败、校验失败、rename 失败、Metadata 失败和 COS 失败；每种错误都断言旧 Parquet 仍可读、journal pending 未丢、NATS 已持久化事件不因下游失败回退、恢复后能收敛。

- [ ] **Step 4: 写永久保留测试**

生成已封口 `202606` 文件和当月文件，运行 compact、verify、COS sync 与 App restart，断言两个文件都存在且 inode 内容 hash 不因 verify/COS 被修改。测试代码中禁止调用任何删除 `.parquet` 的 API。

- [ ] **Step 5: 增加两个 benchmark**

```go
func BenchmarkJournalAppend100Rows(b *testing.B)
func BenchmarkMaterializeOneMonth1m(b *testing.B)
```

第二个 benchmark 固定 43,200 根 1m K 线、12 个业务列，输出 `rows/s`、`bytes_written/op` 和 materialization wall time。基准仅记录性能，不使用机器相关硬阈值作为单元测试成败条件。

- [ ] **Step 6: 运行完整 archive 测试与 race**

Run: `cd modules/archive && go test -count=1 ./...`

Expected: PASS。

Run: `cd modules/archive && go test -race -count=1 ./internal/... ./test/...`

Expected: PASS，无 data race。

Run: `cd modules/archive && go test -run '^$' -bench 'Benchmark(Journal|Materialize)' -benchmem ./test`

Expected: PASS，并输出两个 benchmark 的 ns/op 与 allocs/op。

- [ ] **Step 7: 提交**

```bash
git add modules/archive/test
git commit -m "test(archive): cover end-to-end recovery paths"
```

### Task 14: 接入构建、发布、部署和系统服务清单

**Files:**
- Modify: `scripts/build.sh`
- Modify: `scripts/release.sh`
- Modify: `scripts/deploy-moox.sh`
- Modify: `modules/admin/internal/service/sysdeploy/defaults.go`
- Modify: `modules/admin/internal/service/sysdeploy/defaults_test.go`

- [ ] **Step 1: 先写脚本与服务清单断言**

在 sysdeploy test 中断言存在：

```go
archive := byName["moox_archive"]
if archive.Protocol != "http" || archive.Port != 11416 || healthURL(archive.ExtraConfig) != "http://127.0.0.1:11416/healthz" {
	t.Fatalf("moox_archive deployment = %#v", archive)
}
```

新增 shell smoke test 或现有脚本测试，断言 `scripts/build.sh archive` 生成 `bin/moox-archive` 与 `bin/moox-archive-cli`，deploy stage 包含 `archive/config/app.yaml`。

- [ ] **Step 2: 修改 build/release**

`scripts/build.sh` 的 `all` 和新增 `archive)` case 都执行：

```bash
build_go modules/archive ./cmd/server moox-archive 0
build_go modules/archive ./cmd/cli moox-archive-cli 0
```

`scripts/release.sh` 创建 `archive/bin`、`archive/config`，复制两个 binary 与 `modules/archive/config/.`。

- [ ] **Step 3: 完整接入 deploy 开关和运行脚本**

在 `scripts/deploy-moox.sh` 所有与 factor/monitor 同层的分支加入 `WITH_ARCHIVE=1` 和 `--no-archive`，覆盖 build、stage mkdir、binary/config copy、start、stop、restart、status、healthcheck、rsync exclude、远端旧 binary 清理、变量替换和 SSH 环境传递。启动命令固定为：

```bash
start_service "archive" "${ROOT}/archive" \
  env "${ARCHIVE_ENV[@]}" "${ROOT}/bin/moox-archive" -config config/app.yaml
```

Archive 在 EventBus 和 Storage Access/Metadata ready 后启动；健康检查访问 `http://127.0.0.1:11416/healthz` 并要求 `"ready":true`。

- [ ] **Step 4: 保护永久归档不被 reset 或 redeploy 删除**

把远端 `rm -rf "${DEPLOY_DIR}/data"` 改为只清理非归档子目录：

```bash
if [[ "${RESET_DATA}" == "1" && -d "${DEPLOY_DIR}/data" ]]; then
  find "${DEPLOY_DIR}/data" -mindepth 1 -maxdepth 1 \
    ! -name archive ! -name archive-state -exec rm -rf {} +
fi
```

停止、禁用或升级 archive 只删除 binary/config/run/log，不删除 `data/archive` 和 `data/archive-state`。部署测试先写入 sentinel Parquet 与 journal file，执行 local deploy `--reset-data --no-start` 后断言两者 hash 不变。

- [ ] **Step 5: 增加 sysdeploy 默认项**

```go
withExtra(
	deployment("moox_archive", "archive", "http", "127.0.0.1", 11416, "", "internal", "行情 Parquet 永久归档与可选 COS 副本"),
	`{"health_url":"http://127.0.0.1:11416/healthz","monitor_enabled":true}`,
),
```

- [ ] **Step 6: 构建和 stage 验证**

Run: `./scripts/build.sh archive`

Expected: PASS，生成两个纯 Go binary。

Run: `TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh archive`

Expected: PASS，`file bin/moox-archive` 显示 Linux x86-64。

Run: `./scripts/deploy-moox.sh --target localhost --dir /tmp/moox-archive-plan-check --stage /tmp/moox-archive-stage --skip-build --no-start --no-web-host --no-cloudnode --no-collector --no-factor --no-monitor`

Expected: PASS，stage 和目标目录包含 archive binary/config，归档 sentinel 未删除。

- [ ] **Step 7: 提交**

```bash
git add scripts/build.sh scripts/release.sh scripts/deploy-moox.sh modules/admin/internal/service/sysdeploy
git commit -m "build(archive): package and deploy archive service"
```

### Task 15: 移除 Storage 旧归档入口，确保只能运行新模块

**Files:**
- Delete: `modules/storage/internal/services/archive/metadata.go`
- Delete: `modules/storage/internal/services/archive/service.go`
- Delete: `modules/storage/internal/services/archive/events.go`
- Delete: `modules/storage/internal/services/archive/remote_metadata.go`
- Delete: `modules/storage/internal/services/archive/schedule.go`
- Delete: `modules/storage/internal/service/archive/parquet/archive.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/internal/services/access/options.go`
- Modify: `modules/storage/internal/services/access/service.go`
- Modify: `modules/storage/config/storage*.yaml`
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/storage/README.md`

- [ ] **Step 1: 先把旧 archive role 改成非法配置并写失败测试**

```go
func TestValidateRejectsLegacyArchiveRole(t *testing.T) {
	cfg := RuntimeConfig{Storage: StorageConfig{Roles: []string{"archive"}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "archive role moved to modules/archive") {
		t.Fatalf("Validate() error = %v", err)
	}
}
```

- [ ] **Step 2: 删除 main 中旧 runtime 和 timer wiring**

删除 archive import、`archiveRuntime`、`registerArchiveRole`、`metadataForArchiveRuntime`、`archiveRootForRuntime`、`registerNoopArchiveTimers`、`shouldStartArchiveRole` 以及对 archive role 的 deployment validation。Storage main 不再启动、消费或调度任何 Parquet 归档。

- [ ] **Step 3: 删除 timer service 和只为旧实现存在的路径配置**

从 `trpc_go.yaml` 删除 `trpc.moox.storage.archive.timer`。删除 `StorageDevices.ParquetPath`、defaults/rebase、`storagesvc.Options.ParquetPath` 和 `Service.parquetPath`；从所有 `storage*.yaml` 删除 `parquet_path`。保留 Storage `ArchiveFile` protobuf、Metadata RPC 和 SQLite UPSERT。

- [ ] **Step 4: 更新 parquet device seed**

保留 `device_id: parquet-local` 供新模块登记 ArchiveFile，把 endpoint 更新为部署约定的 `../data/archive`，名称改为 `market-parquet-archive`；不得删除既有 ArchiveFile 表或元数据接口。

- [ ] **Step 5: 删除旧 packages 并更新 README**

README 明确：Storage 只负责提交后发布 `moox.storage.time_series.rows_updated.v1`；归档由 `moox-archive` durable consumer 完成。删除旧 role/timer/long-form archive 的说明，增加新模块入口与路径示例。

- [ ] **Step 6: 验证旧入口完全消失**

Run: `rg -n 'storage\.archive\.timer|registerArchiveRole|internal/services/archive|internal/infra/device/parquet|HasRole\("archive"\)|parquet_path' modules/storage`

Expected: 无输出。`ArchiveFile`、`RegisterArchiveFile`、`ListArchiveFiles` 和 `parquet-local` 仍可被搜索到。

Run: `cd modules/storage && go test -count=1 ./...`

Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add -A modules/storage
git commit -m "refactor(storage): remove legacy archive runtime"
```

### Task 16: 补齐运维手册与单 symbol Python 读取示例

**Files:**
- Create: `docs/行情数据归档运维手册.md`
- Create: `examples/archive/read_symbol.py`
- Modify: `docs/行情数据归档模块设计.md`

- [ ] **Step 1: 编写运维手册的固定章节**

章节必须包含：配置、启动顺序、首次 backfill、日常 status/verify、手工 compact、COS 重试、磁盘告警、故障恢复、停机执行 CLI、永久保留约束、从任意原子文件名恢复身份、升级与回滚。

首次建档命令固定示例：

```bash
./bin/moox-archive-cli backfill \
  --config archive/config/app.yaml \
  --space crypto_binance \
  --dataset spot_kline \
  --freq 1m \
  --subject BTC-USDT \
  --start 2025-01-01T00:00:00Z \
  --end 2026-07-01T00:00:00Z

./bin/moox-archive-cli backfill \
  --config archive/config/app.yaml \
  --space crypto_binance \
  --dataset spot_kline \
  --freq 1m \
  --subject BTC-USDT \
  --start 2025-01-01T00:00:00Z \
  --end 2026-07-01T00:00:00Z \
  --confirm
```

第一条只输出 plan，第二条执行。

- [ ] **Step 2: 添加按单个 symbol 和时间段读取的 Python 示例**

```python
from __future__ import annotations

import argparse
from datetime import datetime
from pathlib import Path

import pyarrow as pa
import pyarrow.dataset as ds
import pyarrow.parquet as pq


def read_symbol(root: Path, space: str, dataset: str, freq: str, subject: str, start: str, end: str):
    subject_dir = root / space / dataset / freq / subject
    files = sorted(subject_dir.glob(f"{space}__{dataset}__{subject}__{freq}__*.parquet"))
    if not files:
        raise FileNotFoundError(subject_dir)
    schema = pa.unify_schemas([pq.read_schema(path) for path in files])
    start_time = datetime.fromisoformat(start.replace("Z", "+00:00"))
    end_time = datetime.fromisoformat(end.replace("Z", "+00:00"))
    table = ds.dataset(files, format="parquet", schema=schema).to_table(
        filter=(ds.field("candle_begin_time") >= pa.scalar(start_time, type=pa.timestamp("ns", tz="UTC")))
        & (ds.field("candle_begin_time") <= pa.scalar(end_time, type=pa.timestamp("ns", tz="UTC")))
    )
    return table.sort_by([("candle_begin_time", "ascending"), ("dimensions_json", "ascending")])


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--space", required=True)
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--freq", required=True)
    parser.add_argument("--subject", required=True)
    parser.add_argument("--start", required=True)
    parser.add_argument("--end", required=True)
    args = parser.parse_args()
    print(read_symbol(args.root, args.space, args.dataset, args.freq, args.subject, args.start, args.end).to_pandas())


if __name__ == "__main__":
    main()
```

示例只读一个 subject 目录中的月份文件，不扫描全 archive root。

- [ ] **Step 3: 更新设计文档实现状态**

把设计文档“文档状态”补充为：实现入口、archive health port、CLI 需要独占 state、journal 高水位规则、部署 reset 永久保留规则。文件命名仍保持五字段 `__` 格式。

- [ ] **Step 4: 验证文档命令和示例**

Run: `python3 -m py_compile examples/archive/read_symbol.py`

Expected: PASS。

若本机已安装 pyarrow，使用 E2E 生成文件运行示例并断言返回时间范围内的 BTC-USDT 行；pyarrow 未安装时，`py_compile` 仍是必跑检查，互操作读取由 Parquet E2E 和人工验收完成。

- [ ] **Step 5: 提交**

```bash
git add docs/行情数据归档模块设计.md docs/行情数据归档运维手册.md examples/archive/read_symbol.py
git commit -m "docs(archive): add operations and query guide"
```

## 5. 最终验收

- [ ] **Step 1: 全工作区新鲜测试**

```bash
cd modules/archive && go test -count=1 ./...
cd ../eventbus && go test -count=1 ./...
cd ../storage && go test -count=1 ./...
cd ../admin && go test -count=1 ./internal/service/sysdeploy
```

Expected: 全部 PASS。

- [ ] **Step 2: 静态与格式检查**

```bash
gofmt -w modules/archive modules/eventbus/internal/config modules/admin/internal/service/sysdeploy modules/storage
go vet ./modules/archive/...
git diff --check
```

Expected: 无输出或全部成功。

- [ ] **Step 3: 构建验证**

```bash
./scripts/build.sh archive
TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh archive
```

Expected: `moox-archive` 和 `moox-archive-cli` 的本机及 Linux 构建均成功。

- [ ] **Step 4: 本地真实链路验收**

1. 启动 EventBus、Storage 与 Archive。
2. 写入 BTC-USDT 的闭合 `1m` 与 `1h` K 线，包含重复、乱序、部分修订和上月迟到行。
3. 等待 `/healthz` 的 pending rows 归零。
4. 核对 subject 目录下直接出现全身份月份文件。
5. 运行 `moox-archive-cli verify`；若 Server 正在运行，先按手册停止 Archive。
6. 重启 Archive 并再次 verify，确认 hash、行数和唯一键不变。

Expected: 无丢行、无重复 K 线、文件可独立识别、迟到月份被重物化、本地文件未删除。

- [ ] **Step 5: 首次上线顺序**

```text
停止旧 Storage archive role（若环境曾启用）
-> 部署包含新 durable 声明的 EventBus
-> 确认 moox_archive_kline_v1 已创建且 DeliverAll
-> 停止 Archive Server，执行历史 backfill + compact + verify
-> 启动 Archive Server，消费 durable 中与 backfill 重叠的增量
-> 等待 backlog 清零并再次 verify
-> 开启 COS 时先小范围 sync-cos，再开启后台 worker
```

回填和 JetStream 增量允许时间范围重叠，最终按逻辑键幂等合并。不得为了“重新开始”删除 archive root 或 state dir。

- [ ] **Step 6: 最终提交与推送**

```bash
git status --short
git log --oneline --decorate -16
git push origin HEAD
```

Expected: 工作区只保留执行前就存在的无关改动；本计划的所有提交已推送到远端分支。

## 6. 完成定义

只有同时满足以下条件才算完成：

1. Archive ACK 严格晚于 journal Sync，坏消息先 quarantine 再 Term。
2. 每根 K 线在 Parquet 中一行，类型无损，部分更新、重复、乱序和迟到修订正确。
3. 文件位于 subject 目录直接下级，文件名携带 Space、Dataset、Subject、freq、month 五项身份。
4. 任一崩溃点恢复后逻辑行不丢、不重复，物化期间的新 seq 不被旧 generation 清理。
5. Metadata 重试不重写已提交文件，COS 失败不影响本地归档与 NATS ACK。
6. `backfill`、`compact`、`verify`、`sync-cos`、`status` 可用，修改型 CLI 明确执行独占要求。
7. 部署、reset、禁用和升级均不会删除本地 archive 与 archive-state。
8. Storage 旧 archive role、timer 和 long-form Parquet writer 已移除，ArchiveFile Metadata 契约保留。
9. archive、eventbus、storage、admin 测试和 Linux 构建通过，真实 BTC-USDT `1m/1h` 链路验收通过。
