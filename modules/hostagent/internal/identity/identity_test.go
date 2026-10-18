package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreate_EmptyPath_ShouldReturnError(t *testing.T) {
	_, err := LoadOrCreate("")
	assert.Error(t, err)
}

func TestLoadOrCreate_BadPermission_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: not-a-uuid\n"), 0o644))
	_, err := LoadOrCreate(path)
	assert.Error(t, err)
}

func TestLoadOrCreate_InvalidID_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: not-a-uuid\n"), 0o600))
	_, err := LoadOrCreate(path)
	assert.Error(t, err)
}

func TestLoadOrCreate_ValidExistingFile_ShouldLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	content := "version: 3\nagent_id: aB3x\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	got, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, "aB3x", got.AgentID)
}

func TestLoadOrCreatePersistsCompactIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	first, err := LoadOrCreate(path)
	require.NoError(t, err)
	require.True(t, hostmetricpb.IsAgentID(first.AgentID))
	again, err := LoadOrCreate(path)
	require.NoError(t, err)
	require.Equal(t, first.AgentID, again.AgentID)
	require.NoError(t, os.WriteFile(path, []byte("version: 2\nagent_id: 4gY2\n"), 0o600))
	_, err = LoadOrCreate(path)
	require.Error(t, err)
}
