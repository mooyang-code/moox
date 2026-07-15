package test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorpeer "github.com/mooyang-code/moox/modules/monitor/internal/peer"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

const monitorGatewaySecret = "two-node-monitor-gateway-secret"

func TestTwoMonitorNodesSynchronizeThroughGatewaysAndAlertOnFailure(t *testing.T) {
	ctx := context.Background()
	nodeA := newMonitorNode(t, "monitor-a", "gateway-a")
	nodeB := newMonitorNode(t, "monitor-b", "gateway-b")
	defer nodeA.Close()
	defer nodeB.Close()

	pullerA := newPeerPuller(t, nodeA, nodeB)
	pullerB := newPeerPuller(t, nodeB, nodeA)
	if err := pullerA.PullOnce(ctx); err != nil {
		t.Fatalf("A pulls B: %v", err)
	}
	if err := pullerB.PullOnce(ctx); err != nil {
		t.Fatalf("B pulls A: %v", err)
	}
	assertPeerActiveWithSnapshot(t, nodeA, "monitor-b")
	assertPeerActiveWithSnapshot(t, nodeB, "monitor-a")

	nodeB.stopMonitor()
	if err := pullerA.PullOnce(ctx); err == nil {
		t.Fatal("A pull after B stopped returned nil")
	}
	updateErr, err := store.WithDatabase(nodeA.store, func(db *gorm.DB) error {
		return db.Model(&domain.MonitorInstance{}).Where("c_instance_id = ?", "monitor-b").Update("c_last_seen_at", time.Now().UTC().Add(-4*time.Second)).Error
	})
	if err != nil || updateErr != nil {
		t.Fatalf("age peer receipt: callback=%v update=%v", err, updateErr)
	}
	if err := pullerA.MarkStale(ctx, time.Now().UTC(), time.Second); err != nil {
		t.Fatalf("mark B stale: %v", err)
	}
	instances, err := nodeA.repos.Peers.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].Status != domain.InstanceStatusDown {
		t.Fatalf("A peer instances = %+v, err=%v", instances, err)
	}
	events, err := nodeA.repos.Alerts.ListRecentEvents(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventType != domain.AlertEventTriggered || events[0].Status != domain.AlertStatusFiring || events[0].OwnerInstanceID != "monitor-a" || events[0].CheckID != "monitor-peer/monitor-b" {
		t.Fatalf("peer failure events = %+v", events)
	}
	if err := pullerA.MarkStale(ctx, time.Now().UTC().Add(time.Second), time.Second); err != nil {
		t.Fatalf("repeat stale scan: %v", err)
	}
	events, err = nodeA.repos.Alerts.ListRecentEvents(ctx, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("repeat stale scan emitted duplicate transition: %+v, err=%v", events, err)
	}

	nodeB.startMonitor(t)
	if err := pullerA.PullOnce(ctx); err != nil {
		t.Fatalf("A pulls recovered B: %v", err)
	}
	instances, err = nodeA.repos.Peers.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].Status != domain.InstanceStatusActive {
		t.Fatalf("recovered peer instances = %+v, err=%v", instances, err)
	}
	events, err = nodeA.repos.Alerts.ListRecentEvents(ctx, 10)
	if err != nil || len(events) != 2 || events[0].EventType != domain.AlertEventResolved || events[0].Status != domain.AlertStatusResolved {
		t.Fatalf("peer recovery events = %+v, err=%v", events, err)
	}
}

type monitorNode struct {
	instanceID string
	nodeID     string
	store      *store.Store
	repos      *store.Repositories
	service    *monitorrpc.Service
	monitor    *httptest.Server
	gateway    *httptest.Server
	mu         sync.Mutex
}

func newMonitorNode(t *testing.T, instanceID, nodeID string) *monitorNode {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		t.Fatal(err)
	}
	node := &monitorNode{instanceID: instanceID, nodeID: nodeID, store: mgr, repos: mgr.Repositories()}
	node.service = monitorrpc.New(node.repos, monitorrpc.Options{InstanceID: instanceID})
	node.startMonitor(t)
	node.gateway = httptest.NewServer(node.gatewayHandler())
	return node
}

