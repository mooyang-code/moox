//go:build e2e

// Package e2e 是 storage 模块的端到端测试：
// 它在本地把 storage 的全部子服务真实部署起来（独立进程 / 端口 / 目录），
// 然后以 HTTP/tRPC 客户端依次驱动 Metadata / Data / Query / Archive 各功能接口，
// 测试数据使用本机下载目录下的 AR-USDT.csv（K 线）。
//
// 运行：
//
//	cd modules/storage && go test -tags e2e -timeout 600s ./tests/e2e/...
//
// 可选环境变量：
//
//	MOOX_E2E_KLINE_CSV    指定 K 线 CSV 路径（默认 ~/Downloads/AR-USDT.csv）
//	MOOX_E2E_KLINE_LIMIT  最多载入多少行（默认 500，<=0 表示全部）
package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

// 测试用元数据常量。space 必须与 archive.timer 配置里的 space_id 一致。
const (
	dataSourceID = "binance"
	subjectID    = "AR-USDT"
	datasetID    = "binance_spot_kline_1h"
	freq         = "1h"
	viewID       = "ar_usdt_close_view"
	cliSubjectID = "CLI-IMPORT-USDT"
	cliDatasetID = "binance_cli_import_kline_1h"

	// symbolsDatasetID 是用于记录读写（按 record_id/version）的记录型数据集。
	symbolsDatasetID = "binance_symbols"
	symbolsViewID    = "binance_symbols_view"
)

// harness 是整个 e2e 套件共享的部署环境，由 TestMain 管理生命周期。
var harness *Harness
var symbolRecordKeys map[string]*pb.RecordKey

func TestMain(m *testing.M) {
	h, err := NewHarness()
	if err != nil {
		fmt.Fprintf(os.Stderr, "创建 e2e harness 失败: %v\n", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	if err := h.Start(ctx); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "启动 e2e 服务失败: %v\n", err)
		os.Exit(1)
	}
	cancel()
	harness = h

	code := m.Run()

	_ = h.Stop()
	os.Exit(code)
}

// klineFixture 缓存载入的 K 线，避免每个子测试重复读盘。
var klineFixture []Kline

func klines(t *testing.T) []Kline {
	t.Helper()
	if klineFixture != nil {
		return klineFixture
	}
	path := klineCSVPath()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("K 线测试文件不存在，跳过: %s（可用 MOOX_E2E_KLINE_CSV 指定）", path)
	}
	loaded, err := loadKlines(path, klineRowLimit())
	require.NoError(t, err, "载入 K 线 CSV 失败")
	require.NotEmpty(t, loaded)
	klineFixture = loaded
	return loaded
}

// TestStorageE2E 按依赖顺序串起各模块的端到端验证。
// 子测试之间存在数据依赖（先建元数据 → 写 → 读 / 搜索 / 视图 / 归档），
// 因此使用有序 t.Run，任一前置失败则后续直接失败更便于定位。
func TestStorageE2E(t *testing.T) {
	ctx := context.Background()

	t.Run("01_metadata_crud", func(t *testing.T) { testMetadataCRUD(ctx, t) })
	t.Run("02_seed_route_and_columns", func(t *testing.T) { testSeedRouteAndColumns(ctx, t) })
	t.Run("03_write_klines", func(t *testing.T) { testWriteKlines(ctx, t) })
	t.Run("04_read_range", func(t *testing.T) { testReadRange(ctx, t) })
	t.Run("05_read_latest_before", func(t *testing.T) { testReadLatestBefore(ctx, t) })
	t.Run("06_read_range_pagination", func(t *testing.T) { testReadRangePagination(ctx, t) })
	t.Run("07_read_column_projection", func(t *testing.T) { testReadColumnProjection(ctx, t) })
	t.Run("08_query_time_series_rows", func(t *testing.T) { testQueryTimeSeriesRows(ctx, t) })
	t.Run("09_view_version_rebuild_after_column_add", func(t *testing.T) { testViewVersionRebuildAfterColumnAdd(ctx, t) })
	t.Run("10_rebuild_time_series_view_write_during_cutover", func(t *testing.T) { testRebuildTimeSeriesViewWriteDuringCutover(ctx, t) })
	t.Run("11_cli_storage_import_csv", func(t *testing.T) { testCLIStorageImportCSV(ctx, t) })
	t.Run("12_record_read", func(t *testing.T) { testRecordRead(ctx, t) })
	t.Run("13_rebuild_record_view", func(t *testing.T) { testRebuildRecordView(ctx, t) })
	t.Run("14_search_record_rows", func(t *testing.T) { testSearchRecordRows(ctx, t) })
	t.Run("15_upsert_column_merge", func(t *testing.T) { testUpsertColumnMerge(ctx, t) })
	t.Run("16_archive", func(t *testing.T) { testArchive(ctx, t) })
	t.Run("17_write_validation_errors", func(t *testing.T) { testWriteValidationErrors(ctx, t) })
	t.Run("18_not_found_errors", func(t *testing.T) { testNotFoundErrors(ctx, t) })
	t.Run("19_direct_storage_counts", func(t *testing.T) { testDirectStorageCounts(ctx, t) })
}

