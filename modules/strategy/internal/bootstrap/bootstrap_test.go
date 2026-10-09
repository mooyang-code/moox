package bootstrap

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/prometheus/client_golang/prometheus"
	_ "trpc.group/trpc-go/trpc-filter/recovery"
	_ "trpc.group/trpc-go/trpc-filter/validation"
	trpc "trpc.group/trpc-go/trpc-go"
	_ "trpc.group/trpc-go/trpc-metrics-prometheus"
)

// 没有接线 Factor 与 Storage 的进程也能启动（只管理定义），并把遗留的 running 回放标记为 failed(interrupted)。
func TestInitializeWithoutDependenciesMarksInterruptedReplays(t *testing.T) {
	t.Setenv("MOOX_INSTANCE_ID", "strategy-test")
	t.Setenv("MOOX_NODE_ID", "strategy-node")
	t.Setenv("MOOX_BOOT_ID", "strategy-boot")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "test-access")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "test-secret")
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "test-v1")
	// 进程级默认注册表在多次初始化之间需要干净。
	previous := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = prometheus.NewRegistry()
	t.Cleanup(func() { prometheus.DefaultRegisterer = previous })
	database := filepath.Join(t.TempDir(), "strategy.sqlite")
	repo, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := repo.CreateReplay(ctx, store.Replay{ReplayID: "p1", DSLYaml: "name: demo", DSLHash: "sha256:demo", ViewGeneration: "idx", SpaceID: "crypto", ViewID: "view_a", StartTime: seedTime, EndTime: seedTime.Add(time.Hour), CreatedAt: seedTime}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.ClaimNextReplay(ctx, seedTime); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件")
	}
	trpcConfig, err := trpc.LoadConfig(filepath.Join(filepath.Dir(testFile), "../../config/trpc_go.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	server, closeFn, err := Initialize(ctx, trpc.NewServerWithConfig(trpcConfig), Config{
		Database: database, InstanceID: "strategy-test",
		EventBus:  EventBusConfig{RelayInterval: time.Second, ReconnectInterval: time.Second, RelayBatchSize: 1},
		Replay:    ReplayConfig{ChunkBars: 10, MissingPriceLiquidateBars: 3},
		Retention: RetentionConfig{ResultItemsDays: 90, ReplaysDays: 90},
	})
	if err != nil {
		t.Fatalf("Initialize() 失败：%v", err)
	}
	if server == nil || closeFn == nil {
		t.Fatal("Initialize() 返回的资源不完整")
	}
	if err := closeFn(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	interrupted, err := reopened.GetReplay(ctx, "p1")
	if err != nil || interrupted.Status != store.ReplayFailed || interrupted.Error != "interrupted" {
		t.Fatalf("遗留的 running 回放应标记为 failed(interrupted)：%+v err=%v", interrupted, err)
	}
}
