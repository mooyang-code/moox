package archive

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Metadata defines the metadata operations required by the archive runtime.
type Metadata interface {
	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)
	ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error)
	RegisterArchiveFile(ctx context.Context, item *pb.ArchiveFile) (*pb.ArchiveFile, error)
}
