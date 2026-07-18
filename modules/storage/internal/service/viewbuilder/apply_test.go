package builder

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type checkpointTestEngine struct {
	checkpoint uint64
	applied    bool
}

func (e *checkpointTestEngine) Engine() string { return "test" }
func (e *checkpointTestEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}
func (e *checkpointTestEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{ShardCheckpoints: map[string]uint64{"shard-a": e.checkpoint}}, nil
}
func (e *checkpointTestEngine) Remove(context.Context, string) error { return nil }
func (e *checkpointTestEngine) Apply(context.Context, string, viewindex.ViewIndexApplyBatch) error {
	e.applied = true
	return nil
}

func TestApplyViewIndexRejectsGapFromDurableCheckpoint(t *testing.T) {
	engine := &checkpointTestEngine{}
	batch := viewindex.BatchWrite{
		TimeSeriesRows: []*pb.TimeSeriesRow{{Key: &pb.TimeSeriesKey{SpaceId: "space", DatasetId: "dataset", SubjectId: "subject", DataTime: "v1"}}},
	}
	err := applyViewIndexWithMode(context.Background(), engine, "index", batch, nil, nil, applyProgress{shardID: "shard-a", sequence: 2}, false)
	if err == nil {
		t.Fatal("applyViewIndexWithMode accepted a sequence gap from durable checkpoint 0")
	}
	if engine.applied {
		t.Fatal("applyViewIndexWithMode applied a batch after rejecting the sequence gap")
	}
}
