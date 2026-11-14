package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestHostSilenceSurvivesMonitorRestartAndIsolatesSameNamedHosts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "monitor.db")
	startedAt := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)

	first, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	registry, err := store.WithDatabase(first, hostmetrics.NewRegistry)
	if err != nil {
		t.Fatal(err)
	}
	for _, observation := range []hostmetrics.HostObservation{
		{AgentID: "agent-a", Hostname: "worker", BootID: "boot-a", EventID: "event-a", OccurredAt: startedAt},
		{AgentID: "agent-b", Hostname: "worker", BootID: "boot-b", EventID: "event-b", OccurredAt: startedAt.Add(90 * time.Second)},
	} {
		if _, err := registry.Observe(ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartedRegistry, err := store.WithDatabase(restarted, hostmetrics.NewRegistry)
	if err != nil {
		t.Fatal(err)
	}
	// 重启后只凭库里的最近上报时间判断：agent-a 已超过停报时限，同名主机上的 agent-b 不受影响。
	scanAt := startedAt.Add(hostmetrics.DefaultHostStaleAfter + time.Second)
	agents, err := restartedRegistry.List(ctx, scanAt)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]bool{}
	for _, agent := range agents {
		status[agent.AgentID] = agent.Reachable
	}
	if status["agent-a"] || !status["agent-b"] {
		t.Fatalf("reachable status=%v", status)
	}
}
