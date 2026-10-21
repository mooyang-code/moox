package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestIngestModesInitializeOnceAndSwitch(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	repo := db.IngestModes()
	ctx := context.Background()
	require.NoError(t, repo.Initialize(ctx, []IngestMode{{SpaceID: "crypto", DatasetID: "a", Mode: IngestPull, Executor: ExecutorSCF}}))
	require.NoError(t, repo.Initialize(ctx, []IngestMode{{SpaceID: "crypto", DatasetID: "a", Mode: IngestStream, Executor: ExecutorLocal}, {SpaceID: "crypto", DatasetID: "b", Mode: IngestStream, Executor: ExecutorLocal}}))
	modes, err := repo.List(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, IngestPull, modes["a"].Mode, "初值只写一次，之后以库为准")
	require.Equal(t, IngestStream, modes["b"].Mode)
	require.NoError(t, repo.Set(ctx, []IngestMode{{SpaceID: "crypto", DatasetID: "a", Mode: IngestStream, Executor: ExecutorLocal, Force: true}}))
	modes, _ = repo.List(ctx, "crypto")
	require.Equal(t, IngestMode{SpaceID: "crypto", DatasetID: "a", Mode: IngestStream, Executor: ExecutorLocal, Force: true, UpdatedAt: modes["a"].UpdatedAt}, modes["a"])
	require.NoError(t, repo.ClearForce(ctx, "crypto", nil))
	modes, _ = repo.List(ctx, "crypto")
	require.False(t, modes["a"].Force)
	require.Error(t, repo.Set(ctx, []IngestMode{{SpaceID: "crypto", DatasetID: "a", Mode: "ws", Executor: ExecutorSCF}}))

	workers := db.StreamWorkers()
	worker, err := workers.Get(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, worker)
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, workers.Heartbeat(ctx, StreamWorker{SpaceID: "crypto", InstanceID: "i1", Applied: `{"id":1}`, Status: "{}", HeartbeatAt: at}))
	require.NoError(t, workers.Heartbeat(ctx, StreamWorker{SpaceID: "crypto", InstanceID: "i2", Applied: `{"id":2}`, Status: "{}", HeartbeatAt: at.Add(time.Second)}))
	worker, err = workers.Get(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "i2", worker.InstanceID)
	require.Equal(t, `{"id":2}`, worker.Applied)
}
