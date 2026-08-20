package sysdeploy

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestSyncDeploymentsCreatesSystemChecks(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)

	n, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{NodeId: "node-a", ServiceName: "moox_cloudnode", Protocol: "http", Host: "10.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11411/readyz","health_kind":"readiness","monitor_enabled":true}`},
		{NodeId: "node-a", ServiceName: "moox_collector", Protocol: "http", Host: "10.0.0.1", Port: 11402, Status: "active", ExtraConfig: `{}`},
		{ServiceName: "inactive", Protocol: "http", Host: "127.0.0.1", Port: 1, Status: "inactive", ExtraConfig: `{}`},
	})
	if err != nil {
		t.Fatalf("SyncDeployments: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced = %d, want 2", n)
	}
	httpCheck, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil {
		t.Fatalf("get cloudnode check: %v", err)
	}
	if httpCheck.Kind != domain.CheckKindHTTP || httpCheck.URL != "http://10.0.0.1:11411/readyz" || httpCheck.BodyContains != `"ready":true` {
		t.Fatalf("http check = %+v", httpCheck)
	}
	tcpCheck, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_collector")
	if err != nil {
		t.Fatalf("get collector check: %v", err)
	}
	if tcpCheck.Kind != domain.CheckKindTCP || tcpCheck.TCPHost != "10.0.0.1" || tcpCheck.TCPPort != 11402 {
		t.Fatalf("tcp check = %+v", tcpCheck)
	}
}

func TestSyncDeploymentsKeepsValidRowsWhenAnotherDefinitionIsInvalid(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)

	// A remote deployment may carry a node-local health URL. It is invalid
	// from the control node, but must not abort registration of other services.
	n, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{NodeId: "compute-1", ServiceName: "storage-primary", Protocol: "http", Host: "127.0.0.1", Port: 20200, Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:20210/readyz"}`},
		{NodeId: "control", ServiceName: "moox_monitor", Protocol: "http", Host: "127.0.0.1", Port: 11410, Status: "active", ExtraConfig: `{"health_url":"http://127.0.0.1:11409/readyz"}`},
	})
	if err != nil {
		t.Fatalf("SyncDeployments: %v", err)
	}
	if n != 1 {
		t.Fatalf("synced = %d, want one valid service", n)
	}
	if _, err := checks.Get(ctx, "", "sysdeploy:control:moox_monitor"); err != nil {
		t.Fatalf("valid service check was not created: %v", err)
	}
}

func TestSyncDeploymentsKeepsExistingCheckForInvalidActiveDefinition(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)
	valid := &adminpb.ServiceDeployment{
		NodeId: "node-a", ServiceName: "moox_monitor", Protocol: "http", Host: "10.0.0.1", Port: 11410,
		Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11409/readyz"}`,
	}
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{valid}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	invalid := *valid
	invalid.ExtraConfig = `{"health_url":"http://127.0.0.1:11409/readyz"}`
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{&invalid}); err == nil {
		t.Fatal("invalid metadata sync error = nil")
	}
	check, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_monitor")
	if err != nil {
		t.Fatalf("get check: %v", err)
	}
	if !check.Enabled {
		t.Fatalf("existing check was disabled by invalid active metadata: %+v", check)
	}
}

