package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/packages/pyruntime/snapshot"
	"github.com/mooyang-code/moox/packages/pyruntime/transport"
)

// SnapshotStore materializes one immutable input window per parent task. The
// same handle is passed to every batch and released after aggregation.
type SnapshotStore struct{ store *snapshot.Store }

func NewSnapshotStore(root string) *SnapshotStore {
	return &SnapshotStore{store: snapshot.NewStore(root)}
}

func (s *SnapshotStore) AcquireFrame(ctx context.Context, task engine.FactorTask, frame *engine.DataFrame) (*snapshot.Handle, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	if frame == nil {
		return nil, fmt.Errorf("snapshot frame is nil")
	}
	if len(frame.Rows) != len(frame.DataTimes) {
		return nil, fmt.Errorf("snapshot rows=%d times=%d mismatch", len(frame.Rows), len(frame.DataTimes))
	}
	rows := make([][]any, len(frame.Rows))
	for i, row := range frame.Rows {
		if len(row) != len(frame.Columns) {
			return nil, fmt.Errorf("snapshot row %d columns=%d expected=%d", i, len(row), len(frame.Columns))
		}
		rows[i] = append([]any(nil), row...)
	}
	table := transport.Table{Columns: append([]string(nil), frame.Columns...), Rows: rows}
	key := snapshot.Key{Namespace: task.SubjectID + "/" + task.Freq, DataRevision: task.TaskID, SchemaHash: fmt.Sprint(frame.Columns), InputHash: fmt.Sprint(frame.DataTimes)}
	return s.store.AcquireArrow(ctx, key, table)
}

func (s *SnapshotStore) Reap() error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Reap()
}

func SnapshotComputedAt() string { return time.Now().UTC().Format(time.RFC3339Nano) }
