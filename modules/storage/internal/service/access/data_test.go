package access

import (
	"context"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/factkey"
	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primary"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMetadataMutationRefreshesCacheReader(t *testing.T) {
	ctx := context.Background()
	service := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Fatalf("close service: %v", err)
		}
	})

	spaceRsp, err := service.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto",
		Name:    "Crypto",
		Status:  "active",
	}})
	mustRetOK(t, spaceRsp, err)
	dataSourceRsp, err := service.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId:      "crypto",
		DataSourceId: "binance",
		Name:         "Binance",
		Kind:         "exchange",
		Market:       "crypto",
		Status:       "active",
	}})
	mustRetOK(t, dataSourceRsp, err)
	datasetRsp, err := service.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId:      "crypto",
		DatasetId:    "binance_kline",
		DataSourceId: "binance",
		Name:         "币安K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	}})
	mustRetOK(t, datasetRsp, err)

	dataset, err := service.MetadataReader().GetDataset(ctx, "crypto", "binance_kline")
	if err != nil {
		t.Fatalf("get dataset through cache reader after create: %v", err)
	}
	if dataset.GetDatasetId() != "binance_kline" {
		t.Fatalf("dataset_id = %q, want binance_kline", dataset.GetDatasetId())
	}
}

func mustRetOK[T interface{ GetRetInfo() *pb.RetInfo }](t *testing.T, rsp T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	ret := rsp.GetRetInfo()
	if ret == nil || ret.GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %#v, want success", ret)
	}
}

func storageSchemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql"))
}

func TestMetadataValidationHelpers_RejectEmptyIDs(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, spaceRsp.GetRetInfo().GetCode())

	dsRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: "crypto"}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, dsRsp.GetRetInfo().GetCode())

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{SpaceId: "crypto"}})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())
}

func TestGetSpaceAndListSpaces(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	createRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, createRsp, err)

	getRsp, err := svc.GetSpace(ctx, &pb.GetSpaceReq{SpaceId: "crypto"})
	mustRetOK(t, getRsp, err)
	assert.Equal(t, "Crypto", getRsp.GetSpace().GetName())

	listRsp, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{})
	mustRetOK(t, listRsp, err)
	require.NotEmpty(t, listRsp.GetSpaces())
}

func TestReadTimeSeriesRowsAppliesGlobalPageWithoutPerKeyTruncation(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1500}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{
			{SpaceId: "crypto", DatasetId: "kline", SubjectId: "A", Freq: "1m"},
			{SpaceId: "crypto", DatasetId: "kline", SubjectId: "B", Freq: "1m"},
		},
		Page: &pb.Page{Page: 50, Size: 25},
	})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 1251 || primary.pageSizes[1] != 1251 {
		t.Fatalf("downstream page sizes = %v, want offset + size + 1 for every key", primary.pageSizes)
	}
	if len(rsp.GetRows()) != 25 || rsp.GetRows()[0].GetKey().GetSubjectId() != "A" {
		t.Fatalf("rows = %d first subject = %q, want the 50th global page from subject A", len(rsp.GetRows()), rsp.GetRows()[0].GetKey().GetSubjectId())
	}
	if rsp.GetPageResult().GetTotalState() != pb.TotalState_SKIPPED || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("page_result = %+v, want skipped total and has_more", rsp.GetPageResult())
	}
}

func TestReadRecordRowsUsesRecordIDForGlobalPage(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1500}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{
			{SpaceId: "crypto", DatasetId: "news", RecordId: "A"},
			{SpaceId: "crypto", DatasetId: "news", RecordId: "B"},
		},
		VersionRange: &pb.VersionRange{StartVersion: "2026-07-01T00:00:00Z", EndVersion: "2026-07-02T00:00:00Z"},
		Page:         &pb.Page{Page: 50, Size: 25},
	})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 1251 || primary.pageSizes[1] != 1251 {
		t.Fatalf("downstream page sizes = %v, want offset + size + 1 for every key", primary.pageSizes)
	}
	if len(rsp.GetRows()) != 25 || rsp.GetRows()[0].GetKey().GetRecordId() != "A" {
		t.Fatalf("rows = %d first record = %q, want the 50th global page from record A", len(rsp.GetRows()), rsp.GetRows()[0].GetKey().GetRecordId())
	}
	if rsp.GetPageResult().GetTotalState() != pb.TotalState_SKIPPED || !rsp.GetPageResult().GetHasMore() {
		t.Fatalf("page_result = %+v, want skipped total and has_more", rsp.GetPageResult())
	}
}

