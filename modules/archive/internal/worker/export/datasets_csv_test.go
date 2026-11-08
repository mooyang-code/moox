package export

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func writeMeta(t *testing.T, files Files, space, purpose, dir, dataset, name, freq string) {
	t.Helper()
	meta := files.BuildDatasetMeta(DatasetMetaInput{
		SpaceID: space, DatasetID: dataset, Purpose: purpose, DirName: dir, Freq: freq, PartitionUnit: "day", Timezone: "UTC",
		DatasetJSON: fmt.Sprintf(`{"name":%q}`, name),
	}, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	if err := files.WriteDatasetJSON(meta); err != nil {
		t.Fatal(err)
	}
}

func TestDatasetsCSVListsEveryTaskDirectorySortedAndSkipsDotDirectories(t *testing.T) {
	files := Files{Root: t.TempDir()}
	writeMeta(t, files, "stockcn", "collect", "hs300-kline-5m", "dataset_dasftksvjhj2jom4vhh0", "A股5分钟K线", "5m")
	writeMeta(t, files, "crypto", "factor", "binance-spot-1m", "dataset_factor_spot_1m", "现货一分钟", "1m")
	writeMeta(t, files, "crypto", "collect", "binance-spot-kline-1m", "dataset_dasftksvjhj2jom4vhd0", "现货分钟K线, 含逗号", "1m")
	// 点开头的目录（暂存库、导入中转）里即便有 dataset.json 也不算任务目录；只有两级的 <space>/<x>/dataset.json 层级不对，也不算。
	for _, stray := range []string{
		filepath.Join(files.Root, ".state", "x", "y", "dataset.json"),
		filepath.Join(files.Root, "crypto", ".import", "i1", "dataset.json"),
		filepath.Join(files.Root, "crypto", "collect", ".hidden", "dataset.json"),
		filepath.Join(files.Root, "crypto", "dataset_old", "dataset.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stray, []byte(`{"dataset_id":"stray","freq":"1m"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := files.WriteDatasetsCSV(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(files.Root, DatasetsCSVName))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"space_id,purpose,dir_name,dataset_id,name,freq",
		`crypto,collect,binance-spot-kline-1m,dataset_dasftksvjhj2jom4vhd0,"现货分钟K线, 含逗号",1m`,
		"crypto,factor,binance-spot-1m,dataset_factor_spot_1m,现货一分钟,1m",
		"stockcn,collect,hs300-kline-5m,dataset_dasftksvjhj2jom4vhh0,A股5分钟K线,5m",
	}, "\n") + "\n"
	if string(raw) != want {
		t.Fatalf("datasets.csv =\n%s\nwant\n%s", raw, want)
	}
	info, _ := os.Stat(filepath.Join(files.Root, DatasetsCSVName))
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("权限 = %v", info.Mode().Perm())
	}
	// 内容没变时不重写。
	before := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	if err := files.WriteDatasetsCSV(); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.Stat(filepath.Join(files.Root, DatasetsCSVName)); !again.ModTime().Equal(before) {
		t.Fatal("内容没变不应重写 datasets.csv")
	}
	matches, _ := filepath.Glob(filepath.Join(files.Root, "*"+partialSuffix))
	if len(matches) != 0 {
		t.Fatalf("残留临时文件: %v", matches)
	}
}

func TestDatasetsCSVConcurrentWritesStayConsistent(t *testing.T) {
	files := Files{Root: t.TempDir()}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta := files.BuildDatasetMeta(DatasetMetaInput{
				SpaceID: "crypto", DatasetID: fmt.Sprintf("ds_%02d", i), Purpose: "collect", DirName: fmt.Sprintf("d-%02d", i), Freq: "1m",
				PartitionUnit: "day", Timezone: "UTC",
			}, time.Now())
			errs <- files.WriteDatasetJSON(meta)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(files.Root, DatasetsCSVName))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(raw), "\n"); lines != 17 {
		t.Fatalf("datasets.csv 应有表头加 16 行，实际 %d 行:\n%s", lines, raw)
	}
	matches, _ := filepath.Glob(filepath.Join(files.Root, "*"+partialSuffix))
	if len(matches) != 0 {
		t.Fatalf("残留临时文件: %v", matches)
	}
}
