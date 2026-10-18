// Package export writes partition files atomically under the archive root.
package export

import (
	"bufio"
	"bytes"
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

// ColumnMeta 是元数据文件里一列的说明。
type ColumnMeta struct {
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	DisplayName string            `json:"display_name,omitempty"`
	Description string            `json:"description,omitempty"`
	OriginType  string            `json:"origin_type,omitempty"`
	OriginID    string            `json:"origin_id,omitempty"`
	Required    bool              `json:"required,omitempty"`
	Aliases     []string          `json:"aliases,omitempty"`
	Attributes  map[string]string `json:"attributes,omitempty"`
}

// DatasetMeta 是 <prefix>_dataset.json 的内容：数据集的完整说明，方便不看线上系统也能读懂归档文件。
type DatasetMeta struct {
	SpaceID    string `json:"space_id"`
	DatasetID  string `json:"dataset_id"`
	FilePrefix string `json:"file_prefix"`
	// Dataset 是 Storage 里的数据集信息（名称、简介、数据来源、标的范围等），由 mgr 提供。
	Dataset json.RawMessage `json:"dataset,omitempty"`
	Freq    string          `json:"freq"`
	// Partition 说明文件如何切分。
	Partition PartitionMeta `json:"partition"`
	Files     FilesMeta     `json:"files"`
	// Columns 依次是文件里的列：先是三个系统列，再是按列名排序的值列。
	Columns   []ColumnMeta `json:"columns"`
	UpdatedAt string       `json:"updated_at"`
}

// PartitionMeta 说明分区方式。
type PartitionMeta struct {
	Unit     string `json:"unit"`
	Timezone string `json:"timezone"`
}

// FilesMeta 说明文件格式与命名。
type FilesMeta struct {
	Format      string   `json:"format"`
	PathPattern string   `json:"path_pattern"`
	Header      []string `json:"header"`
}

// DatasetMetaInput 是生成元数据文件需要的信息。
type DatasetMetaInput struct {
	SpaceID, DatasetID, FilePrefix, Freq string
	PartitionUnit, Timezone              string
	// DatasetJSON 是 mgr 下发的数据集信息 JSON，可为空。
	DatasetJSON string
	Columns     []staging.Column
	// Infos 是值列的元数据，可为空；按列名匹配。
	Infos []ColumnMeta
}

// BuildDatasetMeta 组装元数据：系统列说明固定，值列的展示名等来自 Infos。
func (f Files) BuildDatasetMeta(in DatasetMetaInput, now time.Time) DatasetMeta {
	system := []ColumnMeta{
		{Name: "data_time", Type: "timestamp", DisplayName: "数据时间", Description: "该行数据对应的时间，UTC，格式 RFC3339（如 2026-10-17T12:00:00Z）"},
		{Name: "subject_id", Type: "string", DisplayName: "标的 ID", Description: "标的（如交易对、股票代码）的 ID"},
		{Name: "series_tag", Type: "string", DisplayName: "序列标签", Description: "区分同一标的下不同数据序列的标签（如来源、市场）；没有时为空"},
	}
	byName := map[string]ColumnMeta{}
	for _, info := range in.Infos {
		byName[info.Name] = info
	}
	columns := append([]ColumnMeta{}, system...)
	header := []string{"data_time", "subject_id", "series_tag"}
	for _, c := range in.Columns {
		meta, ok := byName[c.Name]
		if !ok {
			meta = ColumnMeta{Name: c.Name}
		}
		meta.Name, meta.Type = c.Name, c.Type
		columns = append(columns, meta)
		header = append(header, c.Name)
	}
	var dataset json.RawMessage
	if in.DatasetJSON != "" && json.Valid([]byte(in.DatasetJSON)) {
		dataset = json.RawMessage(in.DatasetJSON)
	}
	pattern := "<yyyy>/" + in.FilePrefix + "_<yyyymmdd>" + ext(f.Format)
	if in.PartitionUnit == "month" {
		pattern = "<yyyy>/" + in.FilePrefix + "_<yyyymm>" + ext(f.Format)
	}
	format := f.Format
	if format == "" {
		format = FormatCSV
	}
	return DatasetMeta{
		SpaceID: in.SpaceID, DatasetID: in.DatasetID, FilePrefix: in.FilePrefix, Dataset: dataset, Freq: in.Freq,
		Partition: PartitionMeta{Unit: in.PartitionUnit, Timezone: in.Timezone},
		Files:     FilesMeta{Format: format, PathPattern: pattern, Header: header},
		Columns:   columns, UpdatedAt: now.UTC().Format(time.RFC3339),
	}
}

// WriteDatasetJSON 写 <root>/<space>/<prefix>_dataset.json；除更新时间外内容没变时不重写。
func (f Files) WriteDatasetJSON(meta DatasetMeta) error {
	if _, err := f.RelPath(meta.SpaceID, meta.FilePrefix, "2000-01"); err != nil {
		return err
	}
	dir := filepath.Join(f.Root, meta.SpaceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, meta.FilePrefix+"_dataset.json")
	if raw, err := os.ReadFile(path); err == nil {
		var current DatasetMeta
		if json.Unmarshal(raw, &current) == nil {
			stamp := current.UpdatedAt
			current.UpdatedAt = meta.UpdatedAt
			if a, errA := json.Marshal(current); errA == nil {
				if b, errB := json.Marshal(meta); errB == nil && string(a) == string(b) {
					_ = stamp
					return nil
				}
			}
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // path_pattern 里的 <yyyy> 不转义，保持可读
	enc.SetIndent("", "  ")
	if err := enc.Encode(meta); err != nil {
		return err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
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
