package export

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DatasetsCSVName 是归档根目录下的数据集对照表：每个任务目录一行，方便从目录名找回数据集。
const DatasetsCSVName = "datasets.csv"

var datasetsCSVHeader = []string{"space_id", "purpose", "dir_name", "dataset_id", "name", "freq"}

// datasetsCSVMu 让同一进程里刷新对照表的调用串行（Files 是值类型，锁放在包级）。
var datasetsCSVMu sync.Mutex

// WriteDatasetsCSV 扫描 <root>/<space>/<purpose>/<dir_name>/dataset.json（各级都跳过点开头的目录，如 .state、.import），
// 生成根目录的 datasets.csv（列 space_id,purpose,dir_name,dataset_id,name,freq，按前三列排序）；内容没变时不重写。
// 先写唯一的临时文件（.partial 结尾，残留由 CleanPartials 清掉）再改名。对照表只在本地，不上传对象存储。
func (f Files) WriteDatasetsCSV() error {
	datasetsCSVMu.Lock()
	defer datasetsCSVMu.Unlock()
	rows, err := f.scanDatasetJSON()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(datasetsCSVHeader); err != nil {
		return err
	}
	if err := w.WriteAll(rows); err != nil {
		return err
	}
	target := filepath.Join(f.Root, DatasetsCSVName)
	if current, err := os.ReadFile(target); err == nil && bytes.Equal(current, buf.Bytes()) {
		return nil
	}
	tmp, err := os.CreateTemp(f.Root, DatasetsCSVName+".*"+partialSuffix)
	if err != nil {
		return err
	}
	err = func() error {
		if _, err := tmp.Write(buf.Bytes()); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Chmod(0o644); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Sync(); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), target)
	}()
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return syncDir(f.Root)
}

// scanDatasetJSON 读出全部任务目录的 dataset.json；空间、用途、目录名取自所在路径，其余取自文件内容。读不出或解析不了的文件跳过。
func (f Files) scanDatasetJSON() ([][]string, error) {
	var rows [][]string
	spaces, err := visibleDirs(f.Root)
	if err != nil {
		return nil, err
	}
	for _, space := range spaces {
		purposes, err := visibleDirs(filepath.Join(f.Root, space))
		if err != nil {
			return nil, err
		}
		for _, purpose := range purposes {
			dirs, err := visibleDirs(filepath.Join(f.Root, space, purpose))
			if err != nil {
				return nil, err
			}
			for _, dir := range dirs {
				raw, err := os.ReadFile(filepath.Join(f.Root, space, purpose, dir, datasetJSONName))
				if err != nil {
					continue
				}
				var meta DatasetMeta
				if json.Unmarshal(raw, &meta) != nil {
					continue
				}
				var dataset struct {
					Name string `json:"name"`
				}
				if len(meta.Dataset) > 0 {
					_ = json.Unmarshal(meta.Dataset, &dataset)
				}
				rows = append(rows, []string{space, purpose, dir, meta.DatasetID, dataset.Name, meta.Freq})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		for k := 0; k < 3; k++ {
			if rows[i][k] != rows[j][k] {
				return rows[i][k] < rows[j][k]
			}
		}
		return false
	})
	return rows, nil
}

// visibleDirs 列出 dir 下不以点开头的子目录名；dir 不存在时返回空。
func visibleDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}
