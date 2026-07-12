package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreate_EmptyPath_ShouldReturnError(t *testing.T) {
	_, err := LoadOrCreate("")
	assert.Error(t, err)
}

func TestLoadOrCreate_MissingFile_ShouldCreateAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	first, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, 1, first.Version)
	assert.NotEmpty(t, first.AgentID)

	second, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, first.AgentID, second.AgentID)
}

func TestLoadOrCreate_BadPermission_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: not-a-uuid\n"), 0o644))
	_, err := LoadOrCreate(path)
	assert.Error(t, err)
}

func TestLoadOrCreate_InvalidUUID_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: not-a-uuid\n"), 0o600))
	_, err := LoadOrCreate(path)
	assert.Error(t, err)
}

func TestLoadOrCreate_ValidExistingFile_ShouldLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	content := "version: 1\nagent_id: 550e8400-e29b-41d4-a716-446655440000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	got, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", got.AgentID)
}
