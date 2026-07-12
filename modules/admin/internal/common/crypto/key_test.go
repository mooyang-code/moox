package crypto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetEncryptionKey_Default_ShouldBe32Bytes(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	key := GetEncryptionKey()
	assert.Len(t, key, 32)
}

func TestGetEncryptionKey_FromEnv_ShouldRespectLength(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	key := GetEncryptionKey()
	assert.Equal(t, "0123456789abcdef0123456789abcdef", key)
}

func TestEnsureKeyLength_Short_ShouldHashExpand(t *testing.T) {
	got := ensureKeyLength("short", 32)
	assert.Len(t, got, 32)
}

func TestEnsureKeyLength_Long_ShouldTruncate(t *testing.T) {
	got := ensureKeyLength("abcdefghijklmnopqrstuvwxyz0123456789", 32)
	assert.Len(t, got, 32)
	assert.Equal(t, "abcdefghijklmnopqrstuvwxyz012345", got)
}

func TestEnsureKeyLength_Exact_ShouldKeep(t *testing.T) {
	in := "0123456789abcdef0123456789abcdef"
	assert.Equal(t, in, ensureKeyLength(in, 32))
}
