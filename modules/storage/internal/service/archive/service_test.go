package archive

import (
	"context"
	"testing"
	"time"

	coreeventbus "github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubArchiveMetadata struct {
	dataset         *pb.Dataset
	subjects        []*pb.DatasetSubject
	devices         []*pb.Device
	registeredFiles []*pb.ArchiveFile
}

func (s *stubArchiveMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return s.dataset, nil
}
func (s *stubArchiveMetadata) ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	return nil, nil, nil
}
func (s *stubArchiveMetadata) ListDatasetSubjects(context.Context, string, string, string, *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	return s.subjects, nil, nil
}
func (s *stubArchiveMetadata) ListDevices(context.Context, string, string, *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	if s.devices != nil {
		return s.devices, nil, nil
	}
	return []*pb.Device{{DeviceId: "dev-parquet", Status: "active"}}, nil, nil
}
func (s *stubArchiveMetadata) RegisterArchiveFile(_ context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	s.registeredFiles = append(s.registeredFiles, item)
	return item, nil
}

type stubFactReader struct {
	rows []*pb.TimeSeriesRow
}

func (s *stubFactReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:    s.rows,
	}, nil
}

func TestService_ArchiveDataset_ValidInputs_ShouldRegisterParquetFile(t *testing.T) {
	ctx := context.Background()
	meta := &stubArchiveMetadata{
		dataset: &pb.Dataset{
			SpaceId:   "crypto",
			DatasetId: "binance_kline",
			Freqs:     []string{"1m"},
			Status:    "active",
		},
		subjects: []*pb.DatasetSubject{{
			SpaceId:   "crypto",
			DatasetId: "binance_kline",
			SubjectId: "BTCUSDT",
			Status:    "active",
		}},
	}
	facts := &stubFactReader{rows: []*pb.TimeSeriesRow{{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "binance_kline", SubjectId: "BTCUSDT", Freq: "1m",
			DataTime: "2026-07-12T10:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 100}},
		}},
	}}}
	fixedNow := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	svc := NewService(Options{
		Metadata:    meta,
		Facts:       facts,
		ArchiveRoot: t.TempDir(),
		Now:         func() time.Time { return fixedNow },
	})

	file, err := svc.ArchiveDataset(ctx, "crypto", "binance_kline", "daily", nil)
	require.NoError(t, err)
	require.NotNil(t, file)
	assert.Equal(t, "crypto", file.GetSpaceId())
	assert.Equal(t, "binance_kline", file.GetDatasetId())
	assert.Equal(t, "parquet", file.GetFileFormat())
	assert.Equal(t, uint64(1), file.GetRowCount())
	require.Len(t, meta.registeredFiles, 1)
	assert.Contains(t, file.GetFileUri(), "file://")
}

func TestService_ArchiveDataset_MissingSpaceID_ShouldReturnError(t *testing.T) {
	svc := NewService(Options{Metadata: &stubArchiveMetadata{}, Facts: &stubFactReader{}, ArchiveRoot: t.TempDir()})
	_, err := svc.ArchiveDataset(context.Background(), "", "dataset", "daily", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "space_id and dataset_id are required")
}

func TestSafePathPart_UnsafeCharacters_ShouldSanitize(t *testing.T) {
	assert.Equal(t, "default", safePathPart(""))
	assert.Equal(t, "crypto_space", safePathPart("crypto space"))
	assert.Equal(t, "archive_crypto_dataset_123", safePathPart("archive/crypto@dataset#123"))
}

func TestArchiveFileID_ShouldIncludeSpaceDatasetAndTimestamp(t *testing.T) {
	now := time.Unix(1700000000, 42)
	id := archiveFileID("crypto", "binance_kline", now)
	assert.Contains(t, id, "archive_crypto_binance_kline")
}

