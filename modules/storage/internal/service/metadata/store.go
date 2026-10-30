package metadata

import (
	"context"
	"errors"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var (
	ErrViewExists                  = errors.New("view already exists")
	ErrViewSchemaExtensionConflict = errors.New("view schema extension conflict")
)

// FieldQuery contains the supported server-side field filters and ordering.
type FieldQuery struct {
	SpaceID            string
	GroupID            string
	ValueType          pb.FieldValueType
	Status             string
	Keyword            string
	IncludeDescendants bool
	UngroupedOnly      bool
	SortBy             string
	SortOrder          string
	Page               *pb.Page
}

// FieldGroupCounts contains direct child counts and recursive root counts.
type FieldGroupCounts struct {
	ByGroup   map[string]uint64
	Total     uint64
	Ungrouped uint64
}

// DatasetQuery contains the server-side Dataset filters shared by the
// persistent store, snapshot cache, and catalog aggregation paths.
type DatasetQuery struct {
	SpaceID      string
	DataSourceID string
	DataNodeID   string
	DataNodeIDs  []string
	Freq         string
	DataKind     pb.DataKind
	Page         *pb.Page
}

// SnapshotReader is the request-scoped metadata surface used by validators.
// Implementations must expose one immutable cache generation.
type SnapshotReader interface {
	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

// RequestSnapshot is the immutable metadata generation shared by validation
// and direct Dataset -> DataNode resolution for one request.
type RequestSnapshot interface {
	GetDataset(spaceID string, datasetID string) (*pb.Dataset, bool)
	GetDataNode(nodeID string) (*pb.DataNode, bool)
	ListDatasetColumns(spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

type requestSnapshotContextKey struct{}

func WithRequestSnapshot(ctx context.Context, snapshot RequestSnapshot) context.Context {
	return context.WithValue(ctx, requestSnapshotContextKey{}, snapshot)
}

func RequestSnapshotFromContext(ctx context.Context) RequestSnapshot {
	if snapshot, ok := ctx.Value(requestSnapshotContextKey{}).(RequestSnapshot); ok {
		return snapshot
	}
	return nil
}

// Reader 定义元数据存储的只读查询接口。
type Reader interface {
	GetSpace(ctx context.Context, spaceID string) (*pb.Space, error)
	ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)

	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
	ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)

	GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error)
	ListDataSources(ctx context.Context, spaceID string, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error)

	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
	ListSubjects(ctx context.Context, spaceID string, subjectIDs []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error)

	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasets(ctx context.Context, query DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)
	// SubjectsRevision 返回空间的标的修订号；标的、标签成员或数据集标的范围变化时递增，未变化过返回 0。
	SubjectsRevision(ctx context.Context, spaceID string) (uint64, error)

	GetFieldGroup(ctx context.Context, spaceID string, groupID string) (*pb.FieldGroup, error)
	ListFieldGroups(ctx context.Context, spaceID string, parentGroupID string, page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error)
	GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error)
	ListFields(ctx context.Context, query FieldQuery) ([]*pb.Field, *pb.PageResult, error)
	CountFieldsByGroup(ctx context.Context, spaceID string) (FieldGroupCounts, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)

	GetDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error)
	ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error)
	GetDevice(ctx context.Context, deviceID string) (*pb.Device, error)
	ListDevices(ctx context.Context, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
}

// IndexGenerationAttribute 记录 View 当前索引的代次，首次建成时写入。索引标识对一个 View 是固定的，不能区分代次；
// View 重建（删除后重新建索引）时代次变化，调用方（例如策略回放）据此判断旧的行是否已不存在。
const IndexGenerationAttribute = "moox.index_generation"

// Writer 定义元数据存储的写入与状态变更接口。
type Writer interface {
	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	// CreateSpace 原子地创建空间，已存在时返回冲突。
	CreateSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	DeleteSpace(ctx context.Context, spaceID string) error
	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
	CreateView(ctx context.Context, item *pb.View) (*pb.View, error)
	ReplaceViewColumns(ctx context.Context, item *pb.View) (*pb.View, error)
	DeleteView(ctx context.Context, spaceID string, viewID string) error
	UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error)
	CompleteViewInitialBuild(ctx context.Context, req *pb.CompleteViewInitialBuildReq) (*pb.View, error)
	CommitViewSchemaExtension(ctx context.Context, req *pb.CommitViewSchemaExtensionReq) (*pb.View, error)
	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	DeleteDataSource(ctx context.Context, spaceID string, dataSourceID string) error
	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
	UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error)
	DeleteDataset(ctx context.Context, spaceID string, datasetID string) error
	UpsertFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	CreateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	UpdateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	CreateField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	UpdateField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	BatchUpdateFields(ctx context.Context, spaceID string, fieldIDs []string, targetGroupID string, targetStatus string) (uint32, error)
	DeleteFieldGroup(ctx context.Context, spaceID string, groupID string) error
	UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error)
	RegisterDataNode(ctx context.Context, nodeID string, serviceTarget string, initialName string) (*pb.DataNode, error)
	UpdateDataNode(ctx context.Context, nodeID string, name string, status string) (*pb.DataNode, error)
	DeleteDataNode(ctx context.Context, nodeID string) error
	RebindDatasetDataNode(ctx context.Context, spaceID string, datasetID string, dataNodeID string, expectedRevision uint64) (*pb.Dataset, error)
	CommitDatasetActivation(ctx context.Context, spaceID string, datasetID string, expectedRevision uint64) (*pb.Dataset, error)
	UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
}

type ViewPeriodStateStore interface {
	ListViewPeriodDatasetStates(ctx context.Context, spaceID, viewID, frequency string, periodTime int64) ([]*pb.ViewPeriodDatasetState, error)
	MissingViewSyncPointDatasets(ctx context.Context, spaceID, viewID, requestID string, datasetIDs []string) ([]string, error)
	UpsertViewPeriodDatasetState(ctx context.Context, item *pb.ViewPeriodDatasetState) (*pb.ViewPeriodDatasetState, error)
	RecordViewSyncPoint(ctx context.Context, item *pb.ViewSyncPoint) (*pb.ViewSyncPoint, error)
	DeleteViewPeriodDatasetStatesBefore(ctx context.Context, before time.Time) (int64, error)
	DeleteViewSyncPointsBefore(ctx context.Context, before time.Time) (int64, error)
}

// Store 组合元数据读写能力与生命周期管理能力。
type Store interface {
	Close() error
	InitSchema(ctx context.Context) error
	TableNames(ctx context.Context) ([]string, error)
	Reader
	TagStore
	Writer
	ViewPeriodStateStore
}
