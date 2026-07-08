package testkit

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// FakeStorage is a deterministic StorageIO implementation for scheduler load tests.
type FakeStorage struct {
	Latency time.Duration
	Reads   atomic.Int64
	Writes  atomic.Int64
}

func (s *FakeStorage) ReadWindow(context.Context, storageio.WindowKey, int, time.Time, []string) (*engine.DataFrame, error) {
	s.Reads.Add(1)
	if s.Latency > 0 {
		time.Sleep(s.Latency)
	}
	now := time.Now().UTC()
	return &engine.DataFrame{
		Columns:   storageio.KLineColumns,
		DataTimes: []time.Time{now.Add(-time.Minute), now},
		Rows: [][]any{
			{now.Add(-time.Minute), 1.0, 2.0, 0.8, 1.6, 100.0, 160.0, int64(10)},
			{now, 1.6, 2.2, 1.2, 2.0, 120.0, 240.0, int64(12)},
		},
	}, nil
}

func (s *FakeStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.DataFrame, *engine.FactorResult) error {
	s.Writes.Add(1)
	return nil
}