func (node *monitorNode) startMonitor(t *testing.T) {
	t.Helper()
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.monitor != nil {
		t.Fatal("monitor is already running")
	}
	node.monitor = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/trpc.moox.monitor.MonitorMgr/GetPeerSnapshot" {
			http.NotFound(response, request)
			return
		}
		result, callErr := node.service.GetPeerSnapshot(request.Context(), &monitorpb.GetPeerSnapshotReq{})
		if callErr != nil {
			http.Error(response, callErr.Error(), http.StatusInternalServerError)
			return
		}
		encoded, marshalErr := protojson.Marshal(result)
		if marshalErr != nil {
			http.Error(response, marshalErr.Error(), http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(encoded)
	}))
}

func (node *monitorNode) gatewayHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(response, "bad request", http.StatusBadRequest)
			return
		}
		if _, err := gatewayauth.Verify(
			gatewayauth.Credentials{KeyID: "monitor", Secret: monitorGatewaySecret},
			gatewayauth.Request{Method: request.Method, Path: request.URL.EscapedPath(), TargetNode: node.nodeID, Body: body},
			request.Header, time.Now(),
		); err != nil {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		node.mu.Lock()
		monitorServer := node.monitor
		node.mu.Unlock()
		if monitorServer == nil {
			http.Error(response, "upstream unavailable", http.StatusBadGateway)
			return
		}
		monitorURL, _ := url.Parse(monitorServer.URL)
		route := gatewayproxy.Route{ServiceID: "monitor", Address: monitorURL.Host, ServicePath: "trpc.moox.monitor.MonitorMgr", AllowedMethods: []string{"GetPeerSnapshot"}}
		method := strings.TrimPrefix(request.URL.Path, "/api/service/monitor/")
		upstream, err := gatewayproxy.Forward(request.Context(), nil, route, method, body, request.Header)
		if err != nil {
			http.Error(response, "upstream unavailable", http.StatusBadGateway)
			return
		}
		for name, values := range upstream.Header {
			for _, value := range values {
				response.Header().Add(name, value)
			}
		}
		response.WriteHeader(upstream.StatusCode)
		_, _ = response.Write(upstream.Body)
	})
}

func newPeerPuller(t *testing.T, local, remote *monitorNode) *monitorpeer.Puller {
	t.Helper()
	puller, err := monitorpeer.NewPuller(local.repos.Peers, monitorpeer.PullerOptions{
		Peers:           []monitorpeer.Remote{{InstanceID: remote.instanceID, GatewayURL: remote.gateway.URL, NodeID: remote.nodeID}},
		Timeout:         time.Second,
		Credentials:     gatewayauth.Credentials{KeyID: "monitor", Secret: monitorGatewaySecret},
		Alerts:          local.repos.Alerts,
		OwnerInstanceID: local.instanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return puller
}

func assertPeerActiveWithSnapshot(t *testing.T, node *monitorNode, peerID string) {
	t.Helper()
	instances, err := node.repos.Peers.ListInstances(context.Background())
	if err != nil || len(instances) != 1 || instances[0].InstanceID != peerID || instances[0].Status != domain.InstanceStatusActive || !strings.Contains(instances[0].Snapshot, peerID) {
		t.Fatalf("%s instances = %+v, err=%v", node.instanceID, instances, err)
	}
	snapshots, err := node.repos.Peers.ListSnapshots(context.Background())
	if err != nil || len(snapshots) != 1 || snapshots[0].InstanceID != peerID || !strings.Contains(snapshots[0].Snapshot, peerID) {
		t.Fatalf("%s snapshots = %+v, err=%v", node.instanceID, snapshots, err)
	}
}

func (node *monitorNode) stopMonitor() {
	node.mu.Lock()
	monitor := node.monitor
	node.monitor = nil
	node.mu.Unlock()
	if monitor != nil {
		monitor.Close()
	}
}

func (node *monitorNode) Close() {
	node.mu.Lock()
	gateway, monitor := node.gateway, node.monitor
	node.gateway, node.monitor = nil, nil
	node.mu.Unlock()
	if gateway != nil {
		gateway.Close()
	}
	if monitor != nil {
		monitor.Close()
	}
	_ = node.store.Close()
}