func TestSyncDeploymentsKeepsSameServiceOnTwoNodes(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)

	deployments := []*adminpb.ServiceDeployment{
		{NodeId: "node-a", ServiceName: "moox_collector", Protocol: "http", Host: "10.0.0.1", Port: 11402, Status: "active"},
		{NodeId: "node-b", ServiceName: "moox_collector", Protocol: "http", Host: "10.0.0.2", Port: 11402, Status: "active"},
	}
	requireSyncCount(t, syncer, deployments, 2)
	for _, checkID := range []string{"sysdeploy:node-a:moox_collector", "sysdeploy:node-b:moox_collector"} {
		check, err := checks.Get(ctx, "", checkID)
		if err != nil {
			t.Fatalf("get %s: %v", checkID, err)
		}
		if !check.Enabled {
			t.Fatalf("%s unexpectedly disabled", checkID)
		}
	}

	deployments[1].Status = "disabled"
	requireSyncCount(t, syncer, deployments, 2)
	nodeA, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_collector")
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := checks.Get(ctx, "", "sysdeploy:node-b:moox_collector")
	if err != nil {
		t.Fatal(err)
	}
	if !nodeA.Enabled || nodeB.Enabled {
		t.Fatalf("enabled states node-a=%v node-b=%v", nodeA.Enabled, nodeB.Enabled)
	}
}

func requireSyncCount(t *testing.T, syncer *Syncer, deployments []*adminpb.ServiceDeployment, want int) {
	t.Helper()
	got, err := syncer.SyncDeployments(context.Background(), deployments)
	if err != nil {
		t.Fatalf("SyncDeployments: %v", err)
	}
	if got != want {
		t.Fatalf("synced = %d, want %d", got, want)
	}
}

func TestCheckFromDeploymentDoesNotAssertReadinessForLiveness(t *testing.T) {
	check, err := checkFromDeployment(&adminpb.ServiceDeployment{
		NodeId:      "node-a",
		ServiceName: "web",
		Protocol:    "http",
		Status:      "active",
		ExtraConfig: `{"health_url":"http://10.0.0.1:8080/healthz","health_kind":"liveness"}`,
	})
	if err != nil || check == nil {
		t.Fatalf("checkFromDeployment = %+v, %v", check, err)
	}
	if check.BodyContains != "" {
		t.Fatalf("liveness check body matcher = %q, want empty", check.BodyContains)
	}
}

func TestCheckFromDeploymentUsesConfiguredReadinessBody(t *testing.T) {
	check, err := checkFromDeployment(&adminpb.ServiceDeployment{
		NodeId:      "control",
		ServiceName: "moox_gateway",
		Protocol:    "http",
		Status:      "active",
		ExtraConfig: `{"health_url":"http://127.0.0.1:11012/readyz","health_kind":"readiness","health_body_contains":"ready"}`,
	})
	if err != nil || check == nil {
		t.Fatalf("checkFromDeployment = %+v, %v", check, err)
	}
	if check.BodyContains != "ready" {
		t.Fatalf("readiness body matcher = %q, want ready", check.BodyContains)
	}
}

func TestCheckFromDeploymentRejectsRemoteLoopbackHealthURL(t *testing.T) {
	check, err := checkFromDeployment(&adminpb.ServiceDeployment{
		NodeId:      "node-b",
		ServiceName: "web",
		Protocol:    "http",
		Status:      "active",
		ExtraConfig: `{"health_url":"http://127.0.0.1:8080/readyz"}`,
	})
	if err == nil || check != nil {
		t.Fatalf("checkFromDeployment = %+v, %v; want unreachable error", check, err)
	}
}