func TestReadRecordRowsDoesNotCapVersionPrefixKeys(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 2}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{
			{SpaceId: "crypto", DatasetId: "news", RecordId: "A"},
			{SpaceId: "crypto", DatasetId: "news", RecordId: "B"},
		},
		Page: &pb.Page{Page: 2, Size: 1},
	})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %+v", rsp.GetRetInfo())
	}
	if len(primary.pageSizes) != 2 || primary.pageSizes[0] != 3 || primary.pageSizes[1] != 3 {
		t.Fatalf("downstream page sizes = %v, want page offset + size + 1 for version-prefix keys", primary.pageSizes)
	}
}

func TestReadTimeSeriesRowsAllowsLargeExactKeyBatch(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	keys := make([]*pb.TimeSeriesKey, 0, 200)
	for i := 0; i < 200; i++ {
		keys = append(keys, &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: fmt.Sprintf("subject-%03d", i), Freq: "1m",
			DataTime: "2026-07-10T00:00:00Z",
		})
	}

	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{Keys: keys})
	if err != nil {
		t.Fatalf("ReadTimeSeriesRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 200 {
		t.Fatalf("ret=%+v rows=%d, want all exact rows", rsp.GetRetInfo(), len(rsp.GetRows()))
	}
	for _, size := range primary.pageSizes {
		if size != 1 {
			t.Fatalf("downstream exact-key page size = %d, want 1", size)
		}
	}
}

func TestReadRecordRowsAllowsLargeExactKeyBatch(t *testing.T) {
	primary := &multiKeyPrimary{rowsPerKey: 1}
	svc := &Service{primary: primary, router: router.NewResolver(fakeRouteReader{})}
	keys := make([]*pb.RecordKey, 0, 200)
	for i := 0; i < 200; i++ {
		keys = append(keys, &pb.RecordKey{
			SpaceId: "crypto", DatasetId: "news", RecordId: fmt.Sprintf("record-%03d", i),
			Version: "2026-07-10T00:00:00Z",
		})
	}

	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{Keys: keys})
	if err != nil {
		t.Fatalf("ReadRecordRows: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != 200 {
		t.Fatalf("ret=%+v rows=%d, want all exact rows", rsp.GetRetInfo(), len(rsp.GetRows()))
	}
	for _, size := range primary.pageSizes {
		if size != 1 {
			t.Fatalf("downstream exact-key page size = %d, want 1", size)
		}
	}
}

type multiKeyPrimary struct {
	rowsPerKey int
	pageSizes  []uint32
}

func (*multiKeyPrimary) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (p *multiKeyPrimary) ReadRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	size := uint32(1000)
	if req.GetPage().GetSize() > 0 {
		size = req.GetPage().GetSize()
	}
	p.pageSizes = append(p.pageSizes, size)
	count := min(int(size), p.rowsPerKey)
	rows := make([]*pb.PrimaryStoreRow, 0, count)
	for i := 0; i < count; i++ {
		key := proto.Clone(req.GetKeys()[0]).(*pb.PrimaryStoreKey)
		key.Version = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano)
		rows = append(rows, &pb.PrimaryStoreRow{Key: key, Attributes: map[string]string{"sequence": fmt.Sprint(i)}})
	}
	return rows, &pb.PageResult{Size: size, HasMore: count < p.rowsPerKey, TotalState: pb.TotalState_SKIPPED}, nil
}

func (*multiKeyPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func TestScanRecordDatasetReturnsSortedRows(t *testing.T) {
	svc := &Service{
		primary: recordDatasetScanner{rowsPerPage: 2, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ReadRecordRows(context.Background(), &pb.ReadRecordRowsReq{
		Keys: []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "news"}},
		Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 2)
}

func TestReportViewErrorDelegatesToReporter(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	_, err := bus.SubscribeTimeSeriesRowsUpdated(ctx, func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		return errors.New("publish failed")
	})
	require.NoError(t, err)

	var reportedStage string
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   &capturingPrimary{},
		events:    bus,
		report: func(_ context.Context, stage string, err error) {
			reportedStage = stage
			require.Error(t, err)
		},
	}

	rsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}},
		}},
	}}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "time_series_rows_updated", reportedStage)
}

