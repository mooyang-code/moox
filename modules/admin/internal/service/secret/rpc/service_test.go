package rpc

import (
	"context"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
	"unicode/utf8"
)

func TestRetHelpers(t *testing.T) {
	ok := retOK()
	require.NotNil(t, ok)
	assert.Equal(t, pb.ErrorCode_SUCCESS, ok.Code)
	assert.Equal(t, "success", ok.Msg)

	errInfo := retErr(pb.ErrorCode_INVALID_PARAM, "bad")
	require.NotNil(t, errInfo)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, errInfo.Code)
	assert.Equal(t, "bad", errInfo.Msg)
}

func TestIsMaskedSecret(t *testing.T) {
	assert.False(t, isMaskedSecret(""))
	assert.False(t, isMaskedSecret("plain-secret"))
	assert.False(t, isMaskedSecret("pass*word*with*stars"))
	assert.True(t, isMaskedSecret(maskChar))
	assert.True(t, isMaskedSecret("ab"+maskChar+"cd"))
	assert.True(t, isMaskedSecret(maskSecretValue("a")))
	assert.True(t, isMaskedSecret(maskSecretValue("密钥值")))
	assert.True(t, isMaskedSecret(maskSecretValue("我的测试密钥")))
	assert.True(t, isMaskedSecret(maskSecretValue("🔑🔐🔒🔓🗝")))
}

func TestMaskSecretValue(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: ""},
		{name: "single_ascii", input: "a", want: "•"},
		{name: "len_3_ascii", input: "abc", want: "•••"},
		{name: "len_4_ascii", input: "abcd", want: "••••"},
		{name: "len_5_ascii", input: "abcde", want: "ab•de"},
		{name: "len_8_ascii", input: "abcdefgh", want: "ab••••gh"},
		{name: "len_9_ascii", input: "abcdefghi", want: "abcd•fghi"},
		{name: "len_16_ascii", input: "abcdefghijklmnop", want: "abcd••••••••mnop"},
		{name: "len_26_ascii", input: "abcdefghijklmnopqrstuvwxyz", want: "abcd••••••••••••••••••wxyz"},
		{name: "short_chinese_3_runes", input: "密钥值", want: "•••"},
		{name: "short_chinese_4_runes", input: "秘密口令", want: "••••"},
		{name: "medium_chinese_6_runes", input: "我的测试密钥", want: "我的••密钥"},
		{name: "medium_chinese_8_runes", input: "一二三四五六七八", want: "一二••••七八"},
		{name: "long_chinese_10_runes", input: "这是一个很长的密钥串", want: "这是一个••的密钥串"},
		{name: "emoji_4_runes", input: "🔑🔐🔒🔓", want: "••••"},
		{name: "emoji_5_runes", input: "🔑🔐🔒🔓🗝", want: "🔑🔐•🔓🗝"},
		{name: "emoji_10_runes", input: "🔑🔐🔒🔓🗝🛡⚔🧲💎🚀", want: "🔑🔐🔒🔓••⚔🧲💎🚀"},
		{name: "mixed_utf8", input: "ak_中文🔑_1234", want: "ak_中•••1234"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskSecretValue(tc.input)
			assert.Equal(t, tc.want, got)
			assert.True(t, utf8.ValidString(got), "masked output must remain valid UTF-8")
			assert.Equal(t, utf8.RuneCountInString(tc.input), utf8.RuneCountInString(got))
			if tc.input != "" {
				assert.True(t, isMaskedSecret(got))
			} else {
				assert.False(t, isMaskedSecret(got))
			}
		})
	}
}

func TestCloudIsValidSecretCategory(t *testing.T) {
	assert.True(t, validCategories["cloud"])
}

