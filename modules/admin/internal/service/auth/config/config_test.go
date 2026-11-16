package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadFrom 在临时目录里写 config/console.yaml 后加载。
func loadFrom(t *testing.T, content string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config", "console.yaml"), []byte(content), 0o644))
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	return LoadConfig()
}

func TestConfig_LoadConfig_ValidYAML_ShouldParseFields(t *testing.T) {
	cfg, err := loadFrom(t, "session:\n  ttl: 12h\nsecurity:\n  max_login_attempt: 3\n  lock_duration: 10m\n")
	require.NoError(t, err)
	assert.Equal(t, 12*time.Hour, cfg.Session.TTL)
	assert.Equal(t, 3, cfg.Security.MaxLoginAttempt)
	assert.Equal(t, 10*time.Minute, cfg.Security.LockDuration)
}

func TestConfig_LoadConfig_DefaultsKeepOriginalLockRules(t *testing.T) {
	cfg, err := loadFrom(t, "console: {}\n")
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, cfg.Session.TTL)
	assert.Equal(t, 5, cfg.Security.MaxLoginAttempt)
	assert.Equal(t, 15*time.Minute, cfg.Security.LockDuration)
}

func TestConfig_LoadConfig_RejectsNonPositiveValues(t *testing.T) {
	for _, content := range []string{
		"session:\n  ttl: 0s\n",
		"security:\n  max_login_attempt: 0\n",
		"security:\n  lock_duration: 0s\n",
	} {
		_, err := loadFrom(t, content)
		require.Error(t, err, content)
	}
}

func TestConfig_LoadConfig_MissingFile_ShouldReturnError(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	_, err = LoadConfig()
	assert.Error(t, err)
}

func TestConfig_LoadConfig_InvalidYAML_ShouldReturnError(t *testing.T) {
	_, err := loadFrom(t, "session: [")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "解析YAML失败")
	// Linux 安装门禁靠这一条制造 Admin 启动失败并验证回滚。
	_, err = loadFrom(t, "session:\n  ttl: invalid-duration\n")
	assert.ErrorContains(t, err, "解析YAML失败")
}