func TestWriteRoutedRowsUsesMessageWriter(t *testing.T) {
	writer := &messageWriterPrimary{}
	svc := &Service{primary: writer}
	group := &routedRows{
		target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		rows:   []*pb.PrimaryStoreRow{{Key: &pb.PrimaryStoreKey{SpaceId: "crypto", DatasetId: "kline"}}},
	}
	require.NoError(t, svc.writeRoutedRows(context.Background(), group, []byte("outbox")))
	assert.True(t, writer.usedMessageWriter)
}

type recordDatasetScanner struct {
	rowsPerPage int
	pages       int
}

func (r recordDatasetScanner) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (r recordDatasetScanner) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (r recordDatasetScanner) ScanRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	if req.GetPage().GetCursor() != "" {
		_, _ = fmt.Sscanf(req.GetPage().GetCursor(), "%d", &pageNo)
	}
	rows := make([]*pb.PrimaryStoreRow, r.rowsPerPage)
	for i := range rows {
		rows[i] = &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
			SpaceId:   "crypto",
			DatasetId: "news",
			DataKind:  pb.DataKind_DATA_KIND_RECORD,
			Key:       fmt.Sprintf("news-%d", i),
			Version:   fmt.Sprintf("2026-07-09T00:00:%02dZ", i),
		}}
	}
	return rows, &pb.PageResult{
		HasMore:    int(pageNo) < r.pages,
		NextCursor: fmt.Sprint(pageNo + 1),
	}, nil
}

type messageWriterPrimary struct {
	usedMessageWriter bool
}

func (m *messageWriterPrimary) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (m *messageWriterPrimary) WriteRowsWithMessage(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow, []byte) error {
	m.usedMessageWriter = true
	return nil
}

func (*messageWriterPrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*messageWriterPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func TestScanAllPrimaryRowsStopsAtGuardLimit(t *testing.T) {
	svc := &Service{primary: fakePrimaryScanner{rowsPerPage: 1000, pages: (maxDatasetScanRows / 1000) + 2}}

	_, err := svc.scanAllPrimaryRows(context.Background(), nil, &pb.PrimaryStoreTarget{},
		pb.DataKind_DATA_KIND_TIME_SERIES, nil, nil)
	if err == nil {
		t.Fatal("scanAllPrimaryRows() error = nil, want broad scan guard error")
	}
	if got := err.Error(); got == "" || !containsAll(got, "dataset scan", fmt.Sprint(maxDatasetScanRows)) {
		t.Fatalf("scanAllPrimaryRows() error = %q, want dataset scan guard with limit", got)
	}
}

func TestScanTimeSeriesRowsPagesPrimaryStoreWithoutDatasetGuard(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 1000, pages: (maxDatasetScanRows / 1000) + 2},
		router:  router.NewResolver(fakeRouteReader{}),
	}

	rows, page, err := svc.FactReader().ScanTimeSeriesRows(context.Background(), "crypto", "kline",
		&pb.TimeRange{StartTime: "2026-07-09T00:00:00Z"}, nil, &pb.Page{Size: 1000})
	if err != nil {
		t.Fatalf("ScanTimeSeriesRows() error = %v, want paged scan to bypass broad dataset guard", err)
	}
	if len(rows) != 1000 {
		t.Fatalf("ScanTimeSeriesRows() rows = %d, want one page of 1000 rows", len(rows))
	}
	if page == nil || !page.GetHasMore() || page.GetNextCursor() == "" {
		t.Fatalf("ScanTimeSeriesRows() page = %+v, want has_more with next cursor", page)
	}
}

type fakePrimaryScanner struct {
	rowsPerPage int
	pages       int
}

func (f fakePrimaryScanner) WriteRows(context.Context, *pb.PrimaryStoreTarget, []*pb.PrimaryStoreRow) error {
	return nil
}

func (f fakePrimaryScanner) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (f fakePrimaryScanner) ScanRows(_ context.Context, _ *pb.PrimaryStoreTarget, req *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	pageNo := uint32(1)
	if req.GetPage().GetCursor() != "" {
		_, _ = fmt.Sscanf(req.GetPage().GetCursor(), "%d", &pageNo)
	}
	rows := make([]*pb.PrimaryStoreRow, f.rowsPerPage)
	for i := range rows {
		rows[i] = &pb.PrimaryStoreRow{Key: &pb.PrimaryStoreKey{
			SpaceId:   "crypto",
			DatasetId: "kline",
			DataKind:  req.GetDataKind(),
			Key:       factkey.BuildTimeSeriesDataKey(fmt.Sprintf("sub-%d-%d", pageNo, i), "1m", nil),
			Version:   fmt.Sprintf("2026-07-09T00:00:%02dZ", i%60),
		}}
	}
	next := pageNo + 1
	return rows, &pb.PageResult{
		HasMore:    int(pageNo) < f.pages,
		NextCursor: fmt.Sprint(next),
	}, nil
}

