package rpc

import (
	"context"
	"testing"

	secret "github.com/mooyang-code/moox/modules/admin/internal/service/secret"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type readonlyKeyFake struct {
	secret.Service
	records map[string]*model.Secret
	asked   []string
}

func (f *readonlyKeyFake) GetSecret(_ context.Context, secretID string) (*model.Secret, error) {
	f.asked = append(f.asked, secretID)
	if record, ok := f.records[secretID]; ok {
		return record, nil
	}
	// 与 DAO 一致：不存在或已软删除都是记录不存在。
	return nil, gorm.ErrRecordNotFound
}

func readonlyRecord(status string) *model.Secret {
	return &model.Secret{
		SecretID: "exchange-binance-production-readonly", Name: "readonly", Category: "exchange", Provider: "binance", SecretType: "api_key",
		KeyID: "public-readonly-key", SecretValue: "very-secret-value", ExtraConfig: `{"passphrase":"p"}`, Status: status,
	}
}

func TestGetReadonlyExchangeKeyReturnsOnlyPublicIdentifier(t *testing.T) {
	fake := &readonlyKeyFake{records: map[string]*model.Secret{"exchange-binance-production-readonly": readonlyRecord("active")}}
	rsp, err := NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "public-readonly-key", rsp.GetKeyId())
	require.Equal(t, []string{"exchange-binance-production-readonly"}, fake.asked, "只查固定 ID，不能被调用方指定别的秘钥")
	// 响应里不能出现秘钥值或额外配置。
	require.NotContains(t, rsp.String(), "very-secret-value")
	require.NotContains(t, rsp.String(), "passphrase")

	// 状态为空视为启用（与 GetSecretValue 一致）。
	fake.records["exchange-binance-production-readonly"] = readonlyRecord("")
	rsp, err = NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
	require.NoError(t, err)
	require.Equal(t, "public-readonly-key", rsp.GetKeyId())
}

func TestGetReadonlyExchangeKeyNotFoundWhenMissingDeletedOrDisabled(t *testing.T) {
	for name, records := range map[string]map[string]*model.Secret{
		"不存在或已删除":   {},
		"已停用":       {"exchange-binance-production-readonly": readonlyRecord("inactive")},
		"没有 key_id": {"exchange-binance-production-readonly": func() *model.Secret { r := readonlyRecord("active"); r.KeyID = ""; return r }()},
	} {
		rsp, err := NewService(&readonlyKeyFake{records: records}).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode(), name)
		require.Contains(t, rsp.GetRetInfo().GetMsg(), "没有配置只读 Key", name)
		require.Empty(t, rsp.GetKeyId(), name)
	}
}

func TestGetReadonlyExchangeKeyRejectsUnknownExchange(t *testing.T) {
	fake := &readonlyKeyFake{records: map[string]*model.Secret{}}
	for _, exchange := range []string{"", "BINANCE", "kraken", "binance-production", "../x"} {
		rsp, err := NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: exchange})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), exchange)
	}
	require.Empty(t, fake.asked, "非法交易所不能走到存储")
}
