package engine

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mooyang-code/moox/packages/dataformat"
)

func TestRecordFactorEngineFormatWritesFileInDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data", "factor-engine")
	seriesDir := filepath.Join(dataDir, "seriesstore")
	if err := recordFactorEngineFormat(seriesDir); err != nil {
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
}
