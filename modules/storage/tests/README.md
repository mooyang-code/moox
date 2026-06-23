# storage 端到端测试（E2E）

本目录提供 storage 模块的**端到端测试**：在本地把 storage 的全部子服务真实部署起来
（独立进程 / 端口 / 目录），再以 HTTP/tRPC 客户端依次驱动各功能接口。测试数据使用本机下载目录下的
`AR-USDT.csv`（加密货币 K 线）。

> 与 `internal/.../*_test.go` 的进程内单测不同：这里跑的是**真实编译出的 `moox-storage` 二进制**，
> 走完整的网络链路，覆盖服务部署、计时器调度、异步索引/物化/归档等真实运行时行为。

## 覆盖范围

| 子测试 | 模块 / 接口 | 说明 |
| --- | --- | --- |
| `01_metadata_crud` | `MetadataService` | Space/DataSource/Subject/SubjectSymbol/Dataset/Field 的创建与查询 |
| `02_seed_route_and_columns` | `MetadataService` | Dataset 列契约、PrimaryStoreNode/Device、PrimaryStoreRoute、归档设备 |
| `03_write_klines` | `AccessService.WriteTimeSeriesRows` | 从 CSV 载入 K 线分批写入（含等待元数据缓存生效） |
| `04_read_range` | `AccessService.ReadTimeSeriesRows` | 区间读，校验行数、时间升序、列值 |
| `05_read_latest_before` | `AccessService.ReadTimeSeriesRows` | 用 `TimeRange.end_time + DESC + limit 1` 表达截面最新读 |
| `06_read_range_pagination` | `AccessService.ReadTimeSeriesRows` | 游标分页，校验无遗漏无重复地覆盖全量 |
| `07_read_column_projection` | `AccessService.ReadTimeSeriesRows` | 列裁剪，只返回指定列 |
| `08_query_time_series_rows` | `MetadataService.CreateView` + `ViewService.QueryTimeSeriesRows` | TimeSeries + DuckDB 视图登记 + 计时器物化 + 查询 |
| `09_cli_storage_import_csv` | `moox-cli storage import` + HTTP `MetadataService`/`AccessService.WriteTimeSeriesRows` | CLI 读取 CSV、校验元数据、写入并回读主存 |
| `10_record_read` | `AccessService.WriteRecordRows` / `ReadRecordRows` | 记录型数据集按 `record_id` 读取 |
| `11_search_record_rows` | `ViewService.SearchRecordRows` | Record + Bleve 全文检索（等待异步索引构建） |
| `12_rebuild_record_view` | `ViewService.RebuildRecordView` | 异步重建 Record 派生索引，返回 rebuild_id 后仍可命中 |
| `13_upsert_column_merge` | `AccessService.WriteRecordRows` | Record 列级合并写入：只更新携带列，其余保留 |
| `14_archive` | `archive.timer` + `MetadataService.ListArchiveFiles` | 计时器归档为 Parquet 并登记 |
| `15_write_validation_errors` | `AccessService` | 未登记列 / 缺 subject_id / 无路由 的错误码 |
| `16_not_found_errors` | `MetadataService` / `ViewService` | Space / Dataset / View 不存在的错误码 |
| `17_direct_storage_counts` | SQLite / Pebble / DuckDB / Bleve | 停止服务后直接打开底层存储文件，校验元数据表、主存事实行、View 结果表、搜索索引文档数 |

`PrimaryStoreService` 作为在线主存随进程一起部署，由 `AccessService` 的写读链路间接覆盖；
`view.timer` / `archive.timer` 在部署期即按更短周期（5s / 20s）启用，使物化与归档在测试中能快速完成。
E2E 生成的 `storage.yaml` 显式启用 `roles: [access, primary, deriver]`，使用 `eventbus.type: memory`，并把 `deriver.access_service_name` 置空，让 deriver 通过同进程 Access reader 回读事实行。内存 eventbus 仍是异步的，所以搜索和视图断言都通过轮询等待派生结果。

## 运行

```bash
cd modules/storage
make e2e           # 推荐
./tests/run_e2e.sh # 等价脚本
```

或直接用 go test：

```bash
cd modules/storage
CGO_ENABLED=1 go test -tags e2e -timeout 600s -v ./tests/e2e/...
```

> 测试文件带有 `//go:build e2e` 构建标签，普通 `go test ./...` / `go build ./...` 不会触发，
> 必须显式加 `-tags e2e`。
> `TestStorageE2E` 内的子测试按顺序依赖前置元数据和写入结果，调试单个后置场景时需同时匹配其前置子测试。

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `MOOX_E2E_KLINE_CSV` | `~/Downloads/AR-USDT.csv` | K 线测试文件路径 |
| `MOOX_E2E_KLINE_LIMIT` | `500` | 最多载入行数，`0` 表示全部（约 3.2 万行） |

若测试文件不存在，`go test` 会**跳过**（skip）而非失败。

## 运行机制

1. `tests/e2e/harness.go` 用 `CGO_ENABLED=1 go build` 编译 `moox-storage`（DuckDB 视图存储依赖 cgo）。
2. 在临时目录生成隔离的 `trpc_go.yaml` 与 `storage.yaml`（独立端口 + 独立设备目录 + 快速计时器 + 单进程 memory eventbus）。
3. 执行 `moox-storage -init-metadata` 初始化 SQLite 元数据 schema。
4. 启动服务并轮询端口直到就绪。
5. 测试以 HTTP/tRPC 客户端串行驱动各接口；测试结束后优雅停止进程并清理临时目录。

> 元数据读缓存（snapshotcache）默认 10s 刷新一次，因此写路径在路由/列契约可见前会被拒绝；
> 测试用 `retry()` 轮询等待，属预期行为。

## 手动部署（排障用）

`tests/testdata/trpc_go.e2e.yaml` 与 `tests/testdata/storage.e2e.yaml` 是一组等价的手动部署参考配置（使用 `./var/e2e` 相对目录）。其中 storage 配置显式启用 `access`、`primary`、`deriver`，使用 memory eventbus，并把 deriver 配为同进程本地 Access reader：

```bash
cd modules/storage
CGO_ENABLED=1 go build -o bin/moox-storage ./cmd/moox-storage
./bin/moox-storage -conf=tests/testdata/trpc_go.e2e.yaml -storage-conf=tests/testdata/storage.e2e.yaml -init-metadata
./bin/moox-storage -conf=tests/testdata/trpc_go.e2e.yaml -storage-conf=tests/testdata/storage.e2e.yaml
# 服务端口：metadata_http=29101 data_http=29104 primary=28101 query=28202 admin=29000
```
