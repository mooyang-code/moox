package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type readonlyKeyFake struct {
	secretStore
	records     map[string]*model.Secret
	listRecords []model.Secret
	asked       []string
	listFilters []*dao.SecretFilters
}

func (f *readonlyKeyFake) FindByID(_ context.Context, secretID string) (*model.Secret, error) {
	f.asked = append(f.asked, secretID)
	if record, ok := f.records[secretID]; ok {
		return record, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (f *readonlyKeyFake) List(_ context.Context, offset, limit int, filters *dao.SecretFilters) ([]model.Secret, int64, error) {
	if filters != nil {
		copyFilter := *filters
		f.listFilters = append(f.listFilters, &copyFilter)
	}
	var source []model.Secret
	if f.listRecords != nil {
		source = f.listRecords
	} else {
		for _, rec := range f.records {
			source = append(source, *rec)
		}
	}
	var filtered []model.Secret
	for _, rec := range source {
		if filters != nil {
			if filters.Category != "" && rec.Category != filters.Category {
				continue
			}
			if filters.Provider != "" && !strings.EqualFold(rec.Provider, filters.Provider) {
				continue
			}
			if filters.Status != "" && rec.Status != filters.Status {
				continue
			}
		}
		filtered = append(filtered, rec)
	}
	total := int64(len(filtered))
	if offset >= len(filtered) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[offset:end], total, nil
}

func readonlyRecord(secretID, status, extra string) *model.Secret {
	return &model.Secret{
		SecretID:    secretID,
		Name:        "币安只读",
		Category:    "exchange",
		Provider:    "binance",
		SecretType:  "api_key",
		KeyID:       "public-readonly-key",
		SecretValue: "very-secret-value",
		ExtraConfig: extra,
		Status:      status,
	}
}

func TestGetReadonlyExchangeKeyMatchesByAttributesAndNotFixedID(t *testing.T) {
	// 管理台新建的随机 UUID 密钥（带 environment=production 与 readonly=true）能被找到，且只返回 key_id。
	fake := &readonlyKeyFake{
		listRecords: []model.Secret{
			*readonlyRecord("rand-trade", "active", `{"environment":"production"}`),
			*readonlyRecord("rand-testnet", "active", `{"environment":"testnet","readonly":"true"}`),
			*readonlyRecord("rand-disabled", "inactive", `{"environment":"production","readonly":"true"}`),
			*readonlyRecord("rand-prod-ro", "active", `{"environment":"production","readonly":"true","passphrase":"p"}`),
		},
	}
	rsp, err := NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "public-readonly-key", rsp.GetKeyId())
	require.Equal(t, []*dao.SecretFilters{{Category: "exchange", Provider: "binance"}}, fake.listFilters, "按类别与厂商列出后在内存解密过滤")
	require.NotContains(t, rsp.String(), "very-secret-value")
	require.NotContains(t, rsp.String(), "passphrase")
}

func TestGetReadonlyExchangeKeyTreatsMissingEnvironmentAsProduction(t *testing.T) {
	// 旧备份导回来的密钥（没有 environment 字段，只有 readonly=true）视为正式环境，仍能被找到。
	for name, record := range map[string]*model.Secret{
		"缺省环境":        readonlyRecord("exchange-binance-production-readonly", "active", `{"readonly":"true"}`),
		"空状态+缺省环境":    readonlyRecord("sec-legacy", "", `{"readonly":"true"}`),
		"布尔 readonly": readonlyRecord("sec-bool", "active", `{"readonly":true,"environment":""}`),
	} {
		fake := &readonlyKeyFake{listRecords: []model.Secret{*record}}
		rsp, err := NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), name)
		require.Equal(t, "public-readonly-key", rsp.GetKeyId(), name)
	}
}

func TestGetReadonlyExchangeKeyNotFoundWhenNoMatchingRecord(t *testing.T) {
	for name, records := range map[string][]model.Secret{
		"空列表":       nil,
		"已停用":       {*readonlyRecord("sec-1", "inactive", `{"environment":"production","readonly":"true"}`)},
		"仅交易密钥":     {*readonlyRecord("sec-2", "active", `{"environment":"production"}`)},
		"readonly假": {*readonlyRecord("sec-3", "active", `{"environment":"production","readonly":"false"}`)},
		"测试网只读":     {*readonlyRecord("sec-4", "active", `{"environment":"testnet","readonly":"true"}`)},
		"没有 key_id": {func() model.Secret {
			r := *readonlyRecord("sec-5", "active", `{"readonly":"true"}`)
			r.KeyID = ""
			return r
		}()},
		"其他交易所": {func() model.Secret {
			r := *readonlyRecord("sec-6", "active", `{"readonly":"true"}`)
			r.Provider = "okx"
			return r
		}()},
		"非法 JSON": {*readonlyRecord("sec-7", "active", `not json`)},
	} {
		rsp, err := NewService(&readonlyKeyFake{listRecords: records}).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: "binance"})
		require.NoError(t, err, name)
		require.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode(), name)
		require.Contains(t, rsp.GetRetInfo().GetMsg(), "没有配置只读 Key", name)
		require.Empty(t, rsp.GetKeyId(), name)
	}
}

func TestGetReadonlyExchangeKeyRejectsUnknownExchange(t *testing.T) {
	fake := &readonlyKeyFake{listRecords: []model.Secret{}}
	for _, exchange := range []string{"", "BINANCE", "kraken", "binance-production", "../x"} {
		rsp, err := NewService(fake).GetReadonlyExchangeKey(context.Background(), &pb.GetReadonlyExchangeKeyReq{Exchange: exchange})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), exchange)
	}
	require.Empty(t, fake.listFilters, "非法交易所不能走到存储")
}
