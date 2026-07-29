package schema

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type metadataSeedGrainContract struct {
	Datasets []struct {
		SpaceID   string `yaml:"space_id"`
		DatasetID string `yaml:"dataset_id"`
		DataKind  string `yaml:"data_kind"`
	} `yaml:"datasets"`
	Views []struct {
		SpaceID          string   `yaml:"space_id"`
		ViewID           string   `yaml:"view_id"`
		PrimaryDatasetID string   `yaml:"primary_dataset_id"`
		GrainKeys        []string `yaml:"grain_keys"`
	} `yaml:"views"`
}

func TestActiveMetadataSeedsUseCanonicalTimeSeriesViewGrain(t *testing.T) {
	wantGrain := []string{"subject_id", "freq", "data_time", "series_tag"}
	root := filepath.Join("..", "..", "..")
	seedPaths := []string{
		filepath.Join(root, "modules", "storage", "config", "metadata.seed.yaml"),
		filepath.Join(root, "examples", "metadata-quant-initial.seed.yaml"),
		filepath.Join(root, "examples", "metadata-monitor-host.seed.yaml"),
		filepath.Join(root, "examples", "metadata-monitor-metrics.seed.yaml"),
	}

	for _, seedPath := range seedPaths {
		t.Run(filepath.Base(filepath.Dir(seedPath))+"/"+filepath.Base(seedPath), func(t *testing.T) {
			raw, err := os.ReadFile(seedPath)
			require.NoError(t, err)
			var seed metadataSeedGrainContract
			require.NoError(t, yaml.Unmarshal(raw, &seed))

			kinds := make(map[string]string, len(seed.Datasets))
			for _, dataset := range seed.Datasets {
				kinds[dataset.SpaceID+"/"+dataset.DatasetID] = dataset.DataKind
			}
			for _, view := range seed.Views {
				datasetKey := view.SpaceID + "/" + view.PrimaryDatasetID
				kind, ok := kinds[datasetKey]
				require.True(t, ok, "%s/%s references missing primary Dataset %s", view.SpaceID, view.ViewID, datasetKey)
				if kind != "time_series" {
					continue
				}
				require.Equal(t, wantGrain, view.GrainKeys, "%s/%s", view.SpaceID, view.ViewID)
			}
		})
	}
}
