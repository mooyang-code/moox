package peer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestPullerPostsSnapshotRPCThroughTargetGateway(t *testing.T) {
	ctx := context.Background()
	mgr := openPeerDB(t)
	repo := mgr.Repositories().Peers
	now := time.Now().UTC().Add(-time.Minute)
	const secret = "peer-service-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/service/monitor/GetPeerSnapshot" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
			http.NotFound(w, request)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if string(body) != `{}` {
			t.Errorf("request body = %q, want {}", body)
		}
		if _, err := gatewayauth.Verify(
			gatewayauth.Credentials{KeyID: "monitor", Secret: secret},
			gatewayauth.Request{Method: http.MethodPost, Path: request.URL.EscapedPath(), TargetNode: "gateway-hk-177", Body: body},
			request.Header,
			time.Now(),
		); err != nil {
			t.Errorf("verify gateway signature: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		payload, err := protojson.Marshal(&monitorpb.GetPeerSnapshotRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, InstanceId: "monitor-hk", BaseUrl: "https://hk.example", ObservedAt: now.Format(time.RFC3339Nano),
			Checks: []*monitorpb.PeerCheckSnapshot{{CheckId: "check-1", Status: domain.CheckStatusOK}},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	puller, err := NewPuller(repo, PullerOptions{
		Peers:       []Remote{{InstanceID: "monitor-hk", GatewayURL: server.URL, NodeID: "gateway-hk-177"}},
		Timeout:     time.Second,
		Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: secret},
	})
	if err != nil {
		t.Fatalf("NewPuller: %v", err)
	}
	requireNoError(t, puller.PullOnce(ctx))
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil || len(snapshots) != 1 || snapshots[0].InstanceID != "monitor-hk" {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	if !snapshots[0].CheckedAt.Equal(now) {
		t.Fatalf("snapshot checked_at = %s, want remote observed_at %s", snapshots[0].CheckedAt, now)
	}
	instances, err := repo.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].LastSeenAt == nil || !instances[0].LastSeenAt.After(now) {
		t.Fatalf("instances=%+v err=%v, want local receipt time after remote observed_at %s", instances, err, now)
	}
}

func TestPullerContinuesAfterPeerFailure(t *testing.T) {
	ctx := context.Background()
	mgr := openPeerDB(t)
	repo := mgr.Repositories().Peers
	const secret = "peer-service-secret"
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer bad.Close()
	good := httptest.NewServer(snapshotGateway(t, "gateway-good", secret, "monitor-good"))
	defer good.Close()
	puller, err := NewPuller(repo, PullerOptions{
		Peers:       []Remote{{InstanceID: "monitor-bad", GatewayURL: bad.URL, NodeID: "gateway-bad"}, {InstanceID: "monitor-good", GatewayURL: good.URL, NodeID: "gateway-good"}},
		Timeout:     time.Second,
		Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: secret},
	})
	if err != nil {
		t.Fatalf("NewPuller: %v", err)
	}
	if err := puller.PullOnce(ctx); err == nil {
		t.Fatal("PullOnce error = nil, want partial peer failure")
	}
	instances, err := repo.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].InstanceID != "monitor-good" || instances[0].Status != domain.InstanceStatusActive {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
}

func TestPullerMarksExpiredPeerDown(t *testing.T) {
	ctx := context.Background()
	repo := openPeerDB(t).Repositories().Peers
	staleAt := time.Now().UTC().Add(-time.Minute)
	if err := repo.UpsertInstance(ctx, &domain.MonitorInstance{InstanceID: "monitor-stale", Status: domain.InstanceStatusActive, LastSeenAt: &staleAt, Snapshot: "{}"}); err != nil {
		t.Fatal(err)
	}
	puller, err := NewPuller(repo, PullerOptions{Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := puller.MarkStale(ctx, time.Now().UTC(), time.Second); err != nil {
		t.Fatal(err)
	}
	instances, err := repo.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].Status != domain.InstanceStatusDown {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
}

func TestPullerRejectsResponseWithoutRPCStatus(t *testing.T) {
	repo := openPeerDB(t).Repositories().Peers
	const secret = "peer-service-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if _, err := gatewayauth.Verify(gatewayauth.Credentials{KeyID: "monitor", Secret: secret}, gatewayauth.Request{Method: http.MethodPost, Path: request.URL.EscapedPath(), TargetNode: "gateway-peer", Body: body}, request.Header, time.Now()); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"instanceId":"monitor-peer"}`))
	}))
	defer server.Close()
	puller, err := NewPuller(repo, PullerOptions{Peers: []Remote{{InstanceID: "monitor-peer", GatewayURL: server.URL, NodeID: "gateway-peer"}}, Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: secret}})
	if err != nil {
		t.Fatal(err)
	}
	if err := puller.PullOnce(context.Background()); err == nil {
		t.Fatal("PullOnce error = nil, want missing ret_info rejection")
	}
}

