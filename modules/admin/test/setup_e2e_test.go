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
	"github.com/mooyang-code/moox/modules/admin/internal/service/database"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	secretrpc "github.com/mooyang-code/moox/modules/admin/internal/service/secret/rpc"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSetupInitialSecretE2E(t *testing.T) {
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

	dao := secretdao.NewSecretDAO(manager.GetDB(), encryptionKey)
	rpcService := secretrpc.NewService(dao)
	mux := http.NewServeMux()
	mux.HandleFunc("/trpc.moox.ops.SecretMgr/EnsureInitialSecret", protoHandler(
		func() proto.Message { return &pb.EnsureInitialSecretReq{} },
		func(ctx context.Context, request proto.Message) (proto.Message, error) {
			return rpcService.EnsureInitialSecret(ctx, request.(*pb.EnsureInitialSecretReq))
		},
	))
	mux.HandleFunc("/trpc.moox.ops.SecretMgr/GetReadonlyExchangeKey", protoHandler(
		func() proto.Message { return &pb.GetReadonlyExchangeKeyReq{} },
		func(ctx context.Context, request proto.Message) (proto.Message, error) {
			return rpcService.GetReadonlyExchangeKey(ctx, request.(*pb.GetReadonlyExchangeKeyReq))
		},
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	request := &pb.EnsureInitialSecretReq{
		Secret: &pb.Secret{
			SecretId:    secretmodel.TencentDefaultSecretID,
			Name:        "Tencent Cloud Default",
			Category:    "cloud",
			Provider:    "tencent",
			SecretType:  "api_key",
			KeyId:       "AKID-e2e",
			SecretValue: "cloud-e2e-secret",
		},
	}

	apply := &pb.EnsureInitialSecretRsp{}
	postSetupProto(t, srv.URL+"/trpc.moox.ops.SecretMgr/EnsureInitialSecret", request, apply)
	require.Equal(t, pb.ErrorCode_SUCCESS, apply.GetRetInfo().GetCode())
	require.Equal(t, "created", apply.GetAction())
	assert.Equal(t, secretmodel.TencentDefaultSecretID, apply.GetSecretId())

	var secret secretmodel.Secret
	require.NoError(t, manager.GetDB().Where("c_secret_id = ?", secretmodel.TencentDefaultSecretID).First(&secret).Error)
	assert.NotEqual(t, "cloud-e2e-secret", secret.SecretValue)
	decrypted, err := mooxsecurity.Decrypt(secret.SecretValue, encryptionKey)
	require.NoError(t, err)
	assert.Equal(t, "cloud-e2e-secret", decrypted)

	retry := &pb.EnsureInitialSecretRsp{}
	postSetupProto(t, srv.URL+"/trpc.moox.ops.SecretMgr/EnsureInitialSecret", request, retry)
	assert.Equal(t, "unchanged", retry.GetAction())

	// moox.secrets.toml 里的腾讯云密钥和管理台不一致：以管理台为准保留，不改写，返回 kept。
	differing := proto.Clone(request).(*pb.EnsureInitialSecretReq)
	differing.Secret.SecretValue = "different-cloud-secret"
	kept := &pb.EnsureInitialSecretRsp{}
	postSetupProto(t, srv.URL+"/trpc.moox.ops.SecretMgr/EnsureInitialSecret", differing, kept)
	assert.Equal(t, pb.ErrorCode_SUCCESS, kept.GetRetInfo().GetCode())
	assert.Equal(t, "kept", kept.GetAction())

	// 硬删除后重跑可重建，且库里不再有 t_system_setup 或 t_deleted_initial_secrets。
	require.NoError(t, dao.Delete(context.Background(), secretmodel.TencentDefaultSecretID))
	recreated := &pb.EnsureInitialSecretRsp{}
	postSetupProto(t, srv.URL+"/trpc.moox.ops.SecretMgr/EnsureInitialSecret", request, recreated)
	assert.Equal(t, "created", recreated.GetAction())

	for _, retired := range []string{"t_system_setup", "t_deleted_initial_secrets"} {
		var count int64
		require.NoError(t, manager.GetDB().Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", retired).Scan(&count).Error)
		assert.Zero(t, count, "retired table %s must not exist", retired)
	}

	// 管理台随机 ID 新建的币安正式环境只读密钥可被 GetReadonlyExchangeKey 按属性查到。
	createRsp, err := rpcService.CreateSecret(context.Background(), &pb.CreateSecretReq{
		Secret: &pb.Secret{
			Name:        "币安下架只读",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyId:       "ro-e2e-key",
			SecretValue: "ro-e2e-secret",
			ExtraConfig: `{"environment":"production","readonly":"true"}`,
		},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, createRsp.GetRetInfo().GetCode())
	roRsp := &pb.GetReadonlyExchangeKeyRsp{}
	postSetupProto(t, srv.URL+"/trpc.moox.ops.SecretMgr/GetReadonlyExchangeKey", &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"}, roRsp)
	require.Equal(t, pb.ErrorCode_SUCCESS, roRsp.GetRetInfo().GetCode())
	assert.Equal(t, "ro-e2e-key", roRsp.GetKeyId())
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
		encoded, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(response)
		if err != nil {
			http.Error(writer, "failed", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(encoded)
	}
}

func postSetupProto(t *testing.T, url string, request, response proto.Message) {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(request)
	require.NoError(t, err)
	httpResponse, err := http.Post(url, "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	defer httpResponse.Body.Close()
	require.Equal(t, http.StatusOK, httpResponse.StatusCode)
	body, err := io.ReadAll(httpResponse.Body)
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(body, response))
}