// ---------- 01 MetadataService CRUD ----------

func testMetadataCRUD(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()

	// Space
	_, err := meta.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{SpaceId: e2eSpaceID, Name: "Crypto E2E", Status: "active"}})
	require.NoError(t, err)
	getSpace, err := meta.GetSpace(ctx, &pb.GetSpaceReq{SpaceId: e2eSpaceID})
	require.NoError(t, err)
	requireSuccess(t, getSpace.GetRetInfo())
	require.Equal(t, e2eSpaceID, getSpace.GetSpace().GetSpaceId())

	// DataSource
	mustSuccess(t, "CreateDataSource", func() *pb.RetInfo {
		rsp, err := meta.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: e2eSpaceID, DataSourceId: dataSourceID, Name: "Binance", Kind: "exchange", Market: "crypto", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// Subject + Symbol
	mustSuccess(t, "UpsertSubject", func() *pb.RetInfo {
		rsp, err := meta.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: e2eSpaceID, SubjectId: subjectID, SubjectType: "crypto_pair", Name: subjectID, Market: "crypto", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "UpsertSubjectSymbol", func() *pb.RetInfo {
		rsp, err := meta.UpsertSubjectSymbol(ctx, &pb.UpsertSubjectSymbolReq{SubjectSymbol: &pb.SubjectSymbol{SpaceId: e2eSpaceID, SubjectId: subjectID, DataSourceId: dataSourceID, ExternalSymbol: "ARUSDT", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// Dataset + bind subject
	mustSuccess(t, "CreateDataset", func() *pb.RetInfo {
		rsp, err := meta.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
			SpaceId:      e2eSpaceID,
			DatasetId:    datasetID,
			DataSourceId: dataSourceID,
			Name:         "Binance 现货 K 线 1h",
			DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs:        []string{freq},
			Status:       "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "BindDatasetSubject", func() *pb.RetInfo {
		rsp, err := meta.BindDatasetSubject(ctx, &pb.BindDatasetSubjectReq{DatasetSubject: &pb.DatasetSubject{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// 字段字典：K 线各列的来源字段。
	for _, f := range klineFields() {
		f := f
		mustSuccess(t, "CreateField:"+f.id, func() *pb.RetInfo {
			rsp, err := meta.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{SpaceId: e2eSpaceID, FieldId: f.id, Name: f.name, ValueType: f.valueType, Status: "active"}})
			require.NoError(t, err)
			return rsp.GetRetInfo()
		})
	}

	// 列出验证。
	listDS, err := meta.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: e2eSpaceID})
	require.NoError(t, err)
	requireSuccess(t, listDS.GetRetInfo())
	require.Len(t, listDS.GetDatasets(), 1)

	listFields, err := meta.ListFields(ctx, &pb.ListFieldsReq{SpaceId: e2eSpaceID})
	require.NoError(t, err)
	requireSuccess(t, listFields.GetRetInfo())
	require.GreaterOrEqual(t, len(listFields.GetFields()), len(klineFields()))
}

// ---------- 02 列契约 + 路由 + 设备 ----------

func testSeedRouteAndColumns(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()

	// Dataset 列契约。
	for _, c := range klineColumns() {
		c := c
		mustSuccess(t, "UpsertDatasetColumn:"+c.name, func() *pb.RetInfo {
			rsp, err := meta.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
				SpaceId:    e2eSpaceID,
				DatasetId:  datasetID,
				ColumnName: c.name,
				OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
				OriginId:   c.name,
				ValueType:  c.valueType,
				Status:     "active",
			}})
			require.NoError(t, err)
			return rsp.GetRetInfo()
		})
	}

	// 在线主存路由：PrimaryStoreNode + Pebble Device + PrimaryStoreRoute。
	mustSuccess(t, "CreatePrimaryStoreNode:pebble", func() *pb.RetInfo {
		rsp, err := meta.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "node_pebble", Name: "node_pebble", Endpoint: "local", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreateDevice:pebble", func() *pb.RetInfo {
		rsp, err := meta.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "device_pebble", NodeId: "node_pebble", Name: "pebble", Engine: "pebble", Endpoint: "local", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreatePrimaryStoreRoute", func() *pb.RetInfo {
		rsp, err := meta.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectPattern: "*", NodeId: "node_pebble", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// 归档设备：archive.timer 通过 parquet_archive Device 选择落地设备。
	mustSuccess(t, "CreatePrimaryStoreNode:archive", func() *pb.RetInfo {
		rsp, err := meta.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{Node: &pb.PrimaryStoreNode{NodeId: "node_archive", Name: "node_archive", Endpoint: "local", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreateDevice:archive", func() *pb.RetInfo {
		rsp, err := meta.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{DeviceId: "device_archive", NodeId: "node_archive", Name: "parquet_archive", Engine: "parquet_archive", Endpoint: "local", Status: "active"}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
}

// ---------- 03 写入 K 线 ----------

func testWriteKlines(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	rows := klines(t)

	// 元数据缓存默认 10s 刷新一次，路由/列契约对写路径可见前会被拒绝，
	// 因此先用一行探针轮询直到写入成功。
	probe := klineTimeSeriesRow(e2eSpaceID, datasetID, subjectID, freq, rows[0])
	retry(t, 25*time.Second, time.Second, "首行写入（等待元数据缓存生效）", func() error {
		rsp, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{probe}})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("写入未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		return nil
	})

	// 分批写入剩余 K 线。
	const batchSize = 200
	dataRows := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, k := range rows {
		dataRows = append(dataRows, klineTimeSeriesRow(e2eSpaceID, datasetID, subjectID, freq, k))
	}
	for start := 0; start < len(dataRows); start += batchSize {
		end := start + batchSize
		if end > len(dataRows) {
			end = len(dataRows)
		}
		rsp, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: dataRows[start:end]})
		require.NoError(t, err)
		requireSuccess(t, rsp.GetRetInfo())
	}
	t.Logf("已写入 %d 行 K 线", len(dataRows))
}

// ---------- 04 区间读 ----------

func testReadRange(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	rows := klines(t)

	rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
		Page: &pb.Page{Page: 1, Size: uint32(len(rows) + 100)},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.Len(t, rsp.GetRows(), len(rows), "区间读应返回全部已写入行")

	// 校验时间升序。
	var prev string
	for _, r := range rsp.GetRows() {
		cur := r.GetKey().GetDataTime()
		if prev != "" {
			require.LessOrEqual(t, prev, cur, "结果应按 data_time 升序")
		}
		prev = cur
	}

	// 抽样校验首行 close 值与 CSV 一致。
	first := rsp.GetRows()[0]
	require.InDelta(t, rows[0].Close, columnDouble(first, "close"), 1e-9)
}

// ---------- 05 截面最新读 ----------

func testReadLatestBefore(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	rows := klines(t)
	last := rows[len(rows)-1]
	snapshot := last.Time.Add(time.Hour).UTC().Format(time.RFC3339)

	rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:      []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
		TimeRange: &pb.TimeRange{EndTime: snapshot},
		Order:     pb.SortOrder_SORT_ORDER_DESC,
		Page:      &pb.Page{Size: 1},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.Len(t, rsp.GetRows(), 1, "截面最新读应只返回一行")
	require.Equal(t, last.Time.UTC().Format("2006-01-02T15:04:05.000000000Z"), rsp.GetRows()[0].GetKey().GetDataTime())
}

// ---------- 06 全文 + 结构化搜索 ----------

func testSearchRecordRows(ctx context.Context, t *testing.T) {
	query := harness.QueryClient()

	// 搜索索引异步构建，轮询直到命中。
	retry(t, 30*time.Second, time.Second, "全文搜索命中（等待索引构建）", func() error {
		rsp, err := query.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
			SpaceId:   e2eSpaceID,
			ViewId:    symbolsViewID,
			TextQuery: "AR",
		})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("搜索未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rsp.GetRows()) == 0 {
			return fmt.Errorf("搜索结果为空")
		}
		return nil
	})
}

// ---------- 07 视图查询 ----------

func testQueryTimeSeriesRows(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()
	query := harness.QueryClient()

	// 创建视图及其暴露列（close）。query_window 设大以覆盖历史数据。
	mustSuccess(t, "CreateView", func() *pb.RetInfo {
		rsp, err := meta.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId:          e2eSpaceID,
			ViewId:           viewID,
			Name:             "AR-USDT 收盘价视图",
			PrimaryDatasetId: datasetID,
			DatasetIds:       []string{datasetID},
			QueryWindow:      "4000d",
			Status:           "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "UpsertViewColumn", func() *pb.RetInfo {
		rsp, err := meta.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
			SpaceId:    e2eSpaceID,
			ViewId:     viewID,
			ColumnName: "close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   datasetID + ".close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// 等待 view.timer 物化 + 元数据缓存暴露 active_result。
	retry(t, 45*time.Second, 2*time.Second, "视图查询返回数据（等待物化构建）", func() error {
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId: e2eSpaceID,
			ViewId:  viewID,
			Keys:    []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
		})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("查询未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rsp.GetRows()) == 0 {
			return fmt.Errorf("视图结果为空")
		}
		return nil
	})
}

func testViewVersionRebuildAfterColumnAdd(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()
	query := harness.QueryClient()

	before := getViewOrFail(ctx, t, viewID)
	require.GreaterOrEqual(t, before.GetViewVersion(), uint64(2), "CreateView + close 列应至少推进到 v2")
	beforeVersion := before.GetViewVersion()

	mustSuccess(t, "UpsertViewColumn:volume", func() *pb.RetInfo {
		rsp, err := meta.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
			SpaceId:    e2eSpaceID,
			ViewId:     viewID,
			ColumnName: "volume",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   datasetID + ".volume",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	var targetVersion uint64
	retry(t, 20*time.Second, time.Second, "View 新增列后版本递增", func() error {
		view := getViewOrFail(ctx, t, viewID)
		targetVersion = view.GetViewVersion()
		if targetVersion != beforeVersion+1 {
			return fmt.Errorf("view_version=%d, want=%d", targetVersion, beforeVersion+1)
		}
		return nil
	})

	retry(t, 30*time.Second, time.Second, "手动重建 TimeSeries View 被接受", func() error {
		rsp, err := query.RebuildTimeSeriesView(ctx, &pb.RebuildTimeSeriesViewReq{
			SpaceId: e2eSpaceID,
			ViewId:  viewID,
		})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("重建未成功受理: %s", rsp.GetRetInfo().GetMsg())
		}
		if rsp.GetRebuildId() == "" {
			return fmt.Errorf("rebuild_id 为空")
		}
		return nil
	})

	retry(t, 45*time.Second, time.Second, "新增列重建后切换到新 active 版本", func() error {
		view := getViewOrFail(ctx, t, viewID)
		if view.GetActiveViewVersion() != targetVersion {
			return fmt.Errorf("active_view_version=%d, want=%d status=%s", view.GetActiveViewVersion(), targetVersion, view.GetBuildStatus())
		}
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId:     e2eSpaceID,
			ViewId:      viewID,
			Keys:        []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
			ColumnNames: []string{"volume"},
			Page:        &pb.Page{Size: 5},
		})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("查询未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rsp.GetRows()) == 0 {
			return fmt.Errorf("新增列视图结果为空")
		}
		if got := columnNames(rsp.GetRows()[0]); len(got) != 1 || got[0] != "volume" {
			return fmt.Errorf("新增列查询结果列=%v, want [volume]", got)
		}
		return nil
	})
}

func testRebuildTimeSeriesViewWriteDuringCutover(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	query := harness.QueryClient()

	rebuild, err := query.RebuildTimeSeriesView(ctx, &pb.RebuildTimeSeriesViewReq{
		SpaceId: e2eSpaceID,
		ViewId:  viewID,
	})
	require.NoError(t, err)
	requireSuccess(t, rebuild.GetRetInfo())
	require.NotEmpty(t, rebuild.GetRebuildId(), "异步重建应返回 rebuild_id")

	dataTime := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339Nano)
	row := &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   e2eSpaceID,
			DatasetId: datasetID,
			SubjectId: subjectID,
			Freq:      freq,
			DataTime:  dataTime,
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 12345.67),
			testutil.DoubleValue("volume", 76543.21),
		},
	}
	write, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{row}})
	require.NoError(t, err)
	requireSuccess(t, write.GetRetInfo())

	retry(t, 45*time.Second, time.Second, "重建窗口写入的 K 线在 active View 可见", func() error {
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId: e2eSpaceID,
			ViewId:  viewID,
			Keys: []*pb.TimeSeriesKey{{
				SpaceId:   e2eSpaceID,
				DatasetId: datasetID,
				SubjectId: subjectID,
				Freq:      freq,
				DataTime:  dataTime,
			}},
			ColumnNames: []string{"close", "volume"},
		})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("查询未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rsp.GetRows()) == 0 {
			return fmt.Errorf("重建窗口写入行尚不可见")
		}
		if got := columnDouble(rsp.GetRows()[0], "close"); got != 12345.67 {
			return fmt.Errorf("close=%v, want 12345.67", got)
		}
		if got := columnDouble(rsp.GetRows()[0], "volume"); got != 76543.21 {
			return fmt.Errorf("volume=%v, want 76543.21", got)
		}
		return nil
	})
}