type fakeRouteReader struct{}

func (fakeRouteReader) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return []*pb.PrimaryStoreRoute{{
		SpaceId:        "crypto",
		DatasetId:      "kline",
		RouteId:        "route-1",
		SubjectPattern: "*",
		NodeId:         "node-1",
		Status:         "active",
	}}, &pb.PageResult{}, nil
}

func (fakeRouteReader) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return &pb.PrimaryStoreNode{NodeId: "node-1", Status: "active"}, nil
}

func (fakeRouteReader) ListDevices(context.Context, string, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return []*pb.Device{{DeviceId: "dev-1", Engine: "pebble", Status: "active"}}, &pb.PageResult{}, nil
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func TestDecodePrimaryScanCursorParsesTargetAndInnerCursor(t *testing.T) {
	idx, inner, err := decodePrimaryScanCursor("2|page-3")
	require.NoError(t, err)
	assert.Equal(t, 2, idx)
	assert.Equal(t, "page-3", inner)

	idx, inner, err = decodePrimaryScanCursor("legacy")
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
	assert.Equal(t, "legacy", inner)
}

func TestScanTimeSeriesDatasetReturnsSortedRows(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 2, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline"}},
		Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 2)
}

func TestScanRecordRowsRPCRejectsMissingIDs(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.ScanRecordRows(context.Background(), &pb.ScanRecordRowsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNextRecordVersionIncreasesMonotonically(t *testing.T) {
	svc := &Service{}
	first := svc.nextRecordVersion()
	second := svc.nextRecordVersion()
	assert.True(t, second.After(first))
}

func TestCreateDatasetRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateDataset(context.Background(), &pb.CreateDatasetReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCreatePrimaryStoreNodePersistsGeneratedID(t *testing.T) {
	store := &stubMetadataStore{}
	svc := &Service{metadata: store}
	rsp, err := svc.CreatePrimaryStoreNode(context.Background(), &pb.CreatePrimaryStoreNodeReq{
		Node: &pb.PrimaryStoreNode{Name: "node-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetNode().GetNodeId())
	assert.Equal(t, rsp.GetNode().GetNodeId(), store.lastNode.GetNodeId())
}

func TestListDevicesReturnsStoredDevices(t *testing.T) {
	store := &stubMetadataStore{
		devices: []*pb.Device{{DeviceId: "dev-1", NodeId: "node-1", Engine: "pebble"}},
	}
	svc := &Service{metadata: store}
	rsp, err := svc.ListDevices(context.Background(), &pb.ListDevicesReq{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetDevices(), 1)
	assert.Equal(t, "dev-1", rsp.GetDevices()[0].GetDeviceId())
}

func TestNormalizeWriteRecordRowsFillsVersion(t *testing.T) {
	svc := &Service{}
	rows := svc.normalizeWriteRecordRows([]*pb.RecordRow{{
		Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"},
	}})
	require.Len(t, rows, 1)
	_, err := time.Parse(time.RFC3339Nano, rows[0].GetKey().GetVersion())
	assert.NoError(t, err)
}

func TestTimeSeriesKeyAdapterRoundTrip(t *testing.T) {
	row := &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m",
			DataTime: "2026-07-10T12:00:00Z", Dimensions: map[string]string{"venue": "binance"},
		},
		Columns:    []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
		Attributes: map[string]string{"source": "test"},
	}

	primaryRow, err := timeSeriesRowToPrimaryStoreRow(row)
	require.NoError(t, err)
	assert.Equal(t, "crypto", primaryRow.GetKey().GetSpaceId())
	assert.Contains(t, primaryRow.GetAttributes(), timeSeriesDimensionsAttribute)

	restored := primaryStoreRowToTimeSeriesRow(primaryRow, row.GetKey())
	assert.Equal(t, "BTC", restored.GetKey().GetSubjectId())
	assert.Equal(t, "binance", restored.GetKey().GetDimensions()["venue"])
	assert.NotContains(t, restored.GetAttributes(), timeSeriesDimensionsAttribute)
}

func TestRecordKeyAdapterRoundTrip(t *testing.T) {
	row := &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING}},
	}

	primaryRow, err := recordRowToPrimaryStoreRow(row)
	require.NoError(t, err)
	assert.Equal(t, pb.DataKind_DATA_KIND_RECORD, primaryRow.GetKey().GetDataKind())

	restored := primaryStoreRowToRecordRow(primaryRow, row.GetKey())
	assert.Equal(t, "news-1", restored.GetKey().GetRecordId())
	assert.NotEmpty(t, restored.GetKey().GetVersion())
}

func TestValidateTimeRangeRejectsInvertedWindow(t *testing.T) {
	err := validateTimeRange(&pb.TimeRange{
		StartTime: "2026-07-11T00:00:00Z",
		EndTime:   "2026-07-10T00:00:00Z",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start_time")
}

func TestValidateUserAttributesRejectsReservedPrefix(t *testing.T) {
	err := validateUserAttributes(map[string]string{reservedAttributePrefix + "secret": "x"})
	require.Error(t, err)
}

func TestPageMergedRecordRowsUsesSkippedTotal(t *testing.T) {
	rows := []*pb.RecordRow{
		{Key: &pb.RecordKey{RecordId: "a"}},
		{Key: &pb.RecordKey{RecordId: "b"}},
		{Key: &pb.RecordKey{RecordId: "c"}},
	}
	plan := &multiKeyPagePlan{pageNo: 2, size: 1, start: 1}
	paged, result := pageMergedRecordRows(rows, plan, false)

	require.Len(t, paged, 1)
	assert.Equal(t, "b", paged[0].GetKey().GetRecordId())
	assert.Equal(t, pb.TotalState_SKIPPED, result.GetTotalState())
	assert.True(t, result.GetHasMore())
}

func TestWriteTimeSeriesRowsRoutesAndPublishes(t *testing.T) {
	ctx := context.Background()
	bus := eventbus.NewMemoryBus()
	primaryStore := &capturingPrimary{}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   primaryStore,
		events:    bus,
	}

	var published int
	_, err := bus.SubscribeTimeSeriesRowsUpdated(ctx, func(context.Context, *pb.TimeSeriesRowsUpdated) error {
		published++
		return nil
	})
	require.NoError(t, err)

	rsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.2}},
		}},
	}}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, 1, primaryStore.written)
	assert.Equal(t, 1, published)
}

