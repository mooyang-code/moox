package access

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPruneHostDatasetsDeletesBoundedRows(t *testing.T) {
	primaryStore := &deletePrimary{}
	routes := &deleteMetadata{dataset: &pb.Dataset{
		SpaceId: "moox_system", DatasetId: "host_resource_v1",
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"},
	}}
	svc := &Service{
		metadataReader: routes,
		router:         routerForDelete(routes),
		primary:        primaryStore,
	}
	deleted, err := svc.PruneHostDatasets(context.Background(), "moox_system", []string{"host_resource_v1"}, time.Hour, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, uint32(1), deleted)
}

func TestPruneHostDatasetsRejectsNonPositiveRetention(t *testing.T) {
	svc := &Service{}
	_, err := svc.PruneHostDatasets(context.Background(), "moox_system", []string{"host_resource_v1"}, 0, time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retention must be positive")
}

func TestPruneHostDatasetsSkipsEmptyDatasetIDs(t *testing.T) {
	svc := &Service{
		metadataReader: &deleteMetadata{dataset: &pb.Dataset{SpaceId: "moox_system", DatasetId: "host_resource_v1", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}},
		router:         router.NewResolver(&deleteMetadata{dataset: &pb.Dataset{SpaceId: "moox_system", DatasetId: "host_resource_v1", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}}),
		primary:        &deletePrimary{},
	}
	deleted, err := svc.PruneHostDatasets(context.Background(), "moox_system", []string{"", "host_resource_v1"}, time.Hour, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, uint32(1), deleted)
}
