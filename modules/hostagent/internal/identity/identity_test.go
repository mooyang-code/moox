package identity

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
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
	assert.Equal(t, 2, first.Version)
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9]{4}$`), first.AgentID)
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f-]{36}$`), first.LegacyID)

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

func TestLoadOrCreate_InvalidID_ShouldReturnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: not-a-uuid\n"), 0o600))
	_, err := LoadOrCreate(path)
	assert.Error(t, err)
}

func TestLoadOrCreate_ValidExistingFile_ShouldLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	content := "version: 2\nagent_id: aB3x\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	got, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, "aB3x", got.AgentID)
}

func TestLoadOrCreate_MigratesLegacyUUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 1\nagent_id: 550e8400-e29b-41d4-a716-446655440000\n"), 0o600))

	got, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Version)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", got.LegacyID)
	assert.Regexp(t, regexp.MustCompile(`^[A-Za-z0-9]{4}$`), got.AgentID)
	expected, err := hostmetricpb.CompactAgentIDForLegacy(got.LegacyID)
	require.NoError(t, err)
	assert.Equal(t, expected, got.AgentID)

	reloaded, err := LoadOrCreate(path)
	require.NoError(t, err)
	assert.Equal(t, got.AgentID, reloaded.AgentID)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "agent_id: 550e8400-e29b-41d4-a716-446655440000")
	assert.Contains(t, string(raw), "compact_agent_id: "+got.AgentID)
}
