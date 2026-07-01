package view

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Metadata defines the metadata operations required by View query, rebuild,
// scheduling, and incremental materialization.
type Metadata interface {
	GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error)
	ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error)
	ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
	ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error)
	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	UpsertView(ctx context.Context, item *pb.View) (*pb.View, error)
	BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error)
	CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error
	FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error
}
