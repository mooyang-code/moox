package test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	adminconfig "github.com/mooyang-code/moox/modules/admin/internal/config"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	setupdomain "github.com/mooyang-code/moox/modules/admin/internal/service/setup"
	setuprpc "github.com/mooyang-code/moox/modules/admin/internal/service/setup/rpc"
	adminspace "github.com/mooyang-code/moox/modules/admin/internal/service/space"
	sshmodel "github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSetupPrivateHTTPTransactionE2E(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "admin-encryption-key")
	const encryptionKey = "setup-e2e-encryption-key"
	require.NoError(t, os.WriteFile(keyPath, []byte(encryptionKey), 0o600))
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())

	manager := database.NewManager()
	require.NoError(t, manager.Initialize(&adminconfig.DatabaseConfig{Path: filepath.Join(dir, "admin.db")}))
	sqlDB, err := manager.GetDB().DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, manager.GetDB().Exec(adminschema.AdminSQL()).Error)

	rpcService := setuprpc.NewService(setupdomain.NewService(manager.GetDB(), encryptionKey))
	privateMux := http.NewServeMux()
	privateMux.HandleFunc("/trpc.moox.admin.Setup/ApplySetup", protoHandler(
		func() proto.Message { return &pb.ApplySetupReq{} },
		func(ctx context.Context, request proto.Message) (proto.Message, error) {
			return rpcService.ApplySetup(ctx, request.(*pb.ApplySetupReq))
		},
	))
	privateMux.HandleFunc("/trpc.moox.admin.Setup/GetSetupStatus", protoHandler(
		func() proto.Message { return &pb.GetSetupStatusReq{} },
		func(ctx context.Context, request proto.Message) (proto.Message, error) {
			return rpcService.GetSetupStatus(ctx, request.(*pb.GetSetupStatusReq))
		},
	))
	privateServer := httptest.NewServer(privateMux)
	t.Cleanup(privateServer.Close)

	request := setupApplyRequest()
	apply := &pb.ApplySetupRsp{}
	postSetupProto(t, privateServer.URL+"/trpc.moox.admin.Setup/ApplySetup", request, apply)
	require.Equal(t, pb.ErrorCode_SUCCESS, apply.GetRetInfo().GetCode())
	require.Equal(t, "created", apply.GetAction())
	require.Equal(t, int32(2), apply.GetSpaces())
	require.Equal(t, int32(2), apply.GetSpacesCreated())

	var user authmodel.User
	require.NoError(t, manager.GetDB().Where("c_username = ?", "admin").First(&user).Error)
	assert.True(t, mooxsecurity.VerifyPassword("admin-e2e-password", user.PasswordHash))
	var secret secretmodel.Secret
	require.NoError(t, manager.GetDB().Where("c_secret_id = ?", "tencent-default").First(&secret).Error)
	assert.NotEqual(t, "cloud-e2e-secret", secret.SecretValue)
	decrypted, err := mooxsecurity.Decrypt(secret.SecretValue, encryptionKey)
	require.NoError(t, err)
	assert.Equal(t, "cloud-e2e-secret", decrypted)
	var hosts []sshmodel.SSHHost
	require.NoError(t, manager.GetDB().Order("c_address").Find(&hosts).Error)
	require.Len(t, hosts, 2)
	assert.NotEqual(t, "control-e2e-password", hosts[0].Password)
	var spaces []adminspace.Space
	require.NoError(t, manager.GetDB().Order("c_space_id").Find(&spaces).Error)
	require.Len(t, spaces, 2)
	assert.Equal(t, "crypto", spaces[0].SpaceID)
	assert.Equal(t, "crypto", spaces[0].Market)
	assert.Equal(t, "stock_cn", spaces[1].SpaceID)
	assert.Equal(t, "Asia/Shanghai", spaces[1].Timezone)

	retry := &pb.ApplySetupRsp{}
	postSetupProto(t, privateServer.URL+"/trpc.moox.admin.Setup/ApplySetup", request, retry)
	assert.Equal(t, "unchanged", retry.GetAction())
	assert.Equal(t, int32(2), retry.GetSpacesUnchanged())

	status := &pb.GetSetupStatusRsp{}
	postSetupProto(t, privateServer.URL+"/trpc.moox.admin.Setup/GetSetupStatus", setupStatusRequest(request), status)
	assert.Equal(t, "completed", status.GetState())
	assert.Zero(t, status.GetMissing())
	assert.Zero(t, status.GetConflicts())
	assert.Equal(t, int32(2), status.GetSpaces())

	require.NoError(t, manager.GetDB().Where("c_name = ?", "compute-1").Delete(&sshmodel.SSHHost{}).Error)
	partial := &pb.ApplySetupRsp{}
	postSetupProto(t, privateServer.URL+"/trpc.moox.admin.Setup/ApplySetup", request, partial)
	assert.Equal(t, "created", partial.GetAction())

	conflicting := proto.Clone(request).(*pb.ApplySetupReq)
	conflicting.TencentCloud.SecretKey = "different-cloud-secret"
	conflict := &pb.ApplySetupRsp{}
	postSetupProto(t, privateServer.URL+"/trpc.moox.admin.Setup/ApplySetup", conflicting, conflict)
	assert.Equal(t, "setup_conflict", conflict.GetRetInfo().GetMsg())

	var setupTables int64
	require.NoError(t, manager.GetDB().Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='t_system_setup'").Scan(&setupTables).Error)
	assert.Zero(t, setupTables)

	for _, publicSurface := range []string{"browser-admin", "node-gateway"} {
		t.Run(publicSurface+" rejects setup route", func(t *testing.T) {
			server := httptest.NewServer(http.NotFoundHandler())
			defer server.Close()
			response, err := http.Post(server.URL+"/trpc.moox.admin.Setup/ApplySetup", "application/json", bytes.NewReader(nil))
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(t, http.StatusNotFound, response.StatusCode)
		})
	}
}

