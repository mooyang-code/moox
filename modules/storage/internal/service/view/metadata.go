package view

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	ClaimViewIndexBuild(ctx context.Context, req *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error)
	UpdateViewIndexBuild(ctx context.Context, req *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error)
	ActivateViewIndex(ctx context.Context, req *pb.ActivateViewIndexReq) (*pb.View, error)
	FailViewIndexBuild(ctx context.Context, req *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error)
}