// ---------- 08 归档 ----------

func testArchive(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()

	// archive.timer 每 20s 把 space 下所有 dataset 归档成 parquet 并登记 ArchiveFile。
	retry(t, 60*time.Second, 3*time.Second, "归档文件登记（等待 archive.timer）", func() error {
		rsp, err := meta.ListArchiveFiles(ctx, &pb.ListArchiveFilesReq{SpaceId: e2eSpaceID, DatasetId: datasetID})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("列归档未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		if len(rsp.GetArchiveFiles()) == 0 {
			return fmt.Errorf("尚无归档文件")
		}
		for _, file := range rsp.GetArchiveFiles() {
			if file.GetFileFormat() != "parquet" {
				return fmt.Errorf("归档格式异常: %s", file.GetFileFormat())
			}
			if file.GetRowCount() > 0 {
				t.Logf("归档文件 %s 行数=%d uri=%s", file.GetArchiveFileId(), file.GetRowCount(), file.GetFileUri())
				return nil
			}
		}
		return fmt.Errorf("归档行数为 0")
	})
}

// ---------- 06 游标分页区间读 ----------

func testReadRangePagination(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	rows := klines(t)
	const pageSize = 150

	seen := make(map[string]bool)
	var pages int
	cursor := ""
	for {
		rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			Keys: []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
			Page: &pb.Page{Size: pageSize, Cursor: cursor},
		})
		require.NoError(t, err)
		requireSuccess(t, rsp.GetRetInfo())
		require.LessOrEqual(t, len(rsp.GetRows()), pageSize)
		for _, r := range rsp.GetRows() {
			dt := r.GetKey().GetDataTime()
			require.False(t, seen[dt], "游标分页出现重复行: %s", dt)
			seen[dt] = true
		}
		pages++
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
		cursor = rsp.GetPageResult().GetNextCursor()
		require.NotEmpty(t, cursor, "HasMore 时必须返回 next_cursor")
		require.Less(t, pages, 1000, "分页未收敛，疑似死循环")
	}
	require.Greater(t, pages, 1, "数据量应跨多页")
	require.Len(t, seen, len(rows), "游标分页应无遗漏无重复地覆盖全部行")
}

