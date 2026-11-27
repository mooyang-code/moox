package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type adminUserRow struct {
	UserID       string `gorm:"column:c_user_id"`
	Username     string `gorm:"column:c_username"`
	PasswordHash string `gorm:"column:c_password_hash"`
}

func TestAdminUserEnsureCreatesSuperAdminAndIsIdempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	first := runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "first password\n")
	assert.Equal(t, "created", first["action"])

	db := openUserTestDB(t, dbPath)
	row := readAdminUser(t, db, "admin")
	assert.True(t, mooxsecurity.VerifyPassword("first password", row.PasswordHash))
	firstHash := row.PasswordHash

	second := runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "different password\n")
	assert.Equal(t, "unchanged", second["action"])
	row = readAdminUser(t, db, "admin")
	assert.Equal(t, firstHash, row.PasswordHash)
	var count int64
	require.NoError(t, db.Table("t_users").Where("c_username = ?", "admin").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

// 管理员密码可以改：set-password 只改已有管理员，ensure 仍然只建不改。
func TestAdminPasswordCanBeChanged(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	runAdminUserForTest(t, []string{"user", "ensure", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "first password\n")
	changed := runAdminUserForTest(t, []string{"user", "set-password", "--db-path", dbPath, "--username", "admin", "--password-stdin"}, "new password\n")
	assert.Equal(t, "updated", changed["action"])
	db := openUserTestDB(t, dbPath)
	assert.True(t, mooxsecurity.VerifyPassword("new password", readAdminUser(t, db, "admin").PasswordHash))

	var stdout, stderr bytes.Buffer
	err := runAdminUserCommand([]string{"user", "set-password", "--db-path", dbPath, "--username", "nobody", "--password-stdin"}, strings.NewReader("x\n"), &stdout, &stderr)
	require.ErrorContains(t, err, "不存在")
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
