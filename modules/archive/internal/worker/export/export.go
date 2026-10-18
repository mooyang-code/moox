// Package export writes partition files atomically under the archive root.
package export

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
)

const (
	// previewLines 是预览的行数（表头加前 20 行）。
	previewLines = 21
	// previewBytes 是预览的字节上限。
	previewBytes  = 8 << 10
	partialSuffix = ".partial"
)

// Files 管理归档根目录下的分区文件。
type Files struct {
	Root string
	// Format 是导出文件格式：csv（默认，空值同 csv）或 csv.gz。
	Format string
}

// Info 是一次导出得到的文件信息。
type Info struct {
	// Path 是相对归档根目录的路径（斜杠分隔），登记到 mgr。
	Path    string
	Bytes   uint64
	SHA256  string
	Preview string
}

// 导出文件格式。
const (
	FormatCSV   = "csv"
	FormatCSVGz = "csv.gz"
)

func ext(format string) string {
	if format == FormatCSVGz {
		return ".csv.gz"
	}
	return ".csv"
}

func checkComponent(part string) error {
	if part == "" || strings.ContainsAny(part, `/\`) || part == "." || part == ".." || strings.HasPrefix(part, ".") {
		return fmt.Errorf("unsafe path component %q", part)
	}
	return nil
}

// RelPath 返回分区文件相对归档根目录的路径：
// <space>/<yyyy>/<prefix>_<yyyymmdd>.csv（日分区）或 <prefix>_<yyyymm>.csv（月分区）；format 为 csv.gz 时扩展名为 .csv.gz。
// prefix 是导出文件名前缀（数据集名去空格）。
func RelPath(spaceID, prefix, partitionKey, format string) (string, error) {
	digits := strings.ReplaceAll(partitionKey, "-", "")
	if len(partitionKey) != 7 && len(partitionKey) != 10 || len(digits) < 6 {
		return "", fmt.Errorf("invalid partition key %q", partitionKey)
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("invalid partition key %q", partitionKey)
		}
	}
	for _, part := range []string{spaceID, prefix} {
		if err := checkComponent(part); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(filepath.Join(spaceID, digits[:4], prefix+"_"+digits+ext(format))), nil
}

// RelPath 按本目录配置的格式返回分区文件的相对路径。
func (f Files) RelPath(spaceID, prefix, partitionKey string) (string, error) {
	return RelPath(spaceID, prefix, partitionKey, f.Format)
}

// Abs 把相对路径解析到归档根目录下。
func (f Files) Abs(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

// Write 导出分区：COPY 到 .partial，fsync，rename，fsync 目录，计算 sha256 与预览。任何一步失败都不会留下半写的正式文件。
func (f Files) Write(ctx context.Context, store *staging.Store, task staging.Task, prefix string, p staging.PartitionState) (Info, error) {
	rel, err := f.RelPath(task.SpaceID, prefix, p.Key)
	if err != nil {
		return Info{}, err
	}
	final := f.Abs(rel)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Info{}, err
	}
	partial := final + partialSuffix
	_ = os.Remove(partial)
	if err := store.ExportCSV(ctx, task, p, partial, f.Format == FormatCSVGz); err != nil {
		_ = os.Remove(partial)
		return Info{}, err
	}
	if err := syncFile(partial); err != nil {
		_ = os.Remove(partial)
		return Info{}, err
	}
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return Info{}, err
	}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return Info{}, err
	}
	info := Info{Path: rel}
	if info.SHA256, info.Bytes, err = hashFile(final); err != nil {
		return Info{}, err
	}
	if info.Preview, err = preview(final); err != nil {
		return Info{}, err
	}
	return info, nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	// 部分文件系统不支持对目录 fsync，忽略 EINVAL 类错误。
	if err := handle.Sync(); err != nil && !strings.Contains(err.Error(), "invalid argument") {
		return err
	}
	return nil
}

func hashFile(path string) (string, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), uint64(n), nil
}

// FileSHA256 计算文件的 sha256 与大小。
func FileSHA256(path string) (string, uint64, error) { return hashFile(path) }

// preview 返回表头加前 20 行（不超过 8KB）。
func preview(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	br := bufio.NewReader(file)
	var source io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return "", err
		}
		defer zr.Close()
		source = zr
	}
	reader := bufio.NewReader(source)
	var out strings.Builder
	for i := 0; i < previewLines; i++ {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if out.Len()+len(line) > previewBytes {
				break
			}
			out.WriteString(line)
		}
		if err != nil {
			break
		}
	}
	return out.String(), nil
}

// DatasetMeta 是 _dataset.json 的内容。
type DatasetMeta struct {
	SpaceID   string           `json:"space_id"`
	DatasetID string           `json:"dataset_id"`
	Freq      string           `json:"freq"`
	Columns   []staging.Column `json:"columns"`
	UpdatedAt string           `json:"updated_at"`
}

// WriteDatasetJSON 写 <root>/<space>/<prefix>_dataset.json（频率、列及类型）；内容没变时不重写。
func (f Files) WriteDatasetJSON(spaceID, prefix, datasetID, freq string, columns []staging.Column, now time.Time) error {
	if _, err := f.RelPath(spaceID, prefix, "2000-01"); err != nil {
		return err
	}
	dir := filepath.Join(f.Root, spaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, prefix+"_dataset.json")
	if raw, err := os.ReadFile(path); err == nil {
		var current DatasetMeta
		if json.Unmarshal(raw, &current) == nil && current.Freq == freq && sameColumns(current.Columns, columns) {
			return nil
		}
	}
	raw, err := json.MarshalIndent(DatasetMeta{SpaceID: spaceID, DatasetID: datasetID, Freq: freq, Columns: columns, UpdatedAt: now.UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + partialSuffix
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func sameColumns(a, b []staging.Column) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// CleanPartials 启动时清理残留的 .partial 文件。
func (f Files) CleanPartials() (int, error) {
	removed := 0
	err := filepath.WalkDir(f.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if entry.IsDir() && entry.Name() == ".state" {
			return filepath.SkipDir
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), partialSuffix) {
			if err := os.Remove(path); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