func TestSyncDeploymentsDoesNotTouchManualCheck(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
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
		{NodeId: "node-a", ServiceName: "moox_cloudnode", Protocol: "http", Host: "10.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz"}`},
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
	system, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil || system.Source != domain.CheckSourceSysDeploy {
		t.Fatalf("system check = %+v, err = %v", system, err)
	}
}

func TestSyncDeploymentsPreservesManualSystemCheckEnabledOverride(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	checkID := "sysdeploy:node-a:moox_cloudnode"
	if err := checks.Create(ctx, &domain.Check{
		SpaceID:         "",
		CheckID:         checkID,
		Name:            "CloudNode@node-a",
		Kind:            domain.CheckKindHTTP,
		URL:             "http://10.0.0.1:11411/healthz",
		Method:          "GET",
		Headers:         "{}",
		IntervalSeconds: 30,
		TimeoutMS:       3000,
		ExpectedStatus:  "200-299",
		Enabled:         false,
		Source:          domain.CheckSourceSysDeploy,
		Labels:          `{"node_id":"node-a", "service_name":"moox_cloudnode", "monitor_enabled_override" : false}`,
	}); err != nil {
		t.Fatalf("create disabled system check: %v", err)
	}

	syncer := NewSyncer(checks, nil)
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{{
		NodeId:      "node-a",
		ServiceName: "moox_cloudnode",
		Protocol:    "http",
		Host:        "10.0.0.1",
		Port:        11401,
		Status:      "active",
		ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz"}`,
	}}); err != nil {
		t.Fatalf("sync active deployment: %v", err)
	}

	got, err := checks.Get(ctx, "", checkID)
	if err != nil {
		t.Fatalf("get system check: %v", err)
	}
	if got.Enabled {
		t.Fatalf("system check was re-enabled by sync: %+v", got)
	}
	if _, err := syncer.SyncDeployments(ctx, nil); err != nil {
		t.Fatalf("remove deployment: %v", err)
	}
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{{
		NodeId:      "node-a",
		ServiceName: "moox_cloudnode",
		Protocol:    "http",
		Host:        "10.0.0.1",
		Port:        11401,
		Status:      "active",
		ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz"}`,
	}}); err != nil {
		t.Fatalf("restore deployment: %v", err)
	}
	got, err = checks.Get(ctx, "", checkID)
	if err != nil {
		t.Fatalf("get restored system check: %v", err)
	}
	if got.Enabled {
		t.Fatalf("manual disabled override was lost after deployment recovery: %+v", got)
	}
}

func TestSyncDeploymentsRestoresAutoDisabledCheckWhenDeploymentReturns(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)
	deployment := &adminpb.ServiceDeployment{
		NodeId:      "node-a",
		ServiceName: "moox_cloudnode",
		Protocol:    "http",
		Host:        "10.0.0.1",
		Port:        11401,
		Status:      "active",
		ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz"}`,
	}
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{deployment}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	checkedAt := time.Now().UTC().Truncate(time.Second)
	nextCheckAt := checkedAt.Add(time.Minute)
	initial, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil {
		t.Fatalf("get initial check: %v", err)
	}
	initial.LastCheckedAt = &checkedAt
	initial.NextCheckAt = &nextCheckAt
	if err := checks.Update(ctx, initial); err != nil {
		t.Fatalf("record check schedule: %v", err)
	}
	if _, err := syncer.SyncDeployments(ctx, nil); err != nil {
		t.Fatalf("remove deployment: %v", err)
	}
	removed, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil {
		t.Fatalf("get removed check: %v", err)
	}
	if removed.Enabled {
		t.Fatalf("removed deployment remained enabled: %+v", removed)
	}
	if _, ok := store.CheckEnabledOverride(removed.Labels); ok {
		t.Fatalf("automatic disable left a user override: %s", removed.Labels)
	}
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{deployment}); err != nil {
		t.Fatalf("restore deployment: %v", err)
	}
	restored, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil {
		t.Fatalf("get restored check: %v", err)
	}
	if !restored.Enabled {
		t.Fatalf("restored deployment was not enabled: %+v", restored)
	}
	if restored.LastCheckedAt == nil || !restored.LastCheckedAt.Equal(checkedAt) || restored.NextCheckAt == nil || !restored.NextCheckAt.Equal(nextCheckAt) {
		t.Fatalf("sync changed runtime schedule: last=%v next=%v", restored.LastCheckedAt, restored.NextCheckAt)
	}
}

