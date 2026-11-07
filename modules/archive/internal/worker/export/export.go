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
	"errors"
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
	// Format 是导出文件格式：csv.gz 或 csv（空值同 csv.gz）。
	Format string
}

// Info 是一次导出得到的文件信息。
type Info struct {
	// Path 是相对归档根目录的路径（斜杠分隔），记进分区状态并登记到 mgr。
	Path    string
	SHA256  string
	Preview string
}

// 导出文件格式。
const (
	FormatCSV   = "csv"
	FormatCSVGz = "csv.gz"
)

func (f Files) format() string {
	if f.Format == FormatCSV {
		return FormatCSV
	}
	return FormatCSVGz
}

func ext(format string) string {
	if format == FormatCSV {
		return ".csv"
	}
	return ".csv.gz"
}

func checkComponent(part string) error {
	if part == "" || strings.ContainsAny(part, `/\`) || part == "." || part == ".." || strings.HasPrefix(part, ".") {
		return fmt.Errorf("不安全的路径片段 %q", part)
	}
	return nil
}

// RelPath 返回分区文件相对归档根目录的路径：<space>/<dataset_id>/<yyyy>/<yyyymmdd>.csv.gz（日分区）
// 或 <space>/<dataset_id>/<yyyy>/<yyyymm>.csv.gz（月分区）；format 为 csv 时扩展名为 .csv。
func RelPath(spaceID, datasetID, partitionKey, format string) (string, error) {
	digits := strings.ReplaceAll(partitionKey, "-", "")
	if len(partitionKey) != 7 && len(partitionKey) != 10 || len(digits) < 6 {
		return "", fmt.Errorf("无效的分区键 %q", partitionKey)
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("无效的分区键 %q", partitionKey)
		}
	}
	for _, part := range []string{spaceID, datasetID} {
		if err := checkComponent(part); err != nil {
			return "", err
		}
	}
	return filepath.ToSlash(filepath.Join(spaceID, datasetID, digits[:4], digits+ext(format))), nil
}

// RelPath 按本目录配置的格式返回分区文件的相对路径。
func (f Files) RelPath(spaceID, datasetID, partitionKey string) (string, error) {
	return RelPath(spaceID, datasetID, partitionKey, f.format())
}

// Abs 把相对路径解析到归档根目录下。
func (f Files) Abs(rel string) string { return filepath.Join(f.Root, filepath.FromSlash(rel)) }

// Write 导出分区：写 .partial（写入时同时算磁盘字节的 sha256），fsync，rename，fsync 目录，生成预览。
// 任何一步失败都不会留下半写的正式文件。改名之后的步骤失败时仍返回带 Path 与 SHA256 的 Info（文件已就位），
// 调用方要先记下它再处理错误，否则下次会因“文件已存在却没有记录”拒绝导出。分区已有文件（p.FilePath）时写回原路径并沿用它的格式；
// 没有时按当前格式生成新路径，若该路径已有文件就拒绝导出：本地没有记录却有文件，说明暂存状态丢过，
// 用残缺的暂存数据覆盖会丢掉文件里的行。
func (f Files) Write(ctx context.Context, store *staging.Store, task staging.Task, p staging.PartitionState) (Info, error) {
	rel := p.FilePath
	if rel == "" {
		var err error
		if rel, err = f.RelPath(task.SpaceID, task.DatasetID, p.Key); err != nil {
			return Info{}, err
		}
	}
	final := f.Abs(rel)
	if p.FilePath == "" {
		if _, err := os.Stat(final); err == nil {
			return Info{}, fmt.Errorf("分区 %s 的目标文件 %s 已存在，但本地没有它的导出记录：可能是暂存库丢失或任务重建，拒绝覆盖，需要人工核对", p.Key, rel)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Info{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return Info{}, err
	}
	partial := final + partialSuffix
	sha, err := writePartial(ctx, store, task, p, partial, strings.HasSuffix(rel, ".gz"))
	if err != nil {
		_ = os.Remove(partial)
		return Info{}, fmt.Errorf("导出分区 %s: %w", p.Key, err)
	}
	if err := os.Rename(partial, final); err != nil {
		_ = os.Remove(partial)
		return Info{}, err
	}
	info := Info{Path: rel, SHA256: sha}
	if err := syncDir(filepath.Dir(final)); err != nil {
		return info, err
	}
	if info.Preview, err = preview(final); err != nil {
		return info, err
	}
	return info, nil
}

// writePartial 把分区写进 partial 并 fsync，返回文件内容（压缩后的字节）的 sha256。
func writePartial(ctx context.Context, store *staging.Store, task staging.Task, p staging.PartitionState, partial string, gz bool) (sum string, err error) {
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	hash := sha256.New()
	disk := io.MultiWriter(file, hash)
	if !gz {
		if err := store.ExportCSV(ctx, task, p, disk); err != nil {
			return "", err
		}
	} else {
		zw := gzip.NewWriter(disk)
		if err := store.ExportCSV(ctx, task, p, zw); err != nil {
			return "", err
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Verify 确认相对路径 rel 的文件存在且 sha256 等于 want；读回与定稿前用它确认原文件完好。
func (f Files) Verify(rel, want string) error {
	if rel == "" || want == "" {
		return fmt.Errorf("文件路径或 sha256 未记录（%q）", rel)
	}
	file, err := os.Open(f.Abs(rel))
	if err != nil {
		return fmt.Errorf("打开归档文件 %s: %w", rel, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("读取归档文件 %s: %w", rel, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("归档文件 %s 的 sha256 是 %s，与记录的 %s 不一致：文件被改动或损坏，需要人工核对", rel, got, want)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

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

// DatasetMeta 是 <space>/<dataset_id>/dataset.json 的内容：数据集的完整说明，方便不看线上系统也能读懂归档文件。
type DatasetMeta struct {
	SpaceID   string `json:"space_id"`
	DatasetID string `json:"dataset_id"`
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
	SpaceID, DatasetID, Freq string
	PartitionUnit, Timezone  string
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
	pattern := "<yyyy>/<yyyymmdd>" + ext(f.format())
	if in.PartitionUnit == "month" {
		pattern = "<yyyy>/<yyyymm>" + ext(f.format())
	}
	return DatasetMeta{
		SpaceID: in.SpaceID, DatasetID: in.DatasetID, Dataset: dataset, Freq: in.Freq,
		Partition: PartitionMeta{Unit: in.PartitionUnit, Timezone: in.Timezone},
		Files:     FilesMeta{Format: f.format(), PathPattern: pattern, Header: header},
		Columns:   columns, UpdatedAt: now.UTC().Format(time.RFC3339),
	}
}

// WriteDatasetJSON 写 <root>/<space>/<dataset_id>/dataset.json；除更新时间外内容没变时不重写。
func (f Files) WriteDatasetJSON(meta DatasetMeta) error {
	for _, part := range []string{meta.SpaceID, meta.DatasetID} {
		if err := checkComponent(part); err != nil {
			return err
		}
	}
	dir := filepath.Join(f.Root, meta.SpaceID, meta.DatasetID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "dataset.json")
	if raw, err := os.ReadFile(path); err == nil {
		var current DatasetMeta
		if json.Unmarshal(raw, &current) == nil {
			current.UpdatedAt = meta.UpdatedAt
			if a, errA := json.Marshal(current); errA == nil {
				if b, errB := json.Marshal(meta); errB == nil && string(a) == string(b) {
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
