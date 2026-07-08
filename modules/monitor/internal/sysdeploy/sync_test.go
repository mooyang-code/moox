package sysdeploy

import (
	"context"
	"path/filepath"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func TestSyncDeploymentsCreatesSystemChecks(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := repository.NewCheckRepository(mgr.DB())
	syncer := NewSyncer(checks, nil)

	n, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{ServiceName: "moox_cloudnode", Protocol: "http", Host: "127.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:11411/healthz","monitor_enabled":true}`},
		{ServiceName: "moox_collector", Protocol: "http", Host: "127.0.0.1", Port: 11402, Status: "active", ExtraConfig: `{}`},
		{ServiceName: "inactive", Protocol: "http", Host: "127.0.0.1", Port: 1, Status: "inactive", ExtraConfig: `{}`},
	})
	if err != nil {
		t.Fatalf("SyncDeployments: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced = %d, want 2", n)
	}
	httpCheck, err := checks.Get(ctx, "", "moox_cloudnode")
	if err != nil {
		t.Fatalf("get cloudnode check: %v", err)
	}
	if httpCheck.Kind != domain.CheckKindHTTP || httpCheck.URL != "http://127.0.0.1:11411/healthz" || httpCheck.BodyContains != `"ready":true` {
		t.Fatalf("http check = %+v", httpCheck)
	}
	tcpCheck, err := checks.Get(ctx, "", "moox_collector")
	if err != nil {
		t.Fatalf("get collector check: %v", err)
	}
	if tcpCheck.Kind != domain.CheckKindTCP || tcpCheck.TCPHost != "127.0.0.1" || tcpCheck.TCPPort != 11402 {
		t.Fatalf("tcp check = %+v", tcpCheck)
	}
}

func TestSyncDeploymentsDoesNotTouchManualCheck(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := repository.NewCheckRepository(mgr.DB())
	if err := checks.Create(ctx, &domain.Check{
		SpaceID:         "",
		CheckID:         "moox_cloudnode",
		Name:            "Manual",
		Kind:            domain.CheckKindHTTP,
		URL:             "http://manual",
		Method:          "GET",
		Headers:         "{}",
		Labels:          "{}",
		IntervalSeconds: 60,
		TimeoutMS:       3000,
		ExpectedStatus:  "200-299",
		Enabled:         true,
		Source:          domain.CheckSourceManual,
	}); err != nil {
		t.Fatalf("create manual check: %v", err)
	}

	syncer := NewSyncer(checks, nil)
	_, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{ServiceName: "moox_cloudnode", Protocol: "http", Host: "127.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:11411/healthz"}`},
	})
	if err != nil {
		t.Fatalf("SyncDeployments: %v", err)
	}
	got, err := checks.Get(ctx, "", "moox_cloudnode")
	if err != nil {
		t.Fatalf("get check: %v", err)
	}
	if got.Source != domain.CheckSourceManual || got.URL != "http://manual" {
		t.Fatalf("manual check was modified: %+v", got)
	}
}

func TestSyncKeepsExistingChecksWhenAdminFails(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := repository.NewCheckRepository(mgr.DB())
	syncer := NewSyncer(checks, failingSource{})
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{ServiceName: "moox_cloudnode", Protocol: "http", Host: "127.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:11411/healthz"}`},
	}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if _, err := syncer.Sync(ctx); err == nil {
		t.Fatal("Sync() error = nil, want admin failure")
	}
	got, err := checks.Get(ctx, "", "moox_cloudnode")
	if err != nil {
		t.Fatalf("get existing check: %v", err)
	}
	if !got.Enabled {
		t.Fatalf("existing check disabled after admin failure")
	}
}

type failingSource struct{}

func (failingSource) ActiveDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return nil, errAdminUnavailable
}

func openSyncDB(t *testing.T) *monstorage.Manager {
	t.Helper()
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return mgr
}
