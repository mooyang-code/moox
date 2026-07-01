package builder

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/eventbus"
	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// TimeSeriesViewWriter writes projected time-series view rows.
type TimeSeriesViewWriter interface {
	InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error
}

// RecordViewIndexer indexes projected record view rows.
type RecordViewIndexer interface {
	IndexRecordViewRows(ctx context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error
}

// Options controls the storage view builder service.
type Options struct {
	Events     eventbus.Bus
	Reader     FactReader
	Metadata   viewsvc.Metadata
	Views      TimeSeriesViewWriter
	Search     RecordViewIndexer
	BatchSize  int
	BatchWait  time.Duration
	MaxWorkers int
}

// BatchOptions controls batch aggregation.
type BatchOptions struct {
	BatchSize int
	BatchWait time.Duration
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