func setupApplyRequest() *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: "admin", Password: "admin-e2e-password"},
		TencentCloud: &pb.SetupTencentCloud{SecretId: "AKID-e2e", SecretKey: "cloud-e2e-secret"},
		ControlHost:  &pb.SetupHost{Name: "control", Address: "192.0.2.10", Port: 22, Username: "ubuntu", Password: "control-e2e-password"},
		OtherHosts:   []*pb.SetupHost{{Name: "compute-1", Address: "192.0.2.11", Port: 22, Username: "ubuntu", Password: "compute-e2e-password"}},
		Spaces: []*pb.SetupSpace{
			{SpaceId: "stock_cn", Name: "A股市场", Owner: "quant", Market: "CN", Timezone: "Asia/Shanghai", Status: "active", AttributesJson: "{}"},
			{SpaceId: "crypto", Name: "加密货币市场", Owner: "quant", Market: "crypto", Timezone: "UTC", Status: "active", AttributesJson: "{}"},
		},
	}
}

func setupStatusRequest(request *pb.ApplySetupReq) *pb.GetSetupStatusReq {
	return &pb.GetSetupStatusReq{
		Admin: request.Admin, TencentCloud: request.TencentCloud,
		ControlHost: request.ControlHost, OtherHosts: request.OtherHosts, Spaces: request.Spaces,
	}
}

func protoHandler(newRequest func() proto.Message, call func(context.Context, proto.Message) (proto.Message, error)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
		if err != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		message := newRequest()
		if err := protojson.Unmarshal(body, message); err != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		response, err := call(request.Context(), message)
		if err != nil {
			http.Error(writer, "failed", http.StatusInternalServerError)
			return
		}
		raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(response)
		if err != nil {
			http.Error(writer, "failed", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(raw)
	}
}

func postSetupProto(t *testing.T, endpoint string, request, response proto.Message) {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	require.NoError(t, err)
	httpResponse, err := http.Post(endpoint, "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer httpResponse.Body.Close()
	require.Equal(t, http.StatusOK, httpResponse.StatusCode)
	body, err := io.ReadAll(httpResponse.Body)
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(body, response))
}
