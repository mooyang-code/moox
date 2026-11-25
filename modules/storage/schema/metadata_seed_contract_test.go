package schema

import (
	"os"
	"path/filepath"
	"testing"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

type metadataSeedContract struct {
	Datasets []struct {
		SpaceID    string            `yaml:"space_id"`
		DatasetID  string            `yaml:"dataset_id"`
		DataKind   string            `yaml:"data_kind"`
		Freq       string            `yaml:"freq"`
		Attributes map[string]string `yaml:"attributes"`
	} `yaml:"datasets"`
	DatasetColumns []struct {
		SpaceID     string `yaml:"space_id"`
		DatasetID   string `yaml:"dataset_id"`
		ColumnName  string `yaml:"column_name"`
		DisplayName string `yaml:"display_name"`
		Group       string `yaml:"group"`
	} `yaml:"dataset_columns"`
}

// 种子没有字段目录与 View：列自描述（显示名、分组写在列上），每个数据集的查询索引由 Storage 自动建。
func TestDefaultSetupSeedColumnsDescribeThemselves(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml"))
	require.NoError(t, err)
	var sections map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &sections))
	for _, removed := range []string{"field_groups", "fields", "views"} {
		require.NotContains(t, sections, removed, "种子里不应再有 %s 段", removed)
	}
	var seed metadataSeedContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	datasets := make(map[string]bool, len(seed.Datasets))
	for _, dataset := range seed.Datasets {
		datasets[dataset.SpaceID+"/"+dataset.DatasetID] = true
	}
	require.NotEmpty(t, seed.DatasetColumns)
	for _, column := range seed.DatasetColumns {
		key := column.SpaceID + "/" + column.DatasetID + "/" + column.ColumnName
		require.True(t, datasets[column.SpaceID+"/"+column.DatasetID], "%s 引用的数据集不在种子里", key)
		require.NotEmpty(t, column.DisplayName, "%s 缺显示名", key)
		require.NotEmpty(t, column.Group, "%s 缺分组", key)
	}
}

func TestDefaultSetupSeedTimeSeriesDatasetsDeclareCanonicalFreq(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml"))
	require.NoError(t, err)
	var seed metadataSeedContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	for _, dataset := range seed.Datasets {
		if dataset.DataKind == "time_series" {
			require.True(t, frequencypkg.IsCanonical(dataset.Freq), "%s/%s freq %q", dataset.SpaceID, dataset.DatasetID, dataset.Freq)
		}
	}
}

func TestDefaultSetupSeedHasNoCollectorDatasetsAndKeepsTheOthers(t *testing.T) {
	path := filepath.Join("..", "..", "..", "config", "setup", "metadata.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var seed metadataSeedContract
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	indexes := make(map[string]int, len(seed.Datasets))
	for i, dataset := range seed.Datasets {
		key := dataset.SpaceID + "/" + dataset.DatasetID
		require.NotContains(t, indexes, key, "duplicate Dataset identity")
		indexes[key] = i
	}
	// 采集的结果数据集由采集任务派生，种子里不再放原始行情数据集。
	for _, dataset := range seed.Datasets {
		require.NotEqual(t, "collector", dataset.Attributes["owner_module"], "种子里不放采集的行情数据集：%s/%s", dataset.SpaceID, dataset.DatasetID)
	}
	preserved := map[string]struct{ kind, role string }{
		"stockcn/dataset_stockcn_financial_statement_metric": {"record", ""},
		"stockcn/dataset_stockcn_financial_summary":          {"record", ""},
	}
	for key, want := range preserved {
		t.Run(key, func(t *testing.T) {
			index, found := indexes[key]
			require.True(t, found, "non-Collector Dataset is missing")
			dataset := seed.Datasets[index]
			require.Equal(t, want.kind, dataset.DataKind)
			require.Empty(t, dataset.Attributes["owner_module"])
			require.Equal(t, want.role, dataset.Attributes["dataset_role"])
		})
	}
}