func TestWriteTimeSeriesRowsReturnsCommittedKeysWhenLaterTargetFails(t *testing.T) {
	ctx := context.Background()
	primaryStore := &partialFailurePrimary{failNode: "node-2"}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{}),
		router:    router.NewResolver(twoTargetRouteReader{}),
		primary:   primaryStore,
	}
	rows := []*pb.TimeSeriesRow{
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "A", Freq: "1m", DataTime: "2026-07-10T12:00:00Z"}, Columns: []*pb.ColumnValue{doubleColumn("close", 1.2)}},
		{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "B", Freq: "1m", DataTime: "2026-07-10T12:01:00Z"}, Columns: []*pb.ColumnValue{doubleColumn("close", 1.3)}},
	}

	rsp, err := svc.WriteTimeSeriesRows(ctx, &pb.WriteTimeSeriesRowsReq{Rows: rows})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetWrittenKeys(), 1)
	assert.Contains(t, rsp.GetWrittenKeys()[0], "A")
	assert.Equal(t, []string{"node-1", "node-2"}, primaryStore.calls)
}

func TestWriteRecordRowsReturnsCommittedKeysWhenLaterTargetFails(t *testing.T) {
	ctx := context.Background()
	primaryStore := &partialFailurePrimary{failNode: "node-2"}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{record: true}),
		router:    router.NewResolver(twoTargetRouteReader{}),
		primary:   primaryStore,
	}
	rows := []*pb.RecordRow{
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "kline", RecordId: "A", Version: "2026-07-10T12:00:00Z"}, Columns: []*pb.ColumnValue{stringColumn("title", "first")}},
		{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "kline", RecordId: "B", Version: "2026-07-10T12:01:00Z"}, Columns: []*pb.ColumnValue{stringColumn("title", "second")}},
	}

	rsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: rows})
	require.NoError(t, err)
	assert.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetKeys(), 1)
	assert.Equal(t, "A", rsp.GetKeys()[0].GetRecordId())
}

