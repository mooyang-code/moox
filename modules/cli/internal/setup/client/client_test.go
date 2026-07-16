package client

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeForwarder struct {
	handler http.Handler
	remote  string
}

func (f *fakeForwarder) ForwardLocal(ctx context.Context, remote string) (net.Listener, error) {
	f.remote = remote
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: f.handler}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return listener, nil
}

func clientSnapshot(t *testing.T) *setupconfig.Snapshot {
	snapshot, _ := clientSnapshotWithPath(t)
	return snapshot
}

func clientSnapshotWithPath(t *testing.T) (*setupconfig.Snapshot, string) {
	t.Helper()
	root := t.TempDir()
	body := `[admin]
username = "admin"
password = "recognizable-admin-password"
[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"
[control_host]
name = "control"
address = "192.0.2.10"
username = "ubuntu"
password = "recognizable-control-password"
[[other_hosts]]
name = "compute"
address = "192.0.2.11"
username = "ubuntu"
password = "recognizable-compute-password"
`
	path := filepath.Join(root, "custom.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)
	return snapshot, path
}

func TestApplyUsesForwardedPrivateSetupEndpoint(t *testing.T) {
	var capturedPath string
	var capturedRequest pb.ApplySetupReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Apply(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, 2, result.Hosts)
	assert.Equal(t, "127.0.0.1:11110", forwarder.remote)
	assert.Equal(t, "/trpc.moox.admin.Setup/ApplySetup", capturedPath)
	assert.Equal(t, "recognizable-secret-key", capturedRequest.GetTencentCloud().GetSecretKey())
}

func TestStatusSendsManifestAndReturnsSanitizedState(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 1, Hosts: 2,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Status(context.Background(), clientSnapshot(t))
	require.NoError(t, err)
	assert.Equal(t, "completed", result.State)
	assert.Equal(t, "recognizable-admin-password", capturedRequest.GetAdmin().GetPassword())
}

func TestApplyReturnsStableSecretFreeErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "conflict", body: `{"ret_info":{"code":1,"msg":"setup_conflict"}}`, want: "setup_conflict"},
		{name: "unexpected remote text", body: `recognizable-secret-key`, want: "setup_response_invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})}
			_, err := New(forwarder).Apply(context.Background(), clientSnapshot(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "recognizable-secret-key")
			assert.NotContains(t, err.Error(), "recognizable-admin-password")
		})
	}
}

func TestApplyDetectsManifestMutationBeforeRequest(t *testing.T) {
	snapshot, path := clientSnapshotWithPath(t)
	require.NoError(t, os.WriteFile(path, []byte("changed"), 0o600))
	forwarder := &fakeForwarder{handler: http.NotFoundHandler()}
	_, err := New(forwarder).Apply(context.Background(), snapshot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config_changed")
}
