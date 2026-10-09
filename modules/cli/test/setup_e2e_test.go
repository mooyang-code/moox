package test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSetupWorkflowLeavesManifestAndArtifactsSecretFree(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "moox.toml")
	secrets := []string{"admin-e2e-password", "control-e2e-password", "compute-e2e-password", "AKID-e2e", "cloud-e2e-secret"}
	raw := []byte(`[admin]
username = "admin"
password = "admin-e2e-password"
[tencent_cloud]
secret_id = "AKID-e2e"
secret_key = "cloud-e2e-secret"
[eventbus]
port = 4222
tls_enabled = true
[hosts.control]
address = "192.0.2.10"
ssh = { username = "ubuntu", password = "control-e2e-password" }
[hosts.compute-1]
address = "192.0.2.11"
region = "ap-hongkong"
ssh = { username = "ubuntu", password = "compute-e2e-password" }
[placements]
control = ["console-proxy", "web-host", "admin", "eventbus", "monitor"]
compute-1 = ["access", "egress-proxy"]
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

	admin := &setupAdmin{}
	privateClient := setupclient.New(&setupGateway{handler: admin.handler()})
	apply, err := privateClient.Apply(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Equal(t, "created", apply.Action)
	status, err := privateClient.Status(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.State)

	repository, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	transport := &captureTransport{}
	deployer := &setupdeploy.Deployer{
		Manifest: snapshot.Manifest, RepositoryRoot: repository, Version: "e2e",
		Dial:       func(context.Context, setupconfig.Host) (setupssh.Client, error) { return transport, nil },
		Builder:    fakeBuilder{dir: t.TempDir()},
		Placements: privateClient,
		Out:        io.Discard,
	}
	result, err := deployer.Deploy(context.Background(), "compute-1", setupdeploy.Options{})
	require.NoError(t, err)
	assert.Equal(t, "compute-1", result.Host)
	assert.Equal(t, []string{"host-gateway", "host-agent", "access", "egress-proxy"}, result.Components)
	assert.Equal(t, []string{"compute-1:access,egress-proxy@192.0.2.11/ap-hongkong"}, admin.synced, "部署前按部署表同步部署记录")
	assert.Contains(t, strings.Join(transport.commands, "\n"), "--root /data/moox/compute-1")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)

	var output bytes.Buffer
	require.NoError(t, json.NewEncoder(&output).Encode(validation))
	require.NoError(t, json.NewEncoder(&output).Encode(apply))
	require.NoError(t, json.NewEncoder(&output).Encode(status))
	require.NoError(t, json.NewEncoder(&output).Encode(result))
	combined := output.String() + strings.Join(transport.commands, "\n")
	require.NotZero(t, transport.uploaded.Len())
	for _, secret := range secrets {
		assert.NotContains(t, combined, secret)
		assert.NotContains(t, transport.uploaded.String(), secret, "发布包不含 moox.toml 中的口令")
	}
}

// fakeBuilder 为每个二进制写一个可执行的占位文件。
type fakeBuilder struct{ dir string }

func (b fakeBuilder) Build(_ context.Context, request setupdeploy.BuildRequest) (map[string]string, error) {
	out := map[string]string{}
	for _, component := range request.Components {
		binaries := component.Binaries
		if component.Caddy {
			binaries = []string{component.Binary}
		}
		for _, binary := range binaries {
			path := filepath.Join(b.dir, binary)
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				return nil, err
			}
			out[binary] = path
		}
	}
	return out, nil
}

type staticIdentity struct{}

func (staticIdentity) GetCallerIdentity(context.Context) (cloudprovider.CallerIdentity, error) {
	return cloudprovider.CallerIdentity{Provider: "tencent", AccountID: "100000000001"}, nil
}

type staticSSHChecker struct{}

func (staticSSHChecker) Check(context.Context, setupconfig.Host) error { return nil }

// setupGateway 把 setup client 的调用转成对 handler 的 POST /<服务名>/<方法>（protojson 编码）。
type setupGateway struct{ handler http.Handler }

func (f *setupGateway) Invoke(ctx context.Context, servicePath, method string, req, rsp any, _ ...gatewayclient.CallOption) error {
	raw, err := protojson.Marshal(req.(proto.Message))
	if err != nil {
		return err
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/"+servicePath+"/"+method, bytes.NewReader(raw)).WithContext(ctx))
	if recorder.Code != http.StatusOK {
		return fmt.Errorf("HTTP %d", recorder.Code)
	}
	return protojson.Unmarshal(recorder.Body.Bytes(), rsp.(proto.Message))
}

// setupAdmin 模拟 Admin 的 Setup 与 SysDeploy.SyncHostPlacements，记录同步过的主机与组件。
type setupAdmin struct{ synced []string }

func (a *setupAdmin) handler() http.Handler {
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
	mux.HandleFunc("/trpc.moox.ops.SysDeploy/SyncHostPlacements", func(writer http.ResponseWriter, request *http.Request) {
		var input pb.SyncHostPlacementsReq
		raw, err := io.ReadAll(request.Body)
		if err != nil || protojson.Unmarshal(raw, &input) != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		host := input.GetHost()
		a.synced = append(a.synced, fmt.Sprintf("%s:%s@%s/%s", host.GetHostId(), strings.Join(input.GetComponents(), ","), host.GetAddress(), host.GetRegion()))
		writeSetupResponse(writer, &pb.SyncHostPlacementsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}})
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
func (c *captureTransport) Download(_ context.Context, _ string, _ io.Writer) (int64, error) {
	return 0, nil
}
func (c *captureTransport) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	c.commands = append(c.commands, strings.Join(argv, " "))
	switch {
	case len(argv) > 0 && argv[0] == "uname":
		return setupssh.Result{Stdout: "Linux x86_64\n"}, nil
	case len(argv) > 3 && argv[3] == "moox-credentials":
		return setupssh.Result{Stdout: emptyTarGzBase64()}, nil
	}
	return setupssh.Result{Stdout: "ok\n"}, nil
}

// emptyTarGzBase64 是 control 导出密钥时返回的空包（base64 编码的 tar.gz）。
func emptyTarGzBase64() string {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	_ = tar.NewWriter(gz).Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}
func (c *captureTransport) Close() error { return nil }
