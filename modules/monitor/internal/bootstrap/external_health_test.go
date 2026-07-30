package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestExternalHealthRouteUsesFixedAllowlistAndPersistsResult(t *testing.T) {
	dbConfig := config.Default().Database
	dbConfig.Path = filepath.Join(t.TempDir(), "monitor.db")
	manager, err := store.OpenFromConfig(dbConfig)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	require.NoError(t, registerExternalSentinelChecks(context.Background(), repos))

	now := time.Now().UTC()
	route := externalHealthRoute(repos, nil)
	require.NoError(t, route(context.Background(), &eventpb.EventMessage{SpaceId: "crypto"}, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "monitor_ready", Success: true,
		StatusCode: 200, LatencyMs: 12, CheckedAt: timestamppb.New(now),
	}))
	results, err := repos.Results.Recent(context.Background(), "crypto", externalCheckID(externalSentinelObserver, "monitor_ready"), 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Success)
	require.Equal(t, "scf-node-a", results[0].InstanceID)
	require.NoError(t, route(context.Background(), &eventpb.EventMessage{SpaceId: "crypto"}, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "monitor_ready", Success: true,
		StatusCode: 200, LatencyMs: 12, CheckedAt: timestamppb.New(now),
	}))
	results, err = repos.Results.Recent(context.Background(), "crypto", externalCheckID(externalSentinelObserver, "monitor_ready"), 10)
	require.NoError(t, err)
	require.Len(t, results, 1)

	err = route(context.Background(), &eventpb.EventMessage{SpaceId: "crypto"}, &observabilitypb.HealthCheckReport{
		ObserverId: "unknown", CheckId: "monitor_ready", CheckedAt: timestamppb.New(now),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown external observer")

	err = route(context.Background(), &eventpb.EventMessage{SpaceId: "crypto"}, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, CheckId: "monitor_ready", CheckedAt: timestamppb.New(now),
	})
	require.ErrorContains(t, err, "node_id is required")
}

func TestExternalHealthRouteStoresBacklogWithoutRegressingCurrentState(t *testing.T) {
	dbConfig := config.Default().Database
	dbConfig.Path = filepath.Join(t.TempDir(), "monitor.db")
	manager, err := store.OpenFromConfig(dbConfig)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	require.NoError(t, registerExternalSentinelChecks(context.Background(), repos))
	route := externalHealthRoute(repos, nil)
	now := time.Now().UTC()
	checkID := externalCheckID(externalSentinelObserver, "monitor_ready")
	message := &eventpb.EventMessage{SpaceId: "crypto"}

	require.NoError(t, route(context.Background(), message, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "monitor_ready", Success: false,
		ErrorSummary: "historical outage", CheckedAt: timestamppb.New(now.Add(-2 * time.Minute)),
	}))
	check, err := repos.Checks.Get(context.Background(), "crypto", checkID)
	require.NoError(t, err)
	require.Nil(t, check.LastCheckedAt)

	require.NoError(t, route(context.Background(), message, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "monitor_ready", Success: true,
		StatusCode: 200, CheckedAt: timestamppb.New(now),
	}))
	check, err = repos.Checks.Get(context.Background(), "crypto", checkID)
	require.NoError(t, err)
	require.NotNil(t, check.LastCheckedAt)
	require.WithinDuration(t, now, *check.LastCheckedAt, time.Millisecond)

	require.NoError(t, route(context.Background(), message, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "monitor_ready", Success: false,
		ErrorSummary: "out-of-order failure", CheckedAt: timestamppb.New(now.Add(-10 * time.Second)),
	}))
	check, err = repos.Checks.Get(context.Background(), "crypto", checkID)
	require.NoError(t, err)
	require.WithinDuration(t, now, *check.LastCheckedAt, time.Millisecond)
	results, err := repos.Results.Recent(context.Background(), "crypto", checkID, 10)
	require.NoError(t, err)
	require.Len(t, results, 3)
}

func TestExternalHealthRoutePreservesStableErrorCodeAndSafeDiagnostic(t *testing.T) {
	dbConfig := config.Default().Database
	dbConfig.Path = filepath.Join(t.TempDir(), "monitor.db")
	manager, err := store.OpenFromConfig(dbConfig)
	require.NoError(t, err)
	t.Cleanup(func() { _ = manager.Close() })
	require.NoError(t, manager.ApplySchema(schema.SQL()))
	repos := manager.Repositories()
	require.NoError(t, registerExternalSentinelChecks(context.Background(), repos))

	now := time.Now().UTC()
	route := externalHealthRoute(repos, nil)
	require.NoError(t, route(context.Background(), &eventpb.EventMessage{SpaceId: "crypto"}, &observabilitypb.HealthCheckReport{
		ObserverId: externalSentinelObserver, NodeId: "scf-node-a", CheckId: "market_canary", Kind: "storage",
		Target: "crypto/binance_spot_kline_1h/BTC-USDT/1H", Success: false,
		ErrorCode:    "insufficient_closed_bars",
		ErrorSummary: "Storage 查询只返回 0 根已收盘 K 线，至少需要 2 根",
		LatencyMs:    23, CheckedAt: timestamppb.New(now),
	}))

	results, err := repos.Results.Recent(context.Background(), "crypto", externalCheckID(externalSentinelObserver, "market_canary"), 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "insufficient_closed_bars", results[0].ErrorMessage)
	require.Equal(t,
		"查询范围：crypto/binance_spot_kline_1h/BTC-USDT/1H；SCF 上报：Storage 查询只返回 0 根已收盘 K 线，至少需要 2 根",
		results[0].BodyExcerpt,
	)
}
