package ruleseed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// 美股的种子：标签 us_spx（成分来自 datahub）、us_ndx（成分来自纳斯达克），行情源都是东方财富，
// 同步时刻是纽约开盘前 08:00（不写 sync.cron 时是每小时一次）；停用的“美股 K 线 5m”任务绑标普 500，
// 不写输出字段，与 A 股 5m 一样输出股票行的全部字段；stockus 登记了东方财富数据源；没人写入的 1d 种子数据集已经删掉。
func TestSeedDeclaresTheStockUSTagsAndTask(t *testing.T) {
	setup := filepath.Join("..", "..", "..", "..", "config", "setup")
	tasks, err := LoadFile(filepath.Join(setup, "collection-tasks.yaml"))
	require.NoError(t, err)
	var us, cn *domain.CollectionTask
	ids := map[string]bool{}
	for i := range tasks {
		require.False(t, ids[tasks[i].TaskID], "任务 ID 不能重复：%s", tasks[i].TaskID)
		ids[tasks[i].TaskID] = true
		switch {
		case tasks[i].SpaceID == "stockus":
			require.Nil(t, us, "美股只有一个内置任务")
			us = &tasks[i]
		case tasks[i].TaskName == "A 股 K 线 5m":
			cn = &tasks[i]
		}
	}
	require.NotNil(t, us)
	require.NotNil(t, cn)
	require.Equal(t, "美股 K 线 5m", us.TaskName)
	require.Equal(t, []string{"us_spx"}, us.TagIDs)
	require.False(t, us.Enabled)
	usParams, err := domain.ParseCollectParams(us.CollectParams, "", "", us.DataType)
	require.NoError(t, err)
	cnParams, err := domain.ParseCollectParams(cn.CollectParams, "", "", cn.DataType)
	require.NoError(t, err)
	require.Equal(t, "5m", usParams.Frequency)
	require.Equal(t, cnParams.OutputFields, usParams.OutputFields, "输出字段与 A 股 5m 任务相同")

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
		Views []struct {
			SpaceID string `yaml:"space_id"`
		} `yaml:"views"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &seed))
	want := map[string]map[string]string{
		"us_spx": {"sync.index_source": "datahub", "sync.index": "SPX"},
		"us_ndx": {"sync.index_source": "nasdaq", "sync.index": "NDX"},
	}
	found := map[string]bool{}
	for _, tag := range seed.Tags {
		if tag.SpaceID != "stockus" {
			continue
		}
		extra, ok := want[tag.TagID]
		require.True(t, ok, "意外的美股标签 %s", tag.TagID)
		require.Equal(t, "auto", tag.Mode)
		require.True(t, tag.Builtin)
		expected := map[string]string{
			"market_type": "equity", "sync.source": "eastmoney",
			"sync.cron": "0 8 * * 1-5", "sync.timezone": "America/New_York",
		}
		for key, value := range extra {
			expected[key] = value
		}
		require.Equal(t, expected, tag.Attributes)
		found[tag.TagID] = true
	}
	require.Len(t, found, 2, "种子里要有 us_spx、us_ndx 两个标签")
	eastmoney := false
	for _, source := range seed.DataSources {
		eastmoney = eastmoney || (source.SpaceID == "stockus" && source.DataSourceID == "eastmoney")
		// nasdaq、datahub 只是成分来源，不是采集数据源。
		require.False(t, source.SpaceID == "stockus" && (source.DataSourceID == "nasdaq" || source.DataSourceID == "datahub"))
	}
	require.True(t, eastmoney, "stockus 要登记 eastmoney 数据源（任务结果数据集的数据源）")
	for _, dataset := range seed.Datasets {
		require.NotEqual(t, "stockus", dataset.SpaceID, "美股的结果数据集由采集任务派生，种子里不放")
	}
	for _, view := range seed.Views {
		require.NotEqual(t, "stockus", view.SpaceID, "美股的视图由采集任务派生，种子里不放")
	}
}
