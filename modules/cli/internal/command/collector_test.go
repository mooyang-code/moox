package command

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDefaultCryptoCollectorTasksSelectOnlyKlineFields(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string `yaml:"space_id"`
			TaskName      string `yaml:"task_name"`
			DataType      string `yaml:"data_type"`
			CollectParams struct {
				Frequency    string   `yaml:"frequency"`
				OutputFields []string `yaml:"output_fields"`
			} `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	wantFields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	wantTasks := map[string]bool{
		"Binance 现货 K 线 1m": false,
		"Binance 合约 K 线 1m": false,
	}
	for _, task := range bundle.Tasks {
		if task.SpaceID != "crypto" || task.DataType != "kline" {
			continue
		}
		if _, ok := wantTasks[task.TaskName]; !ok {
			continue
		}
		wantTasks[task.TaskName] = true
		assert.Equal(t, wantFields, task.CollectParams.OutputFields, task.TaskName)
	}
	for name, found := range wantTasks {
		assert.True(t, found, "missing default task %s", name)
	}
}

func TestDefaultStockCNCollectorTasksRequireExplicitActivation(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml"))
	require.NoError(t, err)
	var bundle struct {
		Tasks []struct {
			SpaceID       string         `yaml:"space_id"`
			DataType      string         `yaml:"data_type"`
			TagIDs        []string       `yaml:"tag_ids"`
			Enabled       bool           `yaml:"enabled"`
			Start         string         `yaml:"start"`
			CollectParams map[string]any `yaml:"collect_params"`
		} `yaml:"tasks"`
	}
	require.NoError(t, yaml.Unmarshal(content, &bundle))
	var stock []string
	for _, task := range bundle.Tasks {
		if task.SpaceID != "stockcn" {
			continue
		}
		frequency := fmt.Sprint(task.CollectParams["frequency"])
		stock = append(stock, task.DataType+"/"+strings.Join(task.TagIDs, ",")+"/"+frequency+"/"+task.Start)
		assert.False(t, task.Enabled)
	}
	assert.Equal(t, []string{"kline/cn_hs300/5m/", "kline/cn_hs300/1d/manual", "kline/cn_hs300/1d/manual"}, stock,
		"A 股有沪深 300 的 5m 任务、纯日线任务和带复权、行业、财务、换手率、股本的日线任务，都默认停用；两条日线任务 setup start 不启用")

	// setup start 跳过两条日线任务。
	path := filepath.Join("..", "..", "..", "..", "config", "setup", "collection-tasks.yaml")
	manual, err := manualStartTasks(path, "stockcn")
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"dasftksvjhj2jom4vhi0": true, "dasftksvjhj2jom4vhj0": true}, manual)
}

func TestResolveCollectorRootMissing(t *testing.T) {
	_, err := resolveCollectorRoot("/nonexistent/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collector root not found")
}
