package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

func cloudRecord() *model.Secret {
	return &model.Secret{
		SecretID: "cloud-tencent-main", Name: "腾讯云主账号", Category: "cloud", Provider: "tencent", SecretType: "api_key",
		KeyID: "AKIDfake", SecretValue: "cloud-secret-value", ExtraConfig: `{"note":"x"}`, Status: "active",
	}
}

// 类别 cloud、厂商 tencent、已启用的秘钥返回 key_id 与明文，不返回额外配置。
func TestGetCloudCredentialReturnsTencentCloudKey(t *testing.T) {
	fake := &readonlyKeyFake{records: map[string]*model.Secret{"cloud-tencent-main": cloudRecord()}}
	rsp, err := NewService(fake).GetCloudCredential(context.Background(), &pb.GetCloudCredentialReq{SecretId: "cloud-tencent-main"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "AKIDfake", rsp.GetCredential().GetKeyId())
	require.Equal(t, "cloud-secret-value", rsp.GetCredential().GetSecretValue())
	require.NotContains(t, rsp.String(), "note", "不返回额外配置")
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

// Trade 专用方法 GetExchangeCredential 只返回类别 exchange 且已启用的秘钥，拒绝云账号等其他类别与未启用秘钥。
func TestGetExchangeCredentialOnlyReturnsActiveExchangeSecrets(t *testing.T) {
	exchangeRecord := func() *model.Secret {
		return &model.Secret{
			SecretID: "exchange-binance", Name: "币安", Category: "exchange", Provider: "binance",
			SecretType: "api_key", KeyID: "k", SecretValue: "exchange-secret-value", ExtraConfig: `{"environment":"production"}`, Status: "active",
		}
	}

	fake := &readonlyKeyFake{records: map[string]*model.Secret{"exchange-binance": exchangeRecord(), "cloud-tencent-main": cloudRecord()}}
	svc := NewService(fake)

	rsp, err := svc.GetExchangeCredential(context.Background(), &pb.GetExchangeCredentialReq{SecretId: "exchange-binance"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "exchange-secret-value", rsp.GetSecret().GetSecretValue())
	require.Equal(t, "binance", rsp.GetSecret().GetProvider())

	for name, mutate := range map[string]func(*model.Secret){
		"云账号密钥": func(r *model.Secret) { r.Category, r.Provider = "cloud", "tencent" },
		"已停用":   func(r *model.Secret) { r.Status = "inactive" },
		"空状态":   func(r *model.Secret) { r.Status = "" },
	} {
		rec := exchangeRecord()
		mutate(rec)
		caseSvc := NewService(&readonlyKeyFake{records: map[string]*model.Secret{"exchange-binance": rec}})
		rejected, err := caseSvc.GetExchangeCredential(context.Background(), &pb.GetExchangeCredentialReq{SecretId: "exchange-binance"})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rejected.GetRetInfo().GetCode(), name)
		require.Nil(t, rejected.GetSecret(), name)
		require.NotContains(t, rejected.String(), "exchange-secret-value", name)
	}

	missing, err := svc.GetExchangeCredential(context.Background(), &pb.GetExchangeCredentialReq{SecretId: "missing"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())
	empty, err := svc.GetExchangeCredential(context.Background(), &pb.GetExchangeCredentialReq{})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, empty.GetRetInfo().GetCode())

	// GetSecretValue 只留给 moox-cli，不按组件类别限制，仍可读取云账号等秘钥。
	cliRsp, err := svc.GetSecretValue(context.Background(), &pb.GetSecretValueReq{SecretId: "cloud-tencent-main"})
	require.NoError(t, err)
	require.Equal(t, "cloud-secret-value", cliRsp.GetSecret().GetSecretValue())
}
