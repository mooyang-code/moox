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

// clientSecrets 是测试用的秘密文件内容，extra 可以在后面追加一段 TOML（例如 [[exchange_keys]]）。
func clientSecrets(t *testing.T, extra string) *topology.Secrets {
	t.Helper()
	secrets, err := topology.ParseSecrets([]byte(`[admin]
username = "admin"
password = "recognizable-admin-password"

[tencent_cloud]
secret_id = "recognizable-secret-id"
secret_key = "recognizable-secret-key"
` + extra))
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

func TestApplyUsesCommandGateway(t *testing.T) {
	var capturedPath string
	var capturedRequest pb.ApplySetupReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 1,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Apply(context.Background(), clientSecrets(t, ""))
	require.NoError(t, err)
	assert.Equal(t, "created", result.Action)
	assert.Equal(t, "trpc.moox.admin.Setup", forwarder.remote)
	assert.Equal(t, "/trpc.moox.admin.Setup/ApplySetup", capturedPath)
	assert.Equal(t, "recognizable-secret-key", capturedRequest.GetTencentCloud().GetSecretKey())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestApplyWithSpacesMapsAdminSpaceContractAndCounts(t *testing.T) {
	var capturedRequest pb.ApplySetupReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.ApplySetupRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created",
			Users: 1, Secrets: 1, Spaces: 1, SpacesCreated: 1,
		})
		_, _ = w.Write(response)
	})}
	spaces := []Space{{
		SpaceID: "stockcn", Name: "A股市场", Description: "A股行情",
		Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai",
		Status: "active", AttributesJSON: `{"managed_by":"moox-cli"}`,
	}}

	result, err := New(forwarder).ApplyWithSpaces(context.Background(), clientSecrets(t, ""), spaces)
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "stockcn", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, "CN", capturedRequest.GetSpaces()[0].GetMarket())
	assert.Equal(t, "Asia/Shanghai", capturedRequest.GetSpaces()[0].GetTimezone())
	assert.Equal(t, `{"managed_by":"moox-cli"}`, capturedRequest.GetSpaces()[0].GetAttributesJson())
	assert.Equal(t, 1, result.Spaces)
	assert.Equal(t, 1, result.SpacesCreated)
	assert.Zero(t, result.SpacesUnchanged)
}

func TestStatusSendsManifestAndReturnsSanitizedState(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 1,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).Status(context.Background(), clientSecrets(t, ""))
	require.NoError(t, err)
	assert.Equal(t, "completed", result.State)
	assert.Equal(t, "recognizable-admin-password", capturedRequest.GetAdmin().GetPassword())
	assert.Empty(t, capturedRequest.GetSpaces())
}

func TestStatusWithSpacesReturnsSpaceCount(t *testing.T) {
	var capturedRequest pb.GetSetupStatusReq
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		_ = protojson.Unmarshal(body, &capturedRequest)
		response, _ := protojson.Marshal(&pb.GetSetupStatusRsp{
			RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed",
			Users: 1, Secrets: 1, Spaces: 1,
		})
		_, _ = w.Write(response)
	})}

	result, err := New(forwarder).StatusWithSpaces(context.Background(), clientSecrets(t, ""), []Space{{
		SpaceID: "crypto", Name: "加密货币市场", Market: "crypto",
		Timezone: "UTC", Status: "active", AttributesJSON: "{}",
	}})
	require.NoError(t, err)
	require.Len(t, capturedRequest.GetSpaces(), 1)
	assert.Equal(t, "crypto", capturedRequest.GetSpaces()[0].GetSpaceId())
	assert.Equal(t, 1, result.Spaces)
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
			_, err := New(forwarder).Apply(context.Background(), clientSecrets(t, ""))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.NotContains(t, err.Error(), "recognizable-secret-key")
			assert.NotContains(t, err.Error(), "recognizable-admin-password")
		})
	}
}

