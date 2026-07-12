package crypto

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAESEncryptDecrypt_ValidKey_ShouldRoundTrip(t *testing.T) {
	key := "0123456789abcdef" // 16 bytes AES-128
	cipherText, err := AESEncrypt("hello-moox", key)
	require.NoError(t, err)
	assert.NotEmpty(t, cipherText)

	plain, err := AESDecrypt(cipherText, key)
	require.NoError(t, err)
	assert.Equal(t, "hello-moox", plain)
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
	_, err := AESDecrypt("YQ==", "0123456789abcdef") // "a"
	assert.Error(t, err)
}

func TestHashPassword_AndVerify(t *testing.T) {
	salt := GenerateSalt()
	require.NotEmpty(t, salt)
	hash := HashPassword("secret", salt)
	assert.True(t, VerifyPasswordHash("secret", salt, hash))
	assert.False(t, VerifyPasswordHash("wrong", salt, hash))
}

func TestGenerateUserID_ShouldReturnUUID(t *testing.T) {
	id := GenerateUserID()
	assert.NotEmpty(t, id)
	assert.Contains(t, id, "-")
}

func TestDeriveEncryptionKey_ShouldBe32Bytes(t *testing.T) {
	key := DeriveEncryptionKey("salt", 123456)
	assert.Len(t, key, 32)
}

func TestDecryptPassword_RoundTripWithDerivedKey(t *testing.T) {
	salt := "dyn-salt"
	ts := time.Now().Unix()
	key := DeriveEncryptionKey(salt, ts)

	// Encrypt with byte key via AESEncrypt path using string key of 32 bytes from hex
	// Use AESEncryptWithKey by encrypting manually through AESEncrypt after converting:
	// Encrypt plaintext with derived key using AESEncryptWithKey's inverse:
	plain := "p@ssw0rd"
	// Build ciphertext using AESEncrypt with hex of key if length wrong — use AESEncryptWithKey reverse:
	// Encrypt via temporary helper: AESEncrypt needs string key 16/24/32.
	keyStr := string(key)
	cipherText, err := AESEncrypt(plain, keyStr)
	require.NoError(t, err)

	got, err := AESDecryptWithKey(cipherText, key)
	require.NoError(t, err)
	assert.Equal(t, plain, got)

	decrypted, err := DecryptPassword(cipherText, salt, ts)
	require.NoError(t, err)
	assert.Equal(t, plain, decrypted)
}

func TestValidateEncryptedPassword_Valid_ShouldSucceed(t *testing.T) {
	userSalt := "user-salt"
	password := "secret"
	storedHash := HashPassword(password, userSalt)
	dynSalt := "dyn"
	ts := int64(1700000000)
	key := DeriveEncryptionKey(dynSalt, ts)
	cipherText, err := AESEncrypt(password, string(key))
	require.NoError(t, err)

	ok := ValidateEncryptedPassword(context.Background(), storedHash, userSalt, dynSalt, ts, cipherText)
	assert.True(t, ok)
}

func TestValidateEncryptedPassword_BadCipher_ShouldFail(t *testing.T) {
	ok := ValidateEncryptedPassword(context.Background(), "hash", "salt", "dyn", 1, "bad")
	assert.False(t, ok)
}

func TestGenerateAccessToken_AndValidate(t *testing.T) {
	secret := "jwt-secret-key"
	token, err := GenerateAccessToken("u1", "alice", 1, secret, time.Hour)
	require.NoError(t, err)

	claims, err := ValidateAccessToken(token, secret)
	require.NoError(t, err)
	assert.Equal(t, "u1", claims.UserID)
	assert.Equal(t, "alice", claims.Username)
	assert.Equal(t, int32(1), claims.Role)
	assert.Equal(t, TokenTypeAccess, claims.TokenType)
}

func TestValidateAccessToken_WrongSecret_ShouldFail(t *testing.T) {
	token, err := GenerateAccessToken("u1", "alice", 1, "secret-a", time.Hour)
	require.NoError(t, err)
	_, err = ValidateAccessToken(token, "secret-b")
	assert.Error(t, err)
}

func TestParseToken_Invalid_ShouldFail(t *testing.T) {
	_, err := ParseToken("not.a.jwt", "secret")
	assert.Error(t, err)
}

func TestGetJWTIssuer_FromEnv(t *testing.T) {
	t.Setenv("MOOX_JWT_ISSUER", "custom-issuer")
	assert.Equal(t, "custom-issuer", getJWTIssuer())
	require.NoError(t, os.Unsetenv("MOOX_JWT_ISSUER"))
	assert.Equal(t, "moox-admin", getJWTIssuer())
}