func TestService_ReadAllRows_MissingSubjects_ShouldReturnError(t *testing.T) {
	meta := &stubArchiveMetadata{
		dataset:  &pb.Dataset{Freqs: []string{"1m"}},
		subjects: nil,
	}
	svc := NewService(Options{Metadata: meta, Facts: &stubFactReader{}, ArchiveRoot: t.TempDir()})
	_, err := svc.readAllRows(context.Background(), "crypto", "binance_kline", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dataset subjects are required")
}

func TestService_DefaultDeviceID_NoActiveDevice_ShouldReturnError(t *testing.T) {
	meta := &stubArchiveMetadata{devices: nil}
	meta.devices = []*pb.Device{}
	svc := NewService(Options{Metadata: meta})
	_, err := svc.defaultDeviceID(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active parquet_archive device not found")
}

func TestNewEventConsumer_StartAndClose_ShouldSubscribeBothTopics(t *testing.T) {
	ctx := context.Background()
	bus := coreeventbus.NewMemoryBus()
	t.Cleanup(func() {
		require.NoError(t, bus.Close())
	})

	timeSeriesCalled := false
	recordCalled := false
	consumer := NewEventConsumer(EventConsumerOptions{
		Events: bus,
		HandleTimeSeries: func(context.Context, *pb.TimeSeriesRowsCommitted) error {
			timeSeriesCalled = true
			return nil
		},
		HandleRecord: func(context.Context, *pb.RecordRowsCommitted) error {
			recordCalled = true
			return nil
		},
	})
	require.NoError(t, consumer.Start(ctx))
	require.NoError(t, bus.PublishTimeSeriesRowsCommitted(ctx, &pb.TimeSeriesRowsCommitted{}))
	require.NoError(t, bus.PublishRecordRowsCommitted(ctx, &pb.RecordRowsCommitted{}))
	require.NoError(t, consumer.Close())
	assert.True(t, timeSeriesCalled)
	assert.True(t, recordCalled)
}

func TestNewEventConsumer_NilBus_ShouldReturnError(t *testing.T) {
	consumer := NewEventConsumer(EventConsumerOptions{})
	err := consumer.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires subscribable event bus")
}

func TestNewEventConsumer_DoubleStart_ShouldReturnError(t *testing.T) {
	bus := coreeventbus.NewMemoryBus()
	t.Cleanup(func() {
		require.NoError(t, bus.Close())
	})
	consumer := NewEventConsumer(EventConsumerOptions{Events: bus})
	require.NoError(t, consumer.Start(context.Background()))
	err := consumer.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
	require.NoError(t, consumer.Close())
}

func TestParseScheduleParams_SingleDataset_ShouldParseFields(t *testing.T) {
	req, err := parseScheduleParams("space_id=crypto&dataset_id=binance_kline&partition_key=daily&start_time=2026-07-01&end_time=2026-07-02")
	require.NoError(t, err)
	assert.Equal(t, "crypto", req.spaceID)
	assert.Equal(t, "binance_kline", req.datasetID)
	assert.False(t, req.allDatasets)
	assert.Equal(t, "daily", req.partitionKey)
	require.NotNil(t, req.timeRange)
	assert.Equal(t, "2026-07-01", req.timeRange.GetStartTime())
	assert.Equal(t, "2026-07-02", req.timeRange.GetEndTime())
}

func TestParseScheduleParams_AllDatasets_ShouldSetFlag(t *testing.T) {
	req, err := parseScheduleParams("space_id=crypto;dataset_id=all")
	require.NoError(t, err)
	assert.True(t, req.allDatasets)
}

func TestParseScheduleParams_MissingDatasetID_ShouldReturnError(t *testing.T) {
	_, err := parseScheduleParams("space_id=crypto")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "space_id and dataset_id are required")
}

func TestHandleSchedule_ServiceNotInitialized_ShouldReturnError(t *testing.T) {
	SetDefaultService(nil)
	err := HandleSchedule(context.Background(), "space_id=crypto&dataset_id=binance_kline")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive service is not initialized")
}

var (
	_ Metadata   = (*stubArchiveMetadata)(nil)
	_ FactReader = (*stubFactReader)(nil)
)
