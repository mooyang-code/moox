package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESEncryptDecrypt_ValidKey_ShouldRoundTrip(t *testing.T) {
	key := "0123456789abcdef" // 16 bytes
	cipherText, err := AESEncrypt("trade-secret", key)
	require.NoError(t, err)

	plain, err := AESDecrypt(cipherText, key)
	require.NoError(t, err)
	assert.Equal(t, "trade-secret", plain)
}

func TestAESEncryptDecrypt_HumanReadableKey_ShouldDeriveAndRoundTrip(t *testing.T) {
	key := "moox-admin-secret-key-32bytes" // not 16/24/32
	cipherText, err := AESEncrypt("api-key", key)
	require.NoError(t, err)
	plain, err := AESDecrypt(cipherText, key)
	require.NoError(t, err)
	assert.Equal(t, "api-key", plain)
}

func TestAESEncrypt_EmptyPlaintext_ShouldReturnError(t *testing.T) {
	_, err := AESEncrypt("", "0123456789abcdef")
	assert.Error(t, err)
}

func TestAESDecrypt_EmptyCiphertext_ShouldReturnError(t *testing.T) {
	_, err := AESDecrypt("", "0123456789abcdef")
	assert.Error(t, err)
}

func TestAESDecrypt_InvalidBase64_ShouldReturnError(t *testing.T) {
	_, err := AESDecrypt("%%%", "0123456789abcdef")
	assert.Error(t, err)
}

func TestAESDecrypt_TooShort_ShouldReturnError(t *testing.T) {
	_, err := AESDecrypt("YQ==", "0123456789abcdef")
	assert.Error(t, err)
}

func TestMaskAPIKey_ShortAndLong(t *testing.T) {
	assert.Equal(t, "****", MaskAPIKey("short"))
	assert.Equal(t, "****", MaskAPIKey("12345678"))
	assert.Equal(t, "abcd****wxyz", MaskAPIKey("abcdefghijklwxyz"))
}

func TestDeriveKey_KnownLengths(t *testing.T) {
	assert.Len(t, deriveKey("0123456789abcdef"), 16)
	assert.Len(t, deriveKey("0123456789abcdef01234567"), 24)
	assert.Len(t, deriveKey("0123456789abcdef0123456789abcdef"), 32)
	assert.Len(t, deriveKey("odd-length-key"), 32)
}
