package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/gateway/internal/health"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestInitializeLoadsCacheBeforeInitialPull(t *testing.T) {
	cached := testSnapshot(t, "node-a", "cached")
	pulled := testSnapshot(t, "node-a", "fresh")
	events := []string{}
	routes := &fakeRoutes{load: cached, events: &events}
	control := &fakeControl{pull: pulled, events: &events}
	runtime := New(Options{NodeID: "node-a", Routes: routes, Control: control, Health: health.NewState()})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() = %v", err)
	}
	if strings.Join(events, ",") != "load,pull:"+cached.RouteHash+",save,report:"+pulled.RouteHash {
		t.Fatalf("events = %v", events)
	}
	if route, ok := runtime.Table().Resolve("fresh"); !ok || route.ServiceID != "fresh" {
		t.Fatalf("fresh route = %+v, %v", route, ok)
	}
}

func TestInitializeRequiresCacheOrSuccessfulPull(t *testing.T) {
	runtime := New(Options{NodeID: "node-a", Routes: &fakeRoutes{loadErr: errors.New("no cache")}, Control: &fakeControl{pullErr: errors.New("admin down")}, Health: health.NewState()})
	if err := runtime.Initialize(context.Background()); err == nil {
		t.Fatal("Initialize() succeeded without cache or pull")
	}
}

func TestRefreshFailureKeepsReadinessAndIncrementsMetric(t *testing.T) {
	cached := testSnapshot(t, "node-a", "cached")
	state := health.NewState()
	control := &fakeControl{pull: cached}
	runtime := New(Options{NodeID: "node-a", Routes: &fakeRoutes{load: cached}, Control: control, Health: state})
	if err := runtime.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() = %v", err)
	}
	control.pullErr = errors.New("admin down")
	if err := runtime.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() succeeded")
	}
	if !state.Ready() {
		t.Fatal("failed refresh cleared readiness")
	}
	recorder := httptest.NewRecorder()
	state.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(recorder.Body.String(), "gateway_route_sync_errors_total 1") {
		t.Fatalf("metrics = %q", recorder.Body.String())
	}
	if _, ok := runtime.Table().Resolve("cached"); !ok {
		t.Fatal("failed refresh discarded cached route")
	}
}

func testSnapshot(t *testing.T, nodeID, serviceID string) gatewayproxy.Snapshot {
	t.Helper()
	snapshot, err := gatewayproxy.NormalizeAndHash(nodeID, []gatewayproxy.Route{{ServiceID: serviceID, Address: "127.0.0.1:1234", ServicePath: "trpc.moox.test.Service"}})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

type fakeRoutes struct {
	load             gatewayproxy.Snapshot
	loadErr, saveErr error
	events           *[]string
}

func (routes *fakeRoutes) Load() (gatewayproxy.Snapshot, error) {
	if routes.events != nil {
		*routes.events = append(*routes.events, "load")
	}
	return routes.load, routes.loadErr
}
func (routes *fakeRoutes) Save(gatewayproxy.Snapshot) error {
	if routes.events != nil {
		*routes.events = append(*routes.events, "save")
	}
	return routes.saveErr
}

type fakeControl struct {
	pull               gatewayproxy.Snapshot
	pullErr, reportErr error
	events             *[]string
}

func (control *fakeControl) Pull(_ context.Context, hash string) (gatewayproxy.Snapshot, error) {
	if control.events != nil {
		*control.events = append(*control.events, "pull:"+hash)
	}
	return control.pull, control.pullErr
}
func (control *fakeControl) Report(_ context.Context, hash string, _ int32, _ string) error {
	if control.events != nil {
		*control.events = append(*control.events, "report:"+hash)
	}
	return control.reportErr
}
