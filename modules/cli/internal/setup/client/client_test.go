package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeForwarder struct {
	handler http.Handler
	remote  string
}

func (f *fakeForwarder) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	f.remote = service
	return (testfixture.HandlerGateway{Handler: f.handler}).Invoke(ctx, service, method, req, rsp)
}

func clientSecrets(t *testing.T) *topology.Secrets {
	t.Helper()
	secrets, err := topology.ParseSecrets([]byte(`[admin]
username = "admin"
password = "recognizable-admin-password"

[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"
`))
	require.NoError(t, err)
	return &secrets
}

// clientConfig 是测试用的拓扑：控制机配了私钥（文件在临时目录），另有一台直达主机。
func clientConfig(t *testing.T) *topology.Config {
	t.Helper()
	key := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(key, []byte("PRIVATE KEY BODY\n"), 0o600))
	cfg, err := topology.Parse([]byte(`[hosts.control]
address = "eventbus.example.test"
ssh = { port = 22, username = "ubuntu", private_key_file = "` + key + `" }

[hosts.compute]
address = "192.0.2.11"
ssh = { port = 22, username = "ubuntu" }

[deployments]
control = ["admin", "eventbus"]
compute = []
`))
	require.NoError(t, err)
	return &cfg
}

func TestApplyUsesSecretMgrEnsureInitialSecret(t *testing.T) {
	var capturedPath string
	var capturedBody []byte
	var capturedRequest pb.EnsureInitialSecretReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		capturedBody, _ = io.ReadAll(request.Body)
		_ = protojson.Unmarshal(capturedBody, &capturedRequest)
		response, _ := protojson.Marshal(&pb.EnsureInitialSecretRsp{
			RetInfo:  &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Action:   "created",
			SecretId: tencentDefaultSecretID,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Apply(context.Background(), clientSecrets(t))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, tencentDefaultSecretID, result.SecretID)
	assert.Empty(t, result.Kept)
	assert.Equal(t, secretMgrService, forwarder.remote)
	assert.Equal(t, "/api/admin/secret/EnsureInitialSecret", capturedPath)
	assert.Equal(t, tencentDefaultSecretID, capturedRequest.GetSecret().GetSecretId())
	assert.Equal(t, "cloud", capturedRequest.GetSecret().GetCategory())
	assert.Equal(t, "tencent", capturedRequest.GetSecret().GetProvider())
	assert.Equal(t, "recognizable-secret-id", capturedRequest.GetSecret().GetKeyId())
	assert.Equal(t, "recognizable-secret-key", capturedRequest.GetSecret().GetSecretValue())
	assert.NotContains(t, string(capturedBody), "recognizable-admin-password", "初始化密钥 RPC 不得发送管理员明文密码")

	raw, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "recognizable-secret-key")
	assert.NotContains(t, string(raw), "recognizable-secret-id")
}

func TestApplyReportsKeptWhenConsoleSecretDiffers(t *testing.T) {
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response, _ := protojson.Marshal(&pb.EnsureInitialSecretRsp{
			RetInfo:  &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Action:   "kept",
			SecretId: tencentDefaultSecretID,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Apply(context.Background(), clientSecrets(t))
	require.NoError(t, err)
	assert.Equal(t, "kept", result.Action)
	assert.Equal(t, []string{"tencent_cloud"}, result.Kept)
}

func TestApplyReturnsStableSecretFreeErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{name: "unexpected remote text", body: `recognizable-secret-key`, want: "setup_response_invalid"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tt.body)
			})}
			_, err := New(forwarder).Apply(context.Background(), clientSecrets(t))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "recognizable-secret-key")
			assert.NotContains(t, err.Error(), "recognizable-admin-password")
		})
	}
}
