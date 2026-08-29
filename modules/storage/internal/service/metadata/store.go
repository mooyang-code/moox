package metadata

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	ListDataSources(ctx context.Context, spaceID string, kind string, market string, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error)

	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
	ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error)
	ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error)

	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasets(ctx context.Context, query DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)

	GetFieldGroup(ctx context.Context, spaceID string, groupID string) (*pb.FieldGroup, error)
	ListFieldGroups(ctx context.Context, spaceID string, parentGroupID string, page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error)
	GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error)
	ListFields(ctx context.Context, query FieldQuery) ([]*pb.Field, *pb.PageResult, error)
	CountFieldsByGroup(ctx context.Context, spaceID string) (FieldGroupCounts, error)
	GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error)
	ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)

	GetDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error)
	ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error)
	GetDevice(ctx context.Context, deviceID string) (*pb.Device, error)
	ListDevices(ctx context.Context, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
	ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error)
}

// ViewRebuildLogReader is intentionally separate from Reader so the hot-path
// metadata snapshot/cache implementations do not have to model an append-only
// operational history stream.
type ViewRebuildLogReader interface {
	ListViewRebuildLogs(ctx context.Context, spaceID string, viewID string, result pb.ViewRebuildResult, page *pb.Page) ([]*pb.ViewRebuildLog, *pb.PageResult, error)
}

// ManualRebuildRevisionAttribute is persisted on a View when an operator asks
// for an asynchronous A/B rebuild. The marker is revision-scoped, so it stops
// being active automatically once that revision is activated.
const ManualRebuildRevisionAttribute = "moox.manual_rebuild_revision"

// Writer 定义元数据存储的写入与状态变更接口。
type Writer interface {
	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	DeleteSpace(ctx context.Context, spaceID string) error
	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
	ReplaceViewColumns(ctx context.Context, item *pb.View) (*pb.View, error)
	RequestViewRebuild(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error)
	ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error)
	UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error)
	ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error)
	FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error)
	CreateViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error)
	UpdateViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error)
	UpsertSkippedViewRebuildLog(ctx context.Context, item *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error)
	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	DeleteDataSource(ctx context.Context, spaceID string, dataSourceID string) error
	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
	UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error)
	RegisterDataSubject(ctx context.Context, subject *pb.Subject, symbol *pb.SubjectSymbol, bindings []*pb.DatasetSubject) (*pb.Subject, []*pb.DatasetSubject, error)
	UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error)
	DeleteDataset(ctx context.Context, spaceID string, datasetID string) error
	BindDatasetSubject(ctx context.Context, item *pb.DatasetSubject) (*pb.DatasetSubject, error)
	UpsertFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	CreateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	UpdateFieldGroup(ctx context.Context, item *pb.FieldGroup) (*pb.FieldGroup, error)
	UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	CreateField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	UpdateField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	BatchUpdateFields(ctx context.Context, spaceID string, fieldIDs []string, targetGroupID string, targetStatus string) (uint32, error)
	DeleteFieldGroup(ctx context.Context, spaceID string, groupID string) error
	UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error)
	UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error)
	RegisterDataNode(ctx context.Context, nodeID string, serviceTarget string, initialName string) (*pb.DataNode, error)
	UpdateDataNode(ctx context.Context, nodeID string, name string, status string) (*pb.DataNode, error)
	DeleteDataNode(ctx context.Context, nodeID string) error
	RebindDatasetDataNode(ctx context.Context, spaceID string, datasetID string, dataNodeID string, expectedRevision uint64) (*pb.Dataset, error)
	CommitDatasetActivation(ctx context.Context, spaceID string, datasetID string, expectedRevision uint64) (*pb.Dataset, error)
	UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
	RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
}

// DatasetSubjectSetWriter is the atomic publication contract for complete
// instrument snapshots. Implementations must keep staged rows out of the
// active DatasetSubject reader until activation commits.
type DatasetSubjectSetWriter interface {
	StageDatasetSubjectSet(ctx context.Context, spaceID, setID string, bindings []*pb.DatasetSubject) (int, error)
	ActivateDatasetSubjectSet(ctx context.Context, spaceID, setID string) (int, error)
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
	ViewRebuildLogReader
	Writer
	ViewPeriodStateStore
}
