package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, isMaskedSecret("ab"+maskChar+"cd"))
}

func TestMaskSecretValue(t *testing.T) {
	assert.Equal(t, "", maskSecretValue(""))
	assert.Equal(t, maskChar+maskChar+maskChar, maskSecretValue("abc"))
	assert.Equal(t, maskChar+maskChar+maskChar+maskChar, maskSecretValue("abcd"))

	got := maskSecretValue("abcdefgh")
	assert.True(t, strings.HasPrefix(got, "ab"))
	assert.True(t, strings.HasSuffix(got, "gh"))
	assert.Contains(t, got, maskChar)

	long := maskSecretValue("abcdefghijklmnop")
	assert.True(t, strings.HasPrefix(long, "abcd"))
	assert.True(t, strings.HasSuffix(long, "mnop"))
	assert.Contains(t, long, maskChar)

	veryLong := maskSecretValue("abcdefghijklmnopqrstuvwxyz")
	assert.True(t, strings.HasPrefix(veryLong, "abcd"))
	assert.True(t, strings.HasSuffix(veryLong, "wxyz"))
}

func TestFormatTimeHelpers(t *testing.T) {
	assert.Equal(t, "", formatTime(time.Time{}))
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	assert.Equal(t, "2024-01-02 03:04:05", formatTime(now))

	assert.Equal(t, "", formatTimePtr(nil))
	zero := time.Time{}
	assert.Equal(t, "", formatTimePtr(&zero))
	assert.Equal(t, "2024-01-02 03:04:05", formatTimePtr(&now))
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
		LastUsedAt:  &used,
		LastUsedBy:  "u",
		Creator:     "c1",
		CreateTime:  used,
		ModifyTime:  used,
	}
	pbSecret := secretModelToPB(m)
	require.NotNil(t, pbSecret)
	assert.Equal(t, "sec_1", pbSecret.SecretId)
	assert.NotEqual(t, "super-secret-value", pbSecret.SecretValue)
	assert.Contains(t, pbSecret.SecretValue, maskChar)
	assert.Equal(t, "2024-02-03 04:05:06", pbSecret.LastUsedAt)

	plain := secretModelToPlainPB(m)
	require.NotNil(t, plain)
	assert.Equal(t, "super-secret-value", plain.SecretValue)

	back := secretPBToModel(pbSecret)
	require.NotNil(t, back)
	assert.Equal(t, "sec_1", back.SecretID)
	assert.Equal(t, pbSecret.SecretValue, back.SecretValue)
}
