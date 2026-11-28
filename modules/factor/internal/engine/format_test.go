package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/dataformat"
)

func TestRecordFactorEngineFormatWritesAndVerifies(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data", "factor-engine")
	seriesDir := filepath.Join(dataDir, "seriesstore")
	if err := recordFactorEngineFormat(seriesDir, false); err != nil {
		t.Fatalf("写入因子引擎数据格式文件失败：%v", err)
	}
	rec, err := dataformat.ReadFile(filepath.Join(dataDir, dataformat.FileName))
	if err != nil {
		t.Fatalf("读取因子引擎数据格式文件失败：%v", err)
	}
	if rec.Component != "factor-engine" {
		t.Fatalf("Component = %q, want factor-engine", rec.Component)
	}
	if rec.Parts[dataformat.PartFactorEngineState] != strconv.Itoa(dataformat.FactorEngineStateVersion) {
		t.Fatalf("factor_engine_state = %q", rec.Parts[dataformat.PartFactorEngineState])
	}
	if rec.Parts[dataformat.PartLifecycle] != strconv.Itoa(dataformat.LifecycleStateVersion) {
		t.Fatalf("lifecycle = %q", rec.Parts[dataformat.PartLifecycle])
	}

	// 已有数据且格式文件版本不同时必须拒绝启动并提示 setup install --wipe。
	stale, err := dataformat.Build("factor-engine", map[string]string{
		dataformat.PartFactorEngineState: strconv.Itoa(dataformat.FactorEngineStateVersion + 99),
		dataformat.PartLifecycle:         strconv.Itoa(dataformat.LifecycleStateVersion),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dataformat.WriteDir(dataDir, stale); err != nil {
		t.Fatal(err)
	}
	err = recordFactorEngineFormat(seriesDir, true)
	if !errors.Is(err, dataformat.ErrIncompatibleFormat) || !strings.Contains(err.Error(), "setup install --wipe") {
		t.Fatalf("期望格式不符拒启，实际 err = %v", err)
	}
}

func TestFactorEngineHasDataUsesConfiguredLifecycleStateFile(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data", "factor-engine")
	cfg := &Config{
		SeriesStore: SeriesStoreConfig{Dir: filepath.Join(dataDir, "seriesstore")},
		Watermark:   WatermarkConfig{StateFile: filepath.Join(dataDir, "engine", "watermarks.json")},
		Params:      ParamsConfig{CacheFile: filepath.Join(dataDir, "engine", "param_cache.json")},
		Lifecycle:   LifecycleConfig{StateFile: filepath.Join(dataDir, "engine", "lifecycle.json")},
		Gap:         GapConfig{LedgerDir: filepath.Join(dataDir, "gap")},
	}
	if factorEngineHasData(cfg) {
		t.Fatal("空目录不应判定为已有数据")
	}

	// 数据根目录下的同名 lifecycle.json 不是实际状态文件路径，不应误判。
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "lifecycle.json"), []byte(`{"stage":"paused"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if factorEngineHasData(cfg) {
		t.Fatal("不应把数据根目录下的 lifecycle.json 当成 cfg.Lifecycle.StateFile")
	}

	// 写入实际配置的 cfg.Lifecycle.StateFile 后必须判定为已有数据。
	if err := os.MkdirAll(filepath.Dir(cfg.Lifecycle.StateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Lifecycle.StateFile, []byte(`{"stage":"paused"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !factorEngineHasData(cfg) {
		t.Fatal("实际 cfg.Lifecycle.StateFile 存在时必须判定为已有数据")
	}
}