func TestUnixMilli(t *testing.T) {
	assert.Zero(t, unixMilli(time.Time{}))
	at := time.Date(2024, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	assert.Equal(t, at.UnixMilli(), unixMilli(at))
	// 时区不影响毫秒值
	shanghai := time.FixedZone("UTC+8", 8*3600)
	assert.Equal(t, at.UnixMilli(), unixMilli(at.In(shanghai)))
}

func TestSecretModelConversions(t *testing.T) {
	assert.Nil(t, secretModelToPB(nil))
	assert.Nil(t, secretPBToModel(nil))

	used := time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)
	m := &model.Secret{
		ID:          7,
		SecretID:    "sec_1",
		Name:        "n",
		Description: "d",
		Category:    "c",
		Provider:    "p",
		SecretType:  "api_key",
		KeyID:       "k",
		SecretValue: "super-secret-value",
		ExtraConfig: `{}`,
		Status:      "active",
		Creator:     "c1",
		CreateTime:  used,
		ModifyTime:  used,
	}
	pbSecret := secretModelToPB(m)
	require.NotNil(t, pbSecret)
	assert.Equal(t, "sec_1", pbSecret.SecretId)
	assert.NotEqual(t, "super-secret-value", pbSecret.SecretValue)
	assert.Contains(t, pbSecret.SecretValue, maskChar)

	plain := secretModelToPlainPB(m)
	require.NotNil(t, plain)
	assert.Equal(t, "super-secret-value", plain.SecretValue)

	back := secretPBToModel(pbSecret)
	require.NotNil(t, back)
	assert.Equal(t, "sec_1", back.SecretID)
	assert.Equal(t, pbSecret.SecretValue, back.SecretValue)
}

type getSecretValueFakeService struct {
	secretStore
	gotSecretID string
	record      *model.Secret
}

func (f *getSecretValueFakeService) FindByID(ctx context.Context, secretID string) (*model.Secret, error) {
	f.gotSecretID = secretID
	return f.record, nil
}

func TestGetSecretValueReturnsPlainValueForServiceCall(t *testing.T) {
	fake := &getSecretValueFakeService{
		record: &model.Secret{
			SecretID:    "sec_binance",
			Name:        "binance-main",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "public-api-key",
			SecretValue: "plain-api-secret",
			ExtraConfig: `{"market_type":"swap"}`,
			Status:      "active",
		},
	}
	svc := NewService(fake)

	rsp, err := svc.GetSecretValue(serviceAuthCtx(), &pb.GetSecretValueReq{SecretId: "sec_binance"})
	if err != nil {
		t.Fatalf("GetSecretValue returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("GetSecretValue ret_info = %v", rsp.GetRetInfo())
	}
	if fake.gotSecretID != "sec_binance" {
		t.Fatalf("GetSecret called with %q, want sec_binance", fake.gotSecretID)
	}
	if got := rsp.GetSecret().GetSecretValue(); got != "plain-api-secret" {
		t.Fatalf("secret_value = %q, want plaintext", got)
	}
	if got := rsp.GetSecret().GetKeyId(); got != "public-api-key" {
		t.Fatalf("key_id = %q", got)
	}
}

func TestGetSecretValueRejectsInactiveSecret(t *testing.T) {
	svc := NewService(&getSecretValueFakeService{
		record: &model.Secret{
			SecretID:    "sec_disabled",
			Name:        "disabled",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "key",
			SecretValue: "secret",
			Status:      "inactive",
		},
	})

	rsp, err := svc.GetSecretValue(serviceAuthCtx(), &pb.GetSecretValueReq{SecretId: "sec_disabled"})
	if err != nil {
		t.Fatalf("GetSecretValue returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("GetSecretValue code = %v, want INVALID_PARAM", rsp.GetRetInfo().GetCode())
	}
}

func TestGetSecretValueReliesOnGatewayMethodAllowlist(t *testing.T) {
	svc := NewService(&getSecretValueFakeService{
		record: &model.Secret{
			SecretID:    "sec_binance",
			Name:        "binance-main",
			Category:    "exchange",
			Provider:    "binance",
			SecretType:  "api_key",
			KeyID:       "key",
			SecretValue: "secret",
			Status:      "active",
		},
	})

	rsp, err := svc.GetSecretValue(context.Background(), &pb.GetSecretValueReq{SecretId: "sec_binance"})
	if err != nil {
		t.Fatalf("GetSecretValue returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || rsp.GetSecret().GetSecretValue() != "secret" {
		t.Fatalf("GetSecretValue response = %#v, want plaintext success", rsp)
	}
}

func TestGetSecretValueMapsMissingSecret(t *testing.T) {
	svc := NewService(&getSecretValueFakeService{
		secretStore: &dao.SecretDAO{},
	})

	rsp, err := svc.GetSecretValue(context.Background(), &pb.GetSecretValueReq{})
	if err != nil {
		t.Fatalf("GetSecretValue returned transport error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("GetSecretValue code = %v, want INVALID_PARAM", rsp.GetRetInfo().GetCode())
	}
}

func serviceAuthCtx() context.Context {
	return context.Background()
}
