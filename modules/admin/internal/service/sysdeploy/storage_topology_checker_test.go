package sysdeploy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStorageTopologyWarnings_StorageService_ShouldReturnWarning(t *testing.T) {
	warnings := storageTopologyWarnings("storage-primary")
	assert.Len(t, warnings, 1)
	assert.Equal(t, "storage_topology_overlap", warnings[0].Code)
	assert.Equal(t, "storage-primary", warnings[0].ServiceName)
}

func TestStorageTopologyWarnings_NonStorageService_ShouldReturnNil(t *testing.T) {
	assert.Nil(t, storageTopologyWarnings("moox_trade"))
}

func TestStorageTopologyWarnings_EmptyService_ShouldReturnWarning(t *testing.T) {
	warnings := storageTopologyWarnings("")
	assert.Len(t, warnings, 1)
}

func TestIsStorageDeployment_KnownNames_ShouldReturnTrue(t *testing.T) {
	for _, name := range []string{
		"storage-primary", "storage-view",
	} {
		assert.True(t, isStorageDeployment(name), name)
	}
	for _, name := range []string{"storage_view_builder", "storage_view_query", "storage_view_index"} {
		assert.False(t, isStorageDeployment(name), name)
	}
	assert.False(t, isStorageDeployment("moox_trade"))
}
