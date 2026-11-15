package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-filter/masking"
)

func cloudRecord() *model.Secret {
	return &model.Secret{
		SecretID: "cloud-tencent-main", Name: "腾讯云主账号", Category: "cloud", Provider: "tencent", SecretType: "api_key",
		KeyID: "AKIDfake", SecretValue: "cloud-secret-value", ExtraConfig: `{"note":"x"}`, Status: "active",
	}
}

// 类别 cloud、厂商 tencent、已启用的秘钥返回 key_id 与明文；脱敏过滤器不改写这个响应。
func TestGetCloudCredentialReturnsTencentCloudKey(t *testing.T) {
	fake := &readonlyKeyFake{records: map[string]*model.Secret{"cloud-tencent-main": cloudRecord()}}
	rsp, err := NewService(fake).GetCloudCredential(context.Background(), &pb.GetCloudCredentialReq{SecretId: "cloud-tencent-main"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "AKIDfake", rsp.GetCredential().GetKeyId())
	require.Equal(t, "cloud-secret-value", rsp.GetCredential().GetSecretValue())
	require.NotContains(t, rsp.String(), "note", "不返回额外配置")

	filtered, err := masking.ServerFilter()(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) { return rsp, nil })
	require.NoError(t, err)
	require.Equal(t, "cloud-secret-value", filtered.(*pb.GetCloudCredentialRsp).GetCredential().GetSecretValue())
}

// 交易所密钥、别的厂商、已停用（含空状态）、不完整或不存在的秘钥都被拒绝，响应里没有秘钥值。
func TestGetCloudCredentialRejectsAnythingButActiveTencentCloudKeys(t *testing.T) {
	for name, mutate := range map[string]func(*model.Secret){
		"交易所密钥":     func(r *model.Secret) { r.Category, r.Provider = "exchange", "binance" },
		"别的厂商":      func(r *model.Secret) { r.Provider = "aliyun" },
		"已停用":       func(r *model.Secret) { r.Status = "inactive" },
		"空状态":       func(r *model.Secret) { r.Status = "" },
		"没有 key_id": func(r *model.Secret) { r.KeyID = "" },
		"没有秘钥值":     func(r *model.Secret) { r.SecretValue = "" },
	} {
		record := cloudRecord()
		mutate(record)
		fake := &readonlyKeyFake{records: map[string]*model.Secret{"cloud-tencent-main": record}}
		rsp, err := NewService(fake).GetCloudCredential(context.Background(), &pb.GetCloudCredentialReq{SecretId: "cloud-tencent-main"})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), name)
		require.Nil(t, rsp.GetCredential(), name)
		require.NotContains(t, rsp.String(), "cloud-secret-value", name)
	}

	fake := &readonlyKeyFake{records: map[string]*model.Secret{}}
	rsp, err := NewService(fake).GetCloudCredential(context.Background(), &pb.GetCloudCredentialReq{SecretId: "missing"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
	rsp, err = NewService(fake).GetCloudCredential(context.Background(), &pb.GetCloudCredentialReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Equal(t, []string{"missing"}, fake.asked, "空的秘钥 ID 不走到存储")
}