func TestSyncDeploymentsDisablesRemovedSystemChecks(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, nil)
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{NodeId: "node-a", ServiceName: "moox_cloudnode", Protocol: "http", Host: "10.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz","monitor_enabled":true}`},
		{NodeId: "node-a", ServiceName: "moox_collector", Protocol: "http", Host: "10.0.0.1", Port: 11402, Status: "active", ExtraConfig: `{}`},
	}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	n, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{NodeId: "node-a", ServiceName: "moox_cloudnode", Protocol: "http", Host: "10.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz","monitor_enabled":false}`},
	})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if n != 2 {
		t.Fatalf("synced = %d, want update count including two disables", n)
	}
	for _, checkID := range []string{"sysdeploy:node-a:moox_cloudnode", "sysdeploy:node-a:moox_collector"} {
		got, err := checks.Get(ctx, "", checkID)
		if err != nil {
			t.Fatalf("get %s: %v", checkID, err)
		}
		if got.Enabled {
			t.Fatalf("%s still enabled after removal/monitor disable: %+v", checkID, got)
		}
	}
}

func TestSyncKeepsExistingChecksWhenAdminFails(t *testing.T) {
	ctx := context.Background()
	mgr := openSyncDB(t)
	checks := mgr.Repositories().Checks
	syncer := NewSyncer(checks, failingSource{})
	if _, err := syncer.SyncDeployments(ctx, []*adminpb.ServiceDeployment{
		{NodeId: "node-a", ServiceName: "moox_cloudnode", Protocol: "http", Host: "10.0.0.1", Port: 11401, Status: "active", ExtraConfig: `{"health_url":"http://10.0.0.1:11411/healthz"}`},
	}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if _, err := syncer.Sync(ctx); err == nil {
		t.Fatal("Sync() error = nil, want admin failure")
	}
	got, err := checks.Get(ctx, "", "sysdeploy:node-a:moox_cloudnode")
	if err != nil {
		t.Fatalf("get existing check: %v", err)
	}
	if !got.Enabled {
		t.Fatalf("existing check disabled after admin failure")
	}
}

type failingSource struct{}

func (failingSource) DesiredDeployments(context.Context) ([]*adminpb.ServiceDeployment, error) {
	return nil, errAdminUnavailable
}

func TestClientSourceReadsAllDeploymentStatusesWithBoundedPagination(t *testing.T) {
	t.Parallel()

	client := &pagingDeploymentClient{total: 101}
	source := &ClientSource{client: client}
	deployments, err := source.DesiredDeployments(context.Background())
	if err != nil {
		t.Fatalf("DesiredDeployments: %v", err)
	}
	if len(deployments) != 101 || deployments[100].GetStatus() != "disabled" {
		t.Fatalf("deployments = %d, last=%+v", len(deployments), deployments[len(deployments)-1])
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2", client.calls)
	}

	tooMany := &ClientSource{client: &pagingDeploymentClient{total: 501}}
	if _, err := tooMany.DesiredDeployments(context.Background()); err == nil {
		t.Fatal("more than 500 deployments accepted")
	}
}

type pagingDeploymentClient struct {
	total int
	calls int
}

func (c *pagingDeploymentClient) ListServiceDeployments(_ context.Context, req *adminpb.ListServiceDeploymentsReq, _ ...client.Option) (*adminpb.ListServiceDeploymentsRsp, error) {
	c.calls++
	page := int(req.GetPage().GetPage())
	start := (page - 1) * 100
	end := start + 100
	if end > c.total {
		end = c.total
	}
	rows := make([]*adminpb.ServiceDeployment, 0, end-start)
	for i := start; i < end; i++ {
		status := "active"
		if i == c.total-1 {
			status = "disabled"
		}
		rows = append(rows, &adminpb.ServiceDeployment{ServiceName: fmt.Sprintf("service-%03d", i), Status: status})
	}
	return &adminpb.ListServiceDeploymentsRsp{
		RetInfo:     &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
		Deployments: rows,
		PageResult:  &commonpb.PageResult{Page: uint32(page), Size: 100, HasMore: end < c.total},
	}, nil
}

func openSyncDB(t *testing.T) *store.Store {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return mgr
}
