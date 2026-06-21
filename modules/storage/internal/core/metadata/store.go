package metadata

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Reader 定义元数据存储的只读查询接口。
type Reader interface {
	GetSpace(ctx context.Context, spaceID string) (*pb.Space, error)
	ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)

	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
	ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)

	GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error)
	ListDataSources(ctx context.Context, spaceID string, kind string, market string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error)

	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
	ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error)
	ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error)

	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)

	GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error)
	ListFields(ctx context.Context, spaceID string, valueType pb.FieldValueType, page *pb.Page) ([]*pb.Field, *pb.PageResult, error)
	GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error)
	ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)

	GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error)
	ListPrimaryStoreNodes(ctx context.Context, page *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error)
	GetDevice(ctx context.Context, deviceID string) (*pb.Device, error)
	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
	GetPrimaryStoreRoute(ctx context.Context, spaceID string, routeID string) (*pb.PrimaryStoreRoute, error)
	ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error)
	ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error)
}

// Writer 定义元数据存储的写入与状态变更接口。
type Writer interface {
	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
	UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error)
	BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error)
	CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error
	FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error
	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
	UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error)
	UpsertDataset(ctx context.Context, item *pb.Dataset) (*pb.Dataset, error)
	BindDatasetSubject(ctx context.Context, item *pb.DatasetSubject) (*pb.DatasetSubject, error)
	UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error)
	UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error)
	UpsertPrimaryStoreNode(ctx context.Context, item *pb.PrimaryStoreNode) (*pb.PrimaryStoreNode, error)
	UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
	UpsertPrimaryStoreRoute(ctx context.Context, item *pb.PrimaryStoreRoute) (*pb.PrimaryStoreRoute, error)
	RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
}

// Store 组合元数据读写能力与生命周期管理能力。
type Store interface {
	Close() error
	InitSchema(ctx context.Context) error
	TableNames(ctx context.Context) ([]string, error)
	Reader
	Writer
}