func TestPullerRejectsInvalidGatewayResponses(t *testing.T) {
	const secret = "peer-service-secret"
	tests := []struct {
		name    string
		payload string
	}{
		{name: "malformed JSON", payload: `{"retInfo":`},
		{name: "trailing JSON", payload: `{"retInfo":{"code":"SUCCESS"}} {}`},
		{name: "unknown field", payload: `{"retInfo":{"code":"SUCCESS"},"unexpected":true}`},
		{name: "instance mismatch", payload: `{"retInfo":{"code":"SUCCESS"},"instanceId":"other"}`},
		{name: "bad observed time", payload: `{"retInfo":{"code":"SUCCESS"},"instanceId":"monitor-peer","observedAt":"not-a-time"}`},
		{name: "future observed time", payload: fmt.Sprintf(`{"retInfo":{"code":"SUCCESS"},"instanceId":"monitor-peer","observedAt":%q}`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))},
		{name: "stale observed time", payload: fmt.Sprintf(`{"retInfo":{"code":"SUCCESS"},"instanceId":"monitor-peer","observedAt":%q}`, time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := openPeerDB(t).Repositories().Peers
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.payload)) }))
			defer server.Close()
			puller, err := NewPuller(repo, PullerOptions{Peers: []Remote{{InstanceID: "monitor-peer", GatewayURL: server.URL, NodeID: "gateway-peer"}}, Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: secret}})
			if err != nil {
				t.Fatal(err)
			}
			if err := puller.PullOnce(context.Background()); err == nil {
				t.Fatal("PullOnce error = nil, want response rejection")
			}
		})
	}
}

func TestPullerRejectsOversizedResponseAndRedirect(t *testing.T) {
	const secret = "peer-service-secret"
	for _, handler := range []http.Handler{
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(make([]byte, maxPeerSnapshotBytes+1)) }),
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) }),
	} {
		repo := openPeerDB(t).Repositories().Peers
		server := httptest.NewServer(handler)
		puller, err := NewPuller(repo, PullerOptions{Peers: []Remote{{InstanceID: "monitor-peer", GatewayURL: server.URL, NodeID: "gateway-peer"}}, Credentials: gatewayauth.Credentials{KeyID: "monitor", Secret: secret}})
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		if err := puller.PullOnce(context.Background()); err == nil {
			server.Close()
			t.Fatal("PullOnce error = nil, want hardened response rejection")
		}
		server.Close()
	}
}

func TestPullerRejectsUnsafeClientConfiguration(t *testing.T) {
	repo := openPeerDB(t).Repositories().Peers
	credentials := gatewayauth.Credentials{KeyID: "monitor", Secret: "peer-service-secret"}
	if _, err := NewPuller(repo, PullerOptions{Credentials: credentials, CAFile: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("NewPuller error = nil, want bad CA rejection")
	}
	puller, err := NewPuller(repo, PullerOptions{Peers: []Remote{{InstanceID: "monitor-peer", GatewayURL: "http://192.0.2.1", NodeID: "gateway-peer"}}, Credentials: credentials, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := puller.PullOnce(context.Background()); err == nil {
		t.Fatal("PullOnce error = nil, want non-loopback plaintext rejection")
	}
}

func snapshotGateway(t *testing.T, nodeID, secret, instanceID string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if _, err := gatewayauth.Verify(gatewayauth.Credentials{KeyID: "monitor", Secret: secret}, gatewayauth.Request{Method: http.MethodPost, Path: request.URL.EscapedPath(), TargetNode: nodeID, Body: body}, request.Header, time.Now()); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		payload, _ := protojson.Marshal(&monitorpb.GetPeerSnapshotRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, InstanceId: instanceID, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		_, _ = w.Write(payload)
	})
}

func openPeerDB(t *testing.T) *store.Store {
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

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
