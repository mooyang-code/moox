package metadata

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Reader interface {
	GetSpace(ctx context.Context, spaceID string) (*pb.Space, error)
	ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)

	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)

	GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error)
	ListDataSources(ctx context.Context, spaceID string, kind string, market string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error)

	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
	ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error)
	ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error)

	GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error)
	ListDataSets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.DataSet, *pb.PageResult, error)
	ListDataSetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetSubject, error)
	ListDataSetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error)

	GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error)
	ListFields(ctx context.Context, spaceID string, valueType pb.FieldValueType, page *pb.Page) ([]*pb.Field, *pb.PageResult, error)
	GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error)
	ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error)
	ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error)

	GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error)
	ListStorageNodes(ctx context.Context, page *pb.Page) ([]*pb.StorageNode, *pb.PageResult, error)
	GetDevice(ctx context.Context, deviceID string) (*pb.Device, error)
	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
	GetStorageRoute(ctx context.Context, spaceID string, routeID string) (*pb.StorageRoute, error)
	ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error)
	ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error)
}

type Writer interface {
	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
	UpsertViewColumn(ctx context.Context, item *pb.ViewColumn) (*pb.ViewColumn, error)
	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
	UpsertSubjectSymbol(ctx context.Context, item *pb.SubjectSymbol) (*pb.SubjectSymbol, error)
	UpsertDataSet(ctx context.Context, item *pb.DataSet) (*pb.DataSet, error)
	BindDataSetSubject(ctx context.Context, item *pb.DataSetSubject) (*pb.DataSetSubject, error)
	UpsertField(ctx context.Context, item *pb.Field) (*pb.Field, error)
	UpsertFactor(ctx context.Context, item *pb.Factor) (*pb.Factor, error)
	UpsertDataSetColumn(ctx context.Context, item *pb.DataSetColumn) (*pb.DataSetColumn, error)
	UpsertStorageNode(ctx context.Context, item *pb.StorageNode) (*pb.StorageNode, error)
	UpsertDevice(ctx context.Context, item *pb.Device) (*pb.Device, error)
	UpsertStorageRoute(ctx context.Context, item *pb.StorageRoute) (*pb.StorageRoute, error)
	RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
}

type Store interface {
	Close() error
	InitSchema(ctx context.Context) error
	TableNames(ctx context.Context) ([]string, error)
	Reader
	Writer
}
