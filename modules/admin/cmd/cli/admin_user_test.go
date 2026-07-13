package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type adminUserRow struct {
	UserID       string `gorm:"column:c_user_id"`
	Username     string `gorm:"column:c_username"`
	PasswordHash string `gorm:"column:c_password_hash"`
	Role         int32  `gorm:"column:c_role"`
	Status       int32  `gorm:"column:c_status"`
	Nickname     string `gorm:"column:c_nickname"`
}

func TestAdminUserEnsureCreatesSuperAdminAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	first := runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "first password\n")
	assert.Equal(t, "created", first["action"])

	db := openUserTestDB(t, dbPath)
	row := readAdminUser(t, db, "admin")
	assert.Equal(t, int32(3), row.Role)
	assert.Equal(t, int32(1), row.Status)
	assert.True(t, mooxcrypto.VerifyPassword("first password", row.PasswordHash))
	firstHash := row.PasswordHash

	second := runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "different password\n")
	assert.Equal(t, "unchanged", second["action"])
	row = readAdminUser(t, db, "admin")
	assert.Equal(t, firstHash, row.PasswordHash)
	var count int64
	require.NoError(t, db.Table("t_users").Where("c_username = ?", "admin").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestAdminUserResetPasswordChangesOnlyHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "old password\n")
	db := openUserTestDB(t, dbPath)
	require.NoError(t, db.Table("t_users").Where("c_username = ?", "admin").Update("c_nickname", "Root User").Error)
	before := readAdminUser(t, db, "admin")

	result := runAdminUserForTest(t, []string{"user", "reset-password", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "new password\n")
	assert.Equal(t, "password_reset", result["action"])
	after := readAdminUser(t, db, "admin")
	assert.Equal(t, before.UserID, after.UserID)
	assert.Equal(t, before.Username, after.Username)
	assert.Equal(t, before.Role, after.Role)
	assert.Equal(t, before.Status, after.Status)
	assert.Equal(t, before.Nickname, after.Nickname)
	assert.NotEqual(t, before.PasswordHash, after.PasswordHash)
	assert.True(t, mooxcrypto.VerifyPassword("new password", after.PasswordHash))
}

func TestAdminUserRejectsUnsafePasswordInput(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{name: "password argv", args: []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password", "secret"}, want: "flag provided but not defined"},
		{name: "stdin opt in required", args: []string{"user", "ensure", "--db-path", dbPath, "--username", "admin"}, stdin: "secret\n", want: "--password-stdin is required"},
		{name: "empty", args: []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, want: "password cannot be empty"},
		{name: "bcrypt limit", args: []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, stdin: strings.Repeat("x", 73) + "\n", want: "72 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := runAdminUserCommand(tt.args, strings.NewReader(tt.stdin), &bytes.Buffer{}, &bytes.Buffer{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestAdminUserJSONDoesNotExposePasswordOrHash(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	secret := "do-not-print-this"
	var stdout bytes.Buffer
	require.NoError(t, runAdminUserCommand([]string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, strings.NewReader(secret+"\n"), &stdout, &bytes.Buffer{}))
	assert.NotContains(t, stdout.String(), secret)
	assert.NotContains(t, stdout.String(), "hash")
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
}

func TestRandomSecretEmitsRequestedBytesAsLowercaseHex(t *testing.T) {
	var stdout bytes.Buffer
	require.NoError(t, runRandomSecretCommand([]string{"random-secret", "--bytes", "32"}, &stdout, &bytes.Buffer{}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	value, ok := result["secret"].(string)
	require.True(t, ok)
	assert.Len(t, value, 64)
	_, err := hex.DecodeString(value)
	require.NoError(t, err)
	assert.Equal(t, strings.ToLower(value), value)
}

func TestRandomSecretRejectsUnsafeSizes(t *testing.T) {
	for _, size := range []string{"0", "-1", "4097"} {
		err := runRandomSecretCommand([]string{"random-secret", "--bytes", size}, &bytes.Buffer{}, &bytes.Buffer{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "between 1 and 4096")
	}
}

func runAdminUserForTest(t *testing.T, args []string, stdin string) map[string]any {
	t.Helper()
	var stdout bytes.Buffer
	require.NoError(t, runAdminUserCommand(args, strings.NewReader(stdin), &stdout, &bytes.Buffer{}))
	var result map[string]any
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	return result
}

func openUserTestDB(t *testing.T, dbPath string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func readAdminUser(t *testing.T, db *gorm.DB, username string) adminUserRow {
	t.Helper()
	var row adminUserRow
	require.NoError(t, db.Table("t_users").Where("c_username = ?", username).Take(&row).Error)
	return row
}