// ---------- 07 列裁剪读 ----------

func testReadColumnProjection(ctx context.Context, t *testing.T) {
	data := harness.DataClient()

	rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
		Keys:        []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: freq}},
		ColumnNames: []string{"close"},
		Page:        &pb.Page{Size: 5},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.NotEmpty(t, rsp.GetRows())
	for _, r := range rsp.GetRows() {
		names := columnNames(r)
		require.Equal(t, []string{"close"}, names, "列裁剪后应只返回 close 列")
	}
}

// ---------- 09 手动重建搜索索引 ----------

func testRebuildRecordView(ctx context.Context, t *testing.T) {
	query := harness.QueryClient()

	rsp, err := query.RebuildRecordView(ctx, &pb.RebuildRecordViewReq{
		SpaceId: e2eSpaceID,
		ViewId:  symbolsViewID,
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())

	// 重建为异步任务，应返回 rebuild_id；重建完成后仍可搜索命中。
	require.NotEmpty(t, rsp.GetRebuildId(), "异步重建应返回 rebuild_id")
	retry(t, 30*time.Second, time.Second, "重建后全文搜索命中", func() error {
		searchRsp, err := query.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{SpaceId: e2eSpaceID, ViewId: symbolsViewID, TextQuery: "AR"})
		if err != nil {
			return err
		}
		if len(searchRsp.GetRows()) == 0 {
			return fmt.Errorf("搜索结果为空")
		}
		return nil
	})
}

