package builder

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// MetadataReader exposes the view definitions required by event projection.
type MetadataReader interface {
	ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
}

// Options controls the storage view builder service.
type Options struct {
	Events     eventbus.Subscriber
	Reader     FactReader
	Metadata   MetadataReader
	Engines    map[string]viewindex.ViewIndexEngine
	BatchSize  int
	BatchWait  time.Duration
	MaxWorkers int
}

// BatchOptions controls batch aggregation.
type BatchOptions struct {
	BatchSize int
	BatchWait time.Duration
}

func viewIndexBatch(item *pb.View, columns []*pb.ViewColumn, timeRows []*pb.TimeSeriesRow, recordRows []*pb.RecordRow, warming bool) viewindex.ViewIndexBatch {
	version := item.GetActiveViewVersion()
	schemaHash := item.GetActiveSchemaHash()
	if warming && item.GetIndexBuild() != nil {
		version = item.GetIndexBuild().GetTargetViewVersion()
		schemaHash = item.GetIndexBuild().GetSchemaHash()
	}
	if schemaHash == "" {
		schemaHash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{
			SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), Engine: item.GetEngine(), Columns: columns,
		})
	}
	return viewindex.ViewIndexBatch{
		TimeSeriesRows: timeRows, RecordRows: recordRows, Columns: columns,
		ViewVersion: version, SchemaHash: schemaHash,
	}
}

func normalizeBatchOptions(opts BatchOptions) BatchOptions {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.BatchWait <= 0 {
		opts.BatchWait = 200 * time.Millisecond
	}
	return opts
}
