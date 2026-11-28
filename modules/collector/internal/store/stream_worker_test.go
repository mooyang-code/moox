package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestStreamWorkerHeartbeatKeepsOneRowPerSpace(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	ctx := context.Background()
	workers := db.StreamWorkers()
	worker, err := workers.Get(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, worker)
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	require.NoError(t, workers.Heartbeat(ctx, StreamWorker{SpaceID: "crypto", InstanceID: "i1", AppliedPlanID: 1, Status: "{}", HeartbeatAt: at}))
	require.NoError(t, workers.Heartbeat(ctx, StreamWorker{SpaceID: "crypto", InstanceID: "i2", AppliedPlanID: 2, Status: "{}", HeartbeatAt: at.Add(time.Second)}))
	worker, err = workers.Get(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "i2", worker.InstanceID)
	require.EqualValues(t, 2, worker.AppliedPlanID)
}
