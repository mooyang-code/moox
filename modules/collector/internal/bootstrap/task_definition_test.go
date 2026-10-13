package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/stretchr/testify/require"
)

func TestRefreshTaskDefinitionDropsRetiredParamsAndRehashes(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(schema.AllSQL()))
	repo := db.Tasks()
	ctx := context.Background()

	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "t1", TaskName: "t1", DataType: "kline", TagIDs: []string{"binance_spot"}, Enabled: true,
		CollectParams:   `{"target_dataset_id":"dataset_t1","frequency":"1m","history_policy":{"mode":"live_only","batch_bar_limit":1000}}`,
		DefinitionHash:  "stale-hash",
		ResultDatasetID: "dataset_t1", ResultViewID: "view_t1",
	}
	require.NoError(t, repo.Create(ctx, task))
	stored, err := repo.GetByTaskID(ctx, "crypto", "t1")
	require.NoError(t, err)

	require.NoError(t, refreshTaskDefinition(ctx, repo, stored))

	reloaded, err := repo.GetByTaskID(ctx, "crypto", "t1")
	require.NoError(t, err)
	require.NotContains(t, reloaded.CollectParams, "history_policy")
	require.Equal(t, collectsvc.CollectionTaskDefinitionHash(*reloaded), reloaded.DefinitionHash)
	require.NotEqual(t, "stale-hash", reloaded.DefinitionHash)
}
