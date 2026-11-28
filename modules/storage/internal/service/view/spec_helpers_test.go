package view

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

// testCreatedAt 是测试数据集的创建时刻（2026-11-01T00:00:00Z 的 Unix 毫秒），索引文件记下同一个值才算属于它。
const testCreatedAt int64 = 1793491200000

// testMetadata 按一页返回固定的数据集与列。
type testMetadata struct {
	datasets []*pb.Dataset
	columns  []*pb.DatasetColumn
}

func (m *testMetadata) ListDatasets(context.Context, *pb.ListDatasetsReq, ...client.Option) (*pb.ListDatasetsRsp, error) {
	return &pb.ListDatasetsRsp{RetInfo: successRetInfo(), Datasets: m.datasets, PageResult: &pb.PageResult{Page: 1, Size: metadataPageSize, Total: uint32(len(m.datasets))}}, nil
}

func (m *testMetadata) ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error) {
	return &pb.ListDatasetColumnsRsp{RetInfo: successRetInfo(), Columns: m.columns, PageResult: &pb.PageResult{Page: 1, Size: metadataPageSize, Total: uint32(len(m.columns))}}, nil
}

// metadataFor 返回只含给定声明的元数据。
func metadataFor(specs ...*viewSpec) *testMetadata {
	metadata := &testMetadata{}
	for _, spec := range specs {
		metadata.datasets = append(metadata.datasets, &pb.Dataset{
			SpaceId: spec.SpaceID, DatasetId: spec.DatasetID, DataKind: spec.DataKind, Frequency: spec.Freq,
			Status: spec.Status, MaintenancePriority: spec.MaintenancePriority, CreatedAt: spec.CreatedAt,
		})
		for _, column := range spec.Columns {
			metadata.columns = append(metadata.columns, &pb.DatasetColumn{
				SpaceId: spec.SpaceID, DatasetId: spec.DatasetID, ColumnName: column.ColumnName, ValueType: column.ValueType,
				DisplayName: spec.DisplayNames[column.ColumnName], Status: "active",
			})
		}
		for _, name := range spec.HiddenColumns {
			metadata.columns = append(metadata.columns, &pb.DatasetColumn{
				SpaceId: spec.SpaceID, DatasetId: spec.DatasetID, ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "disabled",
			})
		}
	}
	return metadata
}

// timeSeriesSpec 是一个启用的时序数据集声明，列都是浮点数。
func timeSeriesSpec(space, dataset, freq string, columns ...string) *viewSpec {
	return testSpec(space, dataset, pb.DataKind_DATA_KIND_TIME_SERIES, freq, columns...)
}

// recordSpec 是一个启用的记录型数据集声明，列都是字符串。
func recordSpec(space, dataset string, columns ...string) *viewSpec {
	return testSpec(space, dataset, pb.DataKind_DATA_KIND_RECORD, "", columns...)
}

func testSpec(space, dataset string, kind pb.DataKind, freq string, columns ...string) *viewSpec {
	valueType := pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	if kind == pb.DataKind_DATA_KIND_RECORD {
		valueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	items := make([]*pb.DatasetColumn, 0, len(columns))
	for _, name := range columns {
		items = append(items, &pb.DatasetColumn{ColumnName: name, ValueType: valueType, DisplayName: name, Status: "active"})
	}
	return newViewSpec(&pb.Dataset{SpaceId: space, DatasetId: dataset, DataKind: kind, Frequency: freq, Status: "active", CreatedAt: testCreatedAt}, items)
}

// testRuntime 给声明建运行时（引擎按数据类型从服务里取）。
func testRuntime(svc *Service, spec *viewSpec) *viewRuntime {
	runtime, err := svc.ensureRuntime(context.Background(), spec)
	if err != nil {
		panic(err)
	}
	return runtime
}

// attachTestIndex 把运行时直接挂上一份活动索引契约，不经过引擎。
func attachTestIndex(runtime *viewRuntime, schema viewindex.ViewIndexSchema) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	attachActiveLocked(runtime, runtime.spec, schema)
}

// builtStats 是一个建好的索引文件记下的状态：属于 dataset（创建时间是 testCreatedAt）、记下了跟随进度。
func builtStats(dataset string, revision uint64, hash string) viewindex.ViewIndexStats {
	return viewindex.ViewIndexStats{Exists: true, DatasetID: dataset, DatasetCreatedAt: testCreatedAt, ViewVersion: revision, SchemaHash: hash, Follow: viewindex.FollowState{Cursor: viewindex.ChangeCursor{StoreID: "store", Sequence: 1}}}
}

func newTestService(engines map[string]viewindex.Engine) *Service {
	return &Service{engines: engines, views: make(map[datasetRef]*viewRuntime)}
}