// ---------- 11 对象读写（按 record_id，对象型数据集）----------

func testRecordRead(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()
	data := harness.DataClient()

	// 字段字典。
	for _, f := range []fieldDef{
		{id: "status", name: "状态", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{id: "base_asset", name: "基础资产", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	} {
		f := f
		mustSuccess(t, "CreateField:"+f.id, func() *pb.RetInfo {
			rsp, err := meta.CreateField(ctx, &pb.CreateFieldReq{Field: &pb.Field{SpaceId: e2eSpaceID, FieldId: f.id, Name: f.name, ValueType: f.valueType, Status: "active"}})
			require.NoError(t, err)
			return rsp.GetRetInfo()
		})
	}

	// 记录型数据集 + View + 列契约 + 路由。Record 无需预注册，写入链路不自动维护 DatasetSubject 绑定。
	mustSuccess(t, "CreateDataset:symbols", func() *pb.RetInfo {
		rsp, err := meta.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
			SpaceId: e2eSpaceID, DatasetId: symbolsDatasetID, DataSourceId: dataSourceID,
			Name: "Binance 交易对资料", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	mustSuccess(t, "CreateView:symbols", func() *pb.RetInfo {
		rsp, err := meta.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId:          e2eSpaceID,
			ViewId:           symbolsViewID,
			Name:             "Binance 交易对资料视图",
			PrimaryDatasetId: symbolsDatasetID,
			DatasetIds:       []string{symbolsDatasetID},
			Engine:           "bleve",
			Status:           "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})
	for _, c := range []colDef{
		{name: "symbol", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{name: "status", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{name: "base_asset", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	} {
		c := c
		mustSuccess(t, "UpsertDatasetColumn:symbols:"+c.name, func() *pb.RetInfo {
			rsp, err := meta.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: &pb.DatasetColumn{
				SpaceId: e2eSpaceID, DatasetId: symbolsDatasetID, ColumnName: c.name,
				OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: c.name,
				ValueType: c.valueType, Status: "active",
			}})
			require.NoError(t, err)
			return rsp.GetRetInfo()
		})
		mustSuccess(t, "UpsertViewColumn:symbols:"+c.name, func() *pb.RetInfo {
			rsp, err := meta.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
				SpaceId:    e2eSpaceID,
				ViewId:     symbolsViewID,
				ColumnName: c.name,
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   symbolsDatasetID + "." + c.name,
				ValueType:  c.valueType,
			}})
			require.NoError(t, err)
			return rsp.GetRetInfo()
		})
	}
	mustSuccess(t, "CreatePrimaryStoreRoute:symbols", func() *pb.RetInfo {
		rsp, err := meta.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{
			SpaceId: e2eSpaceID, DatasetId: symbolsDatasetID, SubjectPattern: "*", NodeId: "node_pebble", Status: "active",
		}})
		require.NoError(t, err)
		return rsp.GetRetInfo()
	})

	// 写入两个对象，等待元数据缓存生效。
	row := func(recordID, symbol, status, baseAsset string) *pb.RecordRow {
		return &pb.RecordRow{
			Key: &pb.RecordKey{SpaceId: e2eSpaceID, DatasetId: symbolsDatasetID, RecordId: recordID},
			Columns: []*pb.ColumnValue{
				testutil.StringValue("symbol", symbol),
				testutil.StringValue("status", status),
				testutil.StringValue("base_asset", baseAsset),
			},
		}
	}
	retry(t, 25*time.Second, time.Second, "对象数据集写入（等待缓存生效）", func() error {
		rsp, err := data.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{
			row("AR-USDT", "ARUSDT", "active", "AR"),
			row("AR-USDT#legacy", "ARUSDT_LEGACY", "inactive", "AR"),
		}})
		if err != nil {
			return err
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return fmt.Errorf("写入未成功: %s", rsp.GetRetInfo().GetMsg())
		}
		require.Len(t, rsp.GetKeys(), 2)
		symbolRecordKeys = make(map[string]*pb.RecordKey, len(rsp.GetKeys()))
		for _, key := range rsp.GetKeys() {
			copied := *key
			symbolRecordKeys[key.GetRecordId()] = &copied
		}
		return nil
	})

	// 按 record_id 读取 AR-USDT。
	rsp, err := data.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{recordKeyFor(t, "AR-USDT")},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.Len(t, rsp.GetRows(), 1, "对象读取应只返回指定 record_id 的一行")
	require.Equal(t, "AR-USDT", rsp.GetRows()[0].GetKey().GetRecordId())
	require.Equal(t, "active", recordColumnString(rsp.GetRows()[0], "status"))
}

