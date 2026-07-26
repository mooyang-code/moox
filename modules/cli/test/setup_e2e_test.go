package test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	cloudprovider "github.com/mooyang-code/moox/packages/cloudprovider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSetupWorkflowLeavesManifestAndArtifactsSecretFree(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "custom.toml")
	secrets := []string{"admin-e2e-password", "control-e2e-password", "compute-e2e-password", "AKID-e2e", "cloud-e2e-secret"}
	raw := []byte(`[admin]
username = "admin"
password = "admin-e2e-password"
[tencent_cloud]
secret_id = "AKID-e2e"
secret_key = "cloud-e2e-secret"
[eventbus]
public_address = "eventbus.example.test"
port = 4222
tls_enabled = true
[control_host]
name = "control"
address = "192.0.2.10"
port = 22
username = "ubuntu"
password = "control-e2e-password"
[[other_hosts]]
name = "compute-1"
address = "192.0.2.11"
port = 22
username = "ubuntu"
password = "compute-e2e-password"
`)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	snapshot, err := setupconfig.Load(path, root)
	require.NoError(t, err)

	validation, err := setupvalidate.Run(context.Background(), snapshot, setupvalidate.Dependencies{
		Identity: staticIdentity{}, SSH: staticSSHChecker{},
	})
	require.NoError(t, err)
	require.Len(t, validation.Checks, 4)

	forwarder := &setupForwarder{handler: setupAdminHandler()}
	privateClient := setupclient.New(forwarder)
	apply, err := privateClient.Apply(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Equal(t, "created", apply.Action)
	status, err := privateClient.Status(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)

	archive := filepath.Join(root, "control.tar.gz")
	require.NoError(t, os.WriteFile(archive, []byte("safe-control-package"), 0o600))
	transport := &captureTransport{}
	events := []setupdeploy.ReadinessStage{}
	err = setupdeploy.Control(context.Background(), transport, setupdeploy.Options{
		RepositoryRoot: root, PublicHost: snapshot.Manifest.ControlHost.Address, BrowserPort: 9527,
		TargetGOOS: "linux", TargetGOARCH: "amd64",
		EventBusPublicAddress: snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:          snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:    snapshot.Manifest.EventBus.TLSEnabled,
	}, setupdeploy.Dependencies{
		Packager: staticPackager{path: archive},
		Probe:    captureProbe{events: &events},
		CAStore:  discardCAStore{},
	})
	require.NoError(t, err)
	require.Equal(t, []setupdeploy.ReadinessStage{
		setupdeploy.AdminReady, setupdeploy.SetupReady, setupdeploy.GatewayReady,
		setupdeploy.WebReady, setupdeploy.BrowserHTTPSReady,
	}, events)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	var output bytes.Buffer
	require.NoError(t, json.NewEncoder(&output).Encode(validation))
	require.NoError(t, json.NewEncoder(&output).Encode(apply))
	require.NoError(t, json.NewEncoder(&output).Encode(status))
	combined := output.String() + strings.Join(transport.commands, "\n")
	for _, secret := range secrets {
		assert.NotContains(t, combined, secret)
		assert.NotContains(t, transport.uploaded.String(), secret)
	}
}

type staticIdentity struct{}

func (staticIdentity) GetCallerIdentity(context.Context) (cloudprovider.CallerIdentity, error) {
	return cloudprovider.CallerIdentity{Provider: "tencent", AccountID: "100000000001"}, nil
}

type staticSSHChecker struct{}

func (staticSSHChecker) Check(context.Context, setupconfig.Host) error { return nil }

type setupForwarder struct{ handler http.Handler }

func (f *setupForwarder) ForwardLocal(ctx context.Context, _ string) (net.Listener, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: f.handler}
	go func() { _ = server.Serve(listener) }()
	go func() { <-ctx.Done(); _ = server.Close() }()
	return listener, nil
}

func setupAdminHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/trpc.moox.admin.Setup/ApplySetup", func(writer http.ResponseWriter, request *http.Request) {
		var input pb.ApplySetupReq
		raw, err := io.ReadAll(request.Body)
		if err != nil || protojson.Unmarshal(raw, &input) != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		writeSetupResponse(writer, &pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"},
			Action:  "created", Users: 1, Secrets: 1, Hosts: int32(1 + len(input.GetOtherHosts())),
		})
	})
	mux.HandleFunc("/trpc.moox.admin.Setup/GetSetupStatus", func(writer http.ResponseWriter, request *http.Request) {
		writeSetupResponse(writer, &pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"},
			State:   "completed", Users: 1, Secrets: 1, Hosts: 2,
		})
	})
	return mux
}

func writeSetupResponse(writer http.ResponseWriter, message proto.Message) {
	raw, err := protojson.Marshal(message)
	if err != nil {
		http.Error(writer, "failed", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(raw)
}

type staticPackager struct{ path string }

func (p staticPackager) Package(context.Context, setupdeploy.Options) (string, error) {
	return p.path, nil
}

type discardCAStore struct{}

func (discardCAStore) Save(string, []byte) error { return nil }

type captureProbe struct{ events *[]setupdeploy.ReadinessStage }

func (p captureProbe) Wait(_ context.Context, _ setupssh.Client, stage setupdeploy.ReadinessStage, _ setupdeploy.Options) error {
	*p.events = append(*p.events, stage)
	return nil
}

type captureTransport struct {
	uploaded bytes.Buffer
	commands []string
}

func (c *captureTransport) Check(context.Context) error { return nil }
func (c *captureTransport) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, nil
}
func (c *captureTransport) Upload(_ context.Context, reader io.Reader, _ int64, _ string, mode fs.FileMode) error {
	if mode != 0o600 {
		return os.ErrPermission
	}
	_, err := io.Copy(&c.uploaded, reader)
	return err
}
func (c *captureTransport) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	c.commands = append(c.commands, strings.Join(argv, " "))
	return setupssh.Result{}, nil
}
func (c *captureTransport) Close() error { return nil }