func TestApplyAndStatusCarryExchangeKeysBothWays(t *testing.T) {
	extra := `
[[exchange_keys]]
name = "readonly"
exchange = "binance"
environment = "production"
api_key = "recognizable-api-key"
secret_key = "recognizable-exchange-secret"
readonly = true

[[exchange_keys]]
name = "main"
exchange = "okx"
environment = "testnet"
api_key = "okx-key"
secret_key = "okx-secret"
passphrase = "recognizable-passphrase"
`
	snapshot := clientSecrets(t, extra)
	var applyRequest pb.ApplySetupReq
	var statusRequest pb.GetSetupStatusReq
	results := []*pb.SetupExchangeKeyResult{
		{SecretId: "exchange-binance-production-readonly", State: "created", Readonly: true},
		{SecretId: "exchange-okx-testnet-main", State: "deleted"},
	}
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		var response []byte
		if request.URL.Path == "/trpc.moox.admin.Setup/ApplySetup" {
			_ = protojson.Unmarshal(body, &applyRequest)
			response, _ = protojson.Marshal(&pb.ApplySetupRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 3, ExchangeKeys: results})
		} else {
			_ = protojson.Unmarshal(body, &statusRequest)
			response, _ = protojson.Marshal(&pb.GetSetupStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 3, ExchangeKeys: results})
		}
		_, _ = w.Write(response)
	})}

	applied, err := New(forwarder).Apply(context.Background(), snapshot)
	require.NoError(t, err)
	require.Len(t, applyRequest.GetExchangeKeys(), 2)
	first := applyRequest.GetExchangeKeys()[0]
	assert.Equal(t, "readonly", first.GetName())
	assert.Equal(t, "binance", first.GetExchange())
	assert.Equal(t, "production", first.GetEnvironment())
	assert.Equal(t, "recognizable-api-key", first.GetApiKey())
	assert.Equal(t, "recognizable-exchange-secret", first.GetSecretKey())
	assert.True(t, first.GetReadonly())
	assert.Equal(t, "recognizable-passphrase", applyRequest.GetExchangeKeys()[1].GetPassphrase())
	assert.Equal(t, 3, applied.Secrets)
	assert.Equal(t, []ExchangeKeyResult{
		{SecretID: "exchange-binance-production-readonly", State: "created", ReadOnly: true, Note: "已写入密钥管理"},
		{SecretID: "exchange-okx-testnet-main", State: "deleted", Note: "已在管理台删除，未重建"},
	}, applied.ExchangeKeys)

	status, err := New(forwarder).Status(context.Background(), snapshot)
	require.NoError(t, err)
	assert.Len(t, statusRequest.GetExchangeKeys(), 2, "状态查询也要带上密钥，Admin 才能按固定 ID 判断")
	assert.Equal(t, "completed", status.State)
	assert.Equal(t, "deleted", status.ExchangeKeys[1].State)

	// 打印出来的结果里没有密钥值。
	raw, err := json.Marshal(applied)
	require.NoError(t, err)
	for _, value := range []string{"recognizable-api-key", "recognizable-exchange-secret", "recognizable-passphrase"} {
		assert.NotContains(t, string(raw), value)
	}
}

// 旧的 Admin 不认识 exchange_keys 字段，会静默丢掉它并返回成功：CLI 要能发现，不能让用户以为密钥已经写入。
func TestApplyAndStatusFailWhenAdminIgnoresExchangeKeys(t *testing.T) {
	extra := `
[[exchange_keys]]
name = "readonly"
exchange = "binance"
environment = "production"
api_key = "k"
secret_key = "s"
readonly = true
`
	snapshot := clientSecrets(t, extra)
	forwarder := &fakeForwarder{handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var response []byte
		if request.URL.Path == "/trpc.moox.admin.Setup/ApplySetup" {
			response, _ = protojson.Marshal(&pb.ApplySetupRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Action: "created", Users: 1, Secrets: 1})
		} else {
			response, _ = protojson.Marshal(&pb.GetSetupStatusRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, State: "completed", Users: 1, Secrets: 1})
		}
		_, _ = w.Write(response)
	})}
	_, err := New(forwarder).Apply(context.Background(), snapshot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "先升级管理后台")
	_, err = New(forwarder).Status(context.Background(), snapshot)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup_admin_outdated")

	// 没有交易所密钥时不受影响。
	plain := clientSecrets(t, "")
	_, err = New(forwarder).Apply(context.Background(), plain)
	require.NoError(t, err)
}
