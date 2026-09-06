package bootstrap

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

// A fresh Strategy process recovers only modern instance/session state. Legacy
// owner rows are not part of startup reconciliation.
func TestInitializeStartsWithoutLegacyOwnerReconciliation(t *testing.T) {
	t.Setenv("MOOX_INSTANCE_ID", "strategy-test")
	t.Setenv("MOOX_NODE_ID", "strategy-node")
	t.Setenv("MOOX_BOOT_ID", "strategy-boot")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "test-access")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "test-secret")
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "test-v1")
	database := filepath.Join(t.TempDir(), "strategy.sqlite")
	repo, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve bootstrap test path")
	}
	trpcConfig, err := trpc.LoadConfig(filepath.Join(filepath.Dir(testFile), "../../config/trpc_go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	server, closeFn, err := Initialize(ctx, trpc.NewServerWithConfig(trpcConfig), Config{
		Database:   database,
		InstanceID: "strategy-test",
		EventBus:   EventBusConfig{RelayInterval: time.Second, ReconnectInterval: time.Second, RelayBatchSize: 1},
	})
	if err != nil {
		t.Fatalf("Initialize() failed with only a historical legacy row: %v", err)
	}
	if server == nil || closeFn == nil {
		t.Fatal("Initialize() returned incomplete server resources")
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
}