func doubleColumn(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func stringColumn(name, value string) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}
}

func TestWriteRecordRowsAssignsTimestampVersion(t *testing.T) {
	ctx := context.Background()
	primaryStore := &capturingPrimary{}
	svc := &Service{
		validator: schema.NewValidator(writeValidatorMetadata{record: true}),
		router:    router.NewResolver(fakeRouteReader{}),
		primary:   primaryStore,
		events:    eventbus.NewMemoryBus(),
	}

	rsp, err := svc.WriteRecordRows(ctx, &pb.WriteRecordRowsReq{Rows: []*pb.RecordRow{{
		Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"},
		Columns: []*pb.ColumnValue{{
			ColumnName: "title",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "headline"}},
		}},
	}}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetKeys(), 1)
	_, parseErr := time.Parse(time.RFC3339Nano, rsp.GetKeys()[0].GetVersion())
	assert.NoError(t, parseErr)
	assert.Equal(t, 1, primaryStore.written)
}

func TestErrorCodeHelpers(t *testing.T) {
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, primaryErrorCode(errors.New("field is required")))
	assert.Equal(t, pb.ErrorCode_ENGINE_CAPABILITY_UNSUPPORTED, primaryErrorCode(errors.New("engine_capability_unsupported")))
	assert.Equal(t, pb.ErrorCode_ROUTE_NOT_FOUND, groupRowsErrorCode(errors.New("route missing")))
}

type writeValidatorMetadata struct {
	record bool
}

func (m writeValidatorMetadata) GetDataset(_ context.Context, spaceID, datasetID string) (*pb.Dataset, error) {
	kind := pb.DataKind_DATA_KIND_TIME_SERIES
	if m.record {
		kind = pb.DataKind_DATA_KIND_RECORD
	}
	return &pb.Dataset{
		SpaceId:   spaceID,
		DatasetId: datasetID,
		DataKind:  kind,
		Status:    "active",
	}, nil
}

func (writeValidatorMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return []*pb.DatasetColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		{ColumnName: "title", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"},
	}, &pb.PageResult{}, nil
}

type capturingPrimary struct {
	written int
}

func (p *capturingPrimary) WriteRows(_ context.Context, _ *pb.PrimaryStoreTarget, rows []*pb.PrimaryStoreRow) error {
	p.written += len(rows)
	return nil
}

func (*capturingPrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*capturingPrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

var _ primary.Client = (*capturingPrimary)(nil)

type partialFailurePrimary struct {
	failNode string
	calls    []string
}

func (p *partialFailurePrimary) WriteRows(_ context.Context, target *pb.PrimaryStoreTarget, _ []*pb.PrimaryStoreRow) error {
	p.calls = append(p.calls, target.GetNodeId())
	if target.GetNodeId() == p.failNode {
		return errors.New("injected target failure")
	}
	return nil
}

func (*partialFailurePrimary) ReadRows(context.Context, *pb.PrimaryStoreTarget, *pb.ReadPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

func (*partialFailurePrimary) ScanRows(context.Context, *pb.PrimaryStoreTarget, *pb.ScanPrimaryRowsReq) ([]*pb.PrimaryStoreRow, *pb.PageResult, error) {
	return nil, nil, nil
}

type twoTargetRouteReader struct{}

func (twoTargetRouteReader) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return []*pb.PrimaryStoreRoute{
		{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-a", SubjectPattern: "A", NodeId: "node-1", Status: "active"},
		{SpaceId: "crypto", DatasetId: "kline", RouteId: "route-b", SubjectPattern: "B", NodeId: "node-2", Status: "active"},
	}, &pb.PageResult{}, nil
}

func (twoTargetRouteReader) GetPrimaryStoreNode(_ context.Context, nodeID string) (*pb.PrimaryStoreNode, error) {
	return &pb.PrimaryStoreNode{NodeId: nodeID, Status: "active"}, nil
}

func (twoTargetRouteReader) ListDevices(_ context.Context, nodeID, _ string, _ *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	return []*pb.Device{{DeviceId: "device-" + nodeID, NodeId: nodeID, Engine: "pebble", Status: "active"}}, &pb.PageResult{}, nil
}

var _ primary.Client = (*partialFailurePrimary)(nil)