// ---------- 12 列级合并写入（同 key 增量更新）----------

func testUpsertColumnMerge(ctx context.Context, t *testing.T) {
	data := harness.DataClient()

	// 只更新 status 列，base_asset/symbol 应保留。
	key := recordKeyFor(t, "AR-USDT")
	upd, err := data.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{
		Key:     key,
		Columns: []*pb.ColumnValue{testutil.StringValue("status", "delisted")},
	}}})
	require.NoError(t, err)
	requireSuccess(t, upd.GetRetInfo())

	rsp, err := data.ReadRecordRows(ctx, &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{key},
	})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.Len(t, rsp.GetRows(), 1)
	got := rsp.GetRows()[0]
	require.Equal(t, "delisted", recordColumnString(got, "status"), "status 应被更新")
	require.Equal(t, "AR", recordColumnString(got, "base_asset"), "未携带的列应保留")
	require.Equal(t, "ARUSDT", recordColumnString(got, "symbol"), "未携带的列应保留")
}

// ---------- 14 写入校验错误码 ----------

func testWriteValidationErrors(ctx context.Context, t *testing.T) {
	data := harness.DataClient()
	rows := klines(t)
	base := rows[0]

	t.Run("unregistered_column", func(t *testing.T) {
		row := klineTimeSeriesRow(e2eSpaceID, datasetID, subjectID, freq, base)
		row.Columns = append(row.Columns, testutil.DoubleValue("not_a_real_column", 1))
		rsp, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{row}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "未登记列应返回 INVALID_PARAM")
	})

	t.Run("missing_subject_id", func(t *testing.T) {
		row := klineTimeSeriesRow(e2eSpaceID, datasetID, "", freq, base)
		rsp, err := data.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{row}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "缺少 subject_id 应返回 INVALID_PARAM")
	})

	t.Run("read_route_not_found", func(t *testing.T) {
		rsp, err := data.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			Keys: []*pb.TimeSeriesKey{{SpaceId: e2eSpaceID, DatasetId: "dataset_without_route", SubjectId: subjectID, Freq: freq}},
		})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_ROUTE_NOT_FOUND, rsp.GetRetInfo().GetCode(), "无路由数据集应返回 ROUTE_NOT_FOUND")
	})
}

