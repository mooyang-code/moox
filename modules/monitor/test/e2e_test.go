package test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

const monitorGatewaySecret = "two-node-monitor-gateway-secret"

func TestTwoMonitorNodesSynchronizeThroughGatewaysAndAlertOnFailure(t *testing.T) {
	ctx := context.Background()
	gatewayBinary := buildGatewayHelper(t)
	nodeA := newMonitorNode(t, gatewayBinary, "monitor-a", "gateway-a")
	nodeB := newMonitorNode(t, gatewayBinary, "monitor-b", "gateway-b")
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
	firingState, err := nodeA.repos.Alerts.GetState(ctx, "moox_system", "monitor-peer/monitor-b", "monitor-peer/monitor-b")
	if err != nil || firingState.TriggeredAt == nil {
		t.Fatalf("firing peer state = %+v, err=%v", firingState, err)
	}
	originalTriggeredAt := *firingState.TriggeredAt
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
	resolvedState, err := nodeA.repos.Alerts.GetState(ctx, "moox_system", "monitor-peer/monitor-b", "monitor-peer/monitor-b")
	if err != nil || resolvedState.TriggeredAt == nil || !resolvedState.TriggeredAt.Equal(originalTriggeredAt) || resolvedState.ResolvedAt == nil || !resolvedState.ResolvedAt.After(originalTriggeredAt) {
		t.Fatalf("resolved peer state did not preserve trigger time: %+v, original=%s, err=%v", resolvedState, originalTriggeredAt, err)
	}
}

type monitorNode struct {
	instanceID string
	nodeID     string
	store      *store.Store
	repos      *store.Repositories
	service    *monitorrpc.Service
	monitor    *httptest.Server
	gateway    *gatewayProcess
	running    bool
	mu         sync.Mutex
}

func newMonitorNode(t *testing.T, gatewayBinary, instanceID, nodeID string) *monitorNode {
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
	node.gateway = startGatewayProcess(t, gatewayBinary, nodeID, node.monitor.URL)
	return node
}

func (node *monitorNode) startMonitor(t *testing.T) {
	t.Helper()
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.monitor == nil {
		node.monitor = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			node.mu.Lock()
			running := node.running
			node.mu.Unlock()
			if !running {
				http.Error(response, "monitor unavailable", http.StatusServiceUnavailable)
				return
			}
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
	node.running = true
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
	node.running = false
	node.mu.Unlock()
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

type gatewayProcess struct {
	URL     string
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error
	output  *bytes.Buffer
	once    sync.Once
}

func buildGatewayHelper(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	gatewayDirectory := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "gateway"))
	binary := filepath.Join(t.TempDir(), "moox-gateway-e2e-helper")
	command := exec.Command("go", "build", "-o", binary, "./cmd/e2e-helper")
	command.Dir = gatewayDirectory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Gateway E2E helper: %v\n%s", err, output)
	}
	return binary
}

func startGatewayProcess(t *testing.T, binary, nodeID, upstreamURL string) *gatewayProcess {
	t.Helper()
	directory := t.TempDir()
	readyFile := filepath.Join(directory, "ready")
	output := &bytes.Buffer{}
	command := exec.Command(binary,
		"--node-id", nodeID, "--upstream-url", upstreamURL,
		"--ready-file", readyFile, "--nonce-dir", filepath.Join(directory, "nonces"),
		"--key-id", "monitor",
	)
	command.Env = append(os.Environ(), "MOOX_GATEWAY_E2E_SERVICE_SECRET="+monitorGatewaySecret)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &gatewayProcess{cmd: command, done: make(chan struct{}), output: output}
	t.Cleanup(process.Close)
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(readyFile); err == nil && strings.HasPrefix(string(raw), "http://127.0.0.1:") {
			process.URL = string(raw)
			return process
		}
		select {
		case <-process.done:
			t.Fatalf("Gateway E2E helper exited before ready: %v\n%s", process.waitErr, output.String())
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = command.Process.Kill()
	<-process.done
	t.Fatalf("Gateway E2E helper did not become ready\n%s", output.String())
	return nil
}

func (process *gatewayProcess) Close() {
	if process == nil || process.cmd == nil || process.cmd.Process == nil {
		return
	}
	process.once.Do(func() {
		_ = process.cmd.Process.Signal(os.Interrupt)
		select {
		case <-process.done:
		case <-time.After(5 * time.Second):
			_ = process.cmd.Process.Kill()
			<-process.done
		}
	})
}
