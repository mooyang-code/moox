package unitinstall

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStorageLayoutKeepsLegacyAndAbsolutePathsInsideCandidate(t *testing.T) {
	document, err := yamlDocument([]byte("storage:\n  root: /retired/private-root\n  policy_file: ../config/storage-policy.json\n  metadata: {path: /retired/private-db}\n  devices: {pebble_path: /retired/private-pebble}\n  view: {max_workers: 4}\n"))
	require.NoError(t, err)
	require.NoError(t, renderStorageConfiguration(document))
	storage := member(document, "storage")
	require.Equal(t, "config/storage-policy.json", member(storage, "policy_file").Value)
	require.Equal(t, "./var/storage/metadata/storage_metadata.db", member(member(storage, "metadata"), "path").Value)
	require.Equal(t, "./var/storage/pebble", member(member(storage, "devices"), "pebble_path").Value)
	require.Equal(t, "4", member(member(storage, "view"), "max_workers").Value)
	for key, value := range map[string]string{"MOOX_STORAGE_HOME": "/retired/private-root", "MOOX_STORAGE_METADATA_PATH": "/retired/private-db", "MOOX_STORAGE_ROLE": "view"} {
		err := configureStorageEnvironment("storage-primary", map[string]string{key: value})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "retired")
	}
	require.Error(t, configureStorageEnvironment("storage-primary", nil))
}
