package ruleseed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 港股的种子：标签 hk_hsi（行情源东方财富、成分来源腾讯）、停用的“港股 K 线 5m”任务（不写输出字段，与 A 股 5m 一样
// 输出股票行的全部字段）；stockhk 登记了东方财富数据源；没人写入的 1d 种子数据集已经删掉。
func TestSeedDeclaresTheStockHKTagAndTask(t *testing.T) {
	setup := filepath.Join("..", "..", "..", "..", "config", "setup")
	tasks, err := LoadFile(filepath.Join(setup, "collection-tasks.yaml"))
	require.NoError(t, err)
	var hk, cn *domain.CollectionTask
	for i := range tasks {
		switch {
		case tasks[i].SpaceID == "stockhk":
			require.Nil(t, hk, "港股只有一个内置任务")
			hk = &tasks[i]
		case tasks[i].TaskName == "A 股 K 线 5m":
			cn = &tasks[i]
		}
	}
	require.NotNil(t, hk)
	require.NotNil(t, cn)
	require.Equal(t, "港股 K 线 5m", hk.TaskName)
	require.Equal(t, []string{"hk_hsi"}, hk.TagIDs)
	require.False(t, hk.Enabled)
	hkParams, err := domain.ParseCollectParams(hk.CollectParams, "", "", hk.DataType)
	require.NoError(t, err)
	cnParams, err := domain.ParseCollectParams(cn.CollectParams, "", "", cn.DataType)
	require.NoError(t, err)
	require.Equal(t, "5m", hkParams.Frequency)
	require.Equal(t, cnParams.OutputFields, hkParams.OutputFields, "输出字段与 A 股 5m 任务相同")

	raw, err := os.ReadFile(filepath.Join(setup, "metadata.yaml"))
	require.NoError(t, err)
	var seed struct {
		DataSources []struct {
			SpaceID      string `yaml:"space_id"`
			DataSourceID string `yaml:"data_source_id"`
		} `yaml:"data_sources"`
		Tags []struct {
			SpaceID    string            `yaml:"space_id"`
			TagID      string            `yaml:"tag_id"`
			Mode       string            `yaml:"mode"`
			Builtin    bool              `yaml:"builtin"`
			Attributes map[string]string `yaml:"attributes"`
		} `yaml:"tags"`
		Datasets []struct {
			SpaceID string `yaml:"space_id"`
		} `yaml:"datasets"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	found := false
	for _, tag := range seed.Tags {
		if tag.SpaceID != "stockhk" {
			continue
		}
		require.Equal(t, "hk_hsi", tag.TagID)
		require.Equal(t, "auto", tag.Mode)
		require.True(t, tag.Builtin)
		require.Equal(t, map[string]string{
			"market_type": "equity", "sync.source": "eastmoney", "sync.index_source": "tencent", "sync.index": "HSI",
			"sync.cron": "40 8 * * *", "sync.timezone": "Asia/Hong_Kong",
		}, tag.Attributes)
		found = true
	}
	require.True(t, found, "种子里要有 hk_hsi 标签")
	eastmoney := false
	for _, source := range seed.DataSources {
		eastmoney = eastmoney || (source.SpaceID == "stockhk" && source.DataSourceID == "eastmoney")
	}
	require.True(t, eastmoney, "stockhk 要登记 eastmoney 数据源（任务结果数据集的数据源）")
	for _, dataset := range seed.Datasets {
		require.NotEqual(t, "stockhk", dataset.SpaceID, "港股的结果数据集由采集任务派生，种子里不放")
	}
}
