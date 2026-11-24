package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/setup"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeSetupService struct {
	applyResult setup.Result
	applyErr    error
	status      setup.Status
	statusErr   error
	manifest    setup.Manifest
}

func (f *fakeSetupService) Apply(_ context.Context, manifest setup.Manifest) (setup.Result, error) {
	f.manifest = manifest
	return f.applyResult, f.applyErr
}

func (f *fakeSetupService) Inspect(_ context.Context, manifest setup.Manifest) (setup.Status, error) {
	f.manifest = manifest
	return f.status, f.statusErr
}

func setupRequest() *pb.ApplySetupReq {
	return &pb.ApplySetupReq{
		Admin:        &pb.SetupAdmin{Username: "admin", Password: "recognizable-admin-password"},
		TencentCloud: &pb.SetupTencentCloud{SecretId: "recognizable-secret-id", SecretKey: "recognizable-secret-key"},
	}
}

func statusRequest() *pb.GetSetupStatusReq {
	request := setupRequest()
	return &pb.GetSetupStatusReq{Admin: request.Admin, TencentCloud: request.TencentCloud}
}

func TestApplySetupMapsRequestAndSanitizesResponse(t *testing.T) {
	fake := &fakeSetupService{applyResult: setup.Result{Action: "created", Users: 1, Secrets: 1}}
	response, err := NewService(fake).ApplySetup(context.Background(), setupRequest())
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	assert.Equal(t, "created", response.GetAction())
	assert.Equal(t, "admin", fake.manifest.Admin.Username)
	assert.Equal(t, "recognizable-secret-key", fake.manifest.TencentCloud.SecretKey)

	raw, err := protojson.Marshal(response)
	require.NoError(t, err)
	for _, secret := range []string{"recognizable-admin-password", "recognizable-secret-id", "recognizable-secret-key"} {
		assert.NotContains(t, string(raw), secret)
	}
}

func TestApplySetupMapsDomainErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code pb.ErrorCode
		msg  string
	}{
		{name: "invalid", err: setup.ErrInvalid, code: pb.ErrorCode_INVALID_PARAM, msg: "setup_invalid"},
		{name: "admin missing", err: setup.ErrAdminMissing, code: pb.ErrorCode_INVALID_PARAM, msg: "setup_admin_missing：还没有管理员，先用 setup deploy 部署控制机（会建管理员）"},
		{name: "storage", err: setup.ErrStorage, code: pb.ErrorCode_INNER_ERR, msg: "setup_storage_failed"},
		{name: "unknown", err: errors.New("recognizable-secret-key"), code: pb.ErrorCode_INNER_ERR, msg: "setup_storage_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			response, err := NewService(&fakeSetupService{applyErr: tt.err}).ApplySetup(context.Background(), setupRequest())
			require.NoError(t, err)
			assert.Equal(t, tt.code, response.GetRetInfo().GetCode())
			assert.Equal(t, tt.msg, response.GetRetInfo().GetMsg())
			assert.NotContains(t, response.GetRetInfo().GetMsg(), "recognizable-secret-key")
		})
	}
}

func TestGetSetupStatusReturnsRecordDerivedState(t *testing.T) {
	fake := &fakeSetupService{status: setup.Status{State: "incomplete", Users: 1, Secrets: 1, Missing: 1}}
	response, err := NewService(fake).GetSetupStatus(context.Background(), statusRequest())
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
	assert.Equal(t, "incomplete", response.GetState())
	assert.Equal(t, int32(1), response.GetMissing())
	assert.Equal(t, "recognizable-admin-password", fake.manifest.Admin.Password)

	raw, err := protojson.Marshal(response)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "recognizable-admin-password")
	assert.NotContains(t, string(raw), "recognizable-secret-key")
}

func TestApplySetupRejectsMissingSections(t *testing.T) {
	response, err := NewService(&fakeSetupService{}).ApplySetup(context.Background(), &pb.ApplySetupReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode())
}


func TestSetupMapsExchangeKeysAndNeverEchoesTheirValues(t *testing.T) {
	keys := []*pb.SetupExchangeKey{
		{Name: "readonly", Exchange: "binance", Environment: "production", ApiKey: "recognizable-api-key", SecretKey: "recognizable-exchange-secret", Readonly: true},
		{Name: "main", Exchange: "okx", Environment: "testnet", ApiKey: "okx-key", SecretKey: "okx-secret", Passphrase: "recognizable-passphrase"},
	}
	results := []setup.ExchangeKeyResult{
		{SecretID: "exchange-binance-production-readonly", State: setup.ExchangeKeyCreated, ReadOnly: true},
		{SecretID: "exchange-okx-testnet-main", State: setup.ExchangeKeyExists},
	}
	fake := &fakeSetupService{
		applyResult: setup.Result{Action: "created", Users: 1, Secrets: 3, ExchangeKeys: results},
		status:      setup.Status{State: "completed", Users: 1, Secrets: 3, ExchangeKeys: results},
	}
	request := setupRequest()
	request.ExchangeKeys = keys
	response, err := NewService(fake).ApplySetup(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, int32(3), response.GetSecrets())
	require.Len(t, fake.manifest.ExchangeKeys, 2)
	assert.Equal(t, setup.ExchangeKey{Name: "readonly", Exchange: "binance", Environment: "production", APIKey: "recognizable-api-key", SecretKey: "recognizable-exchange-secret", ReadOnly: true}, fake.manifest.ExchangeKeys[0])
	assert.Equal(t, "recognizable-passphrase", fake.manifest.ExchangeKeys[1].Passphrase)
	require.Len(t, response.GetExchangeKeys(), 2)
	assert.Equal(t, "exchange-binance-production-readonly", response.GetExchangeKeys()[0].GetSecretId())
	assert.Equal(t, "created", response.GetExchangeKeys()[0].GetState())
	assert.True(t, response.GetExchangeKeys()[0].GetReadonly())
	assert.Equal(t, "exists", response.GetExchangeKeys()[1].GetState())

	statusReq := statusRequest()
	statusReq.ExchangeKeys = keys
	status, err := NewService(fake).GetSetupStatus(context.Background(), statusReq)
	require.NoError(t, err)
	assert.Equal(t, "completed", status.GetState())
	require.Len(t, fake.manifest.ExchangeKeys, 2)
	require.Len(t, status.GetExchangeKeys(), 2)

	for _, message := range []*pb.ApplySetupRsp{response} {
		raw, err := protojson.Marshal(message)
		require.NoError(t, err)
		for _, value := range []string{"recognizable-api-key", "recognizable-exchange-secret", "recognizable-passphrase"} {
			assert.NotContains(t, string(raw), value)
		}
	}

	// 空的密钥项是非法请求。
	request.ExchangeKeys = []*pb.SetupExchangeKey{nil}
	response, err = NewService(fake).ApplySetup(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, response.GetRetInfo().GetCode())
}
