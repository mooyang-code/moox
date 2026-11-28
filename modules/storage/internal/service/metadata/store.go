package metadata

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// DatasetQuery contains the server-side Dataset filters shared by the
// persistent store, dataset cache, and catalog aggregation paths.
type DatasetQuery struct {
	SpaceID      string
	DataSourceID string
	Freq         string
	DataKind     pb.DataKind
	Status       string
	Page         *pb.Page
}

// Reader 定义元数据存储的只读查询接口。
type Reader interface {
	GetSpace(ctx context.Context, spaceID string) (*pb.Space, error)
	ListSpaces(ctx context.Context, owner, status string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)

	GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error)
	ListDataSources(ctx context.Context, spaceID string, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error)

	GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error)
	ListSubjects(ctx context.Context, spaceID string, subjectIDs []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error)

	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasets(ctx context.Context, query DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)
	// SubjectsRevision 返回空间的标的修订号；标的、标签成员或数据集标的范围变化时递增，未变化过返回 0。
	SubjectsRevision(ctx context.Context, spaceID string) (uint64, error)

	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

// Writer 定义元数据存储的写入与状态变更接口。
type Writer interface {
	UpsertSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	// CreateSpace 原子地创建空间，已存在时返回冲突。
	CreateSpace(ctx context.Context, space *pb.Space) (*pb.Space, error)
	DeleteSpace(ctx context.Context, spaceID string) error
	UpsertDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	// CreateDataSource 原子地创建数据来源，已存在或同空间重名时返回冲突。
	CreateDataSource(ctx context.Context, item *pb.DataSource) (*pb.DataSource, error)
	DeleteDataSource(ctx context.Context, spaceID string, dataSourceID string) error
	UpsertSubject(ctx context.Context, item *pb.Subject) (*pb.Subject, error)
	UpsertDataset(ctx context.Context, item *pb.Dataset, columns ...*pb.DatasetColumn) (*pb.Dataset, error)
	// CreateDataset 原子地创建数据集及其初始列，已存在或同空间重名时返回冲突。
	CreateDataset(ctx context.Context, item *pb.Dataset, columns ...*pb.DatasetColumn) (*pb.Dataset, error)
	DeleteDataset(ctx context.Context, spaceID string, datasetID string) error
	UpsertDatasetColumn(ctx context.Context, item *pb.DatasetColumn) (*pb.DatasetColumn, error)
}

// Store 组合元数据读写能力与生命周期管理能力。
type Store interface {
	Close() error
	InitSchema(ctx context.Context) error
	Reader
	TagStore
	Writer
}