// ---------- 15 资源不存在错误码 ----------

func testNotFoundErrors(ctx context.Context, t *testing.T) {
	meta := harness.MetadataClient()
	query := harness.QueryClient()

	t.Run("space_not_found", func(t *testing.T) {
		rsp, err := meta.GetSpace(ctx, &pb.GetSpaceReq{SpaceId: "no_such_space"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SPACE_NOT_FOUND, rsp.GetRetInfo().GetCode())
	})

	t.Run("dataset_not_found", func(t *testing.T) {
		rsp, err := meta.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: e2eSpaceID, DatasetId: "no_such_dataset"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_DATASET_NOT_FOUND, rsp.GetRetInfo().GetCode())
	})

	t.Run("view_not_found", func(t *testing.T) {
		rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{SpaceId: e2eSpaceID, ViewId: "no_such_view"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, rsp.GetRetInfo().GetCode())
	})
}

// ---------- 列/字段定义 ----------

// colDef 描述端到端测试动态创建的 Dataset 列。
type colDef struct {
	name      string
	valueType pb.FieldValueType
}

func klineColumns() []colDef {
	return []colDef{
		{name: "open", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "high", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "low", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "close", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "volume", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "quote_volume", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "trade_num", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{name: "symbol", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{name: "note", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
	}
}

func recordKeyFor(t *testing.T, recordID string) *pb.RecordKey {
	t.Helper()
	require.NotNil(t, symbolRecordKeys, "record keys should be captured from WriteRecordRowsRsp")
	key := symbolRecordKeys[recordID]
	require.NotNil(t, key, "record key not captured for %s", recordID)
	copied := *key
	return &copied
}

// fieldDef 描述端到端测试动态创建的字段定义。
type fieldDef struct {
	id        string
	name      string
	valueType pb.FieldValueType
}

func klineFields() []fieldDef {
	cols := klineColumns()
	out := make([]fieldDef, 0, len(cols))
	for _, c := range cols {
		out = append(out, fieldDef{id: c.name, name: c.name, valueType: c.valueType})
	}
	return out
}

// ---------- 通用辅助 ----------

func requireSuccess(t *testing.T, ret *pb.RetInfo) {
	t.Helper()
	require.Equal(t, pb.ErrorCode_SUCCESS, ret.GetCode(), "ret: %s", ret.GetMsg())
}

func mustSuccess(t *testing.T, op string, fn func() *pb.RetInfo) {
	t.Helper()
	var ret *pb.RetInfo
	for attempt := 0; attempt < 20; attempt++ {
		ret = fn()
		if ret.GetCode() == pb.ErrorCode_SUCCESS || !isSQLiteBusy(ret.GetMsg()) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	require.Equal(t, pb.ErrorCode_SUCCESS, ret.GetCode(), "%s 失败: %s", op, ret.GetMsg())
}

func isSQLiteBusy(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_busy")
}

func columnDouble(row *pb.TimeSeriesRow, name string) float64 {
	for _, c := range row.GetColumns() {
		if c.GetColumnName() == name {
			return c.GetValue().GetDoubleValue()
		}
	}
	return 0
}

func columnString(row *pb.TimeSeriesRow, name string) string {
	for _, c := range row.GetColumns() {
		if c.GetColumnName() == name {
			return c.GetValue().GetStringValue()
		}
	}
	return ""
}

func recordColumnString(row *pb.RecordRow, name string) string {
	for _, c := range row.GetColumns() {
		if c.GetColumnName() == name {
			return c.GetValue().GetStringValue()
		}
	}
	return ""
}

func getViewOrFail(ctx context.Context, t *testing.T, viewID string) *pb.View {
	t.Helper()
	rsp, err := harness.MetadataClient().GetView(ctx, &pb.GetViewReq{SpaceId: e2eSpaceID, ViewId: viewID})
	require.NoError(t, err)
	requireSuccess(t, rsp.GetRetInfo())
	require.NotNil(t, rsp.GetView())
	return rsp.GetView()
}

func columnNames(row *pb.TimeSeriesRow) []string {
	out := make([]string, 0, len(row.GetColumns()))
	for _, c := range row.GetColumns() {
		out = append(out, c.GetColumnName())
	}
	return out
}

// retry 在 timeout 内按 interval 轮询 fn，直到返回 nil；否则报告最后一次错误。
func retry(t *testing.T, timeout, interval time.Duration, desc string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = fn(); lastErr == nil {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("%s 在 %s 内未成功，最后错误: %v\n----- server.log -----\n%s", desc, timeout, lastErr, harness.LogTail())
}
