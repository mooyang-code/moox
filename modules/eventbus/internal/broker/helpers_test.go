package broker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/eventbus/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigPublicHost(t *testing.T) {
	assert.False(t, configPublicHost("127.0.0.1:4222"))
	assert.False(t, configPublicHost("localhost"))
	assert.True(t, configPublicHost("eventbus.example.com"))
	assert.True(t, configPublicHost("eventbus.example.com:4222"))
}

func TestLoadUsersFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	content := "users:\n  - username: svc\n    password: secret\n    permissions:\n      publish:\n        allow: [\"moox.>\"]\n      subscribe:\n        allow: [\"moox.>\"]\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	users, err := loadUsersFile(path)
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "svc", users[0].Username)
	_, err = loadUsersFile("")
	require.Error(t, err)
}

func TestServerStringAndConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Broker.StoreDir = t.TempDir()
	s := &Server{cfg: cfg}
	assert.Equal(t, "", s.String())
	assert.Equal(t, cfg, s.Config())
}
