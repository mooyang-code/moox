// Package dataformat 定义各组件本地数据的格式版本常量、复合格式值计算与格式文件读写。
//
// 每个有本地数据的组件在启动校验通过后，把自己的格式记录写进数据目录的 data-format 文件；
// 部署预检经 SSH 读取该文件，与当前版本算出的期望格式比对，在动手之前回答“数据能不能保留”。
package dataformat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// FileName 是各组件数据目录下存放数据格式记录的文件名。
const FileName = "data-format"

// 四个由代码决定、无法从 schema 或 proto 描述自动推导的格式版本常量。
// 对应本地落盘格式有破坏性变更时把对应常量加一。
const (
	// StoragePebbleLayoutVersion 是 storage-primary Pebble 库的键与行布局版本（与 storage_layout_version 文件一致）。
	StoragePebbleLayoutVersion = 5
	// StorageViewIndexVersion 是 storage-view 索引（DuckDB / Bleve）的格式版本。
	StorageViewIndexVersion = 1
	// FactorEngineStateVersion 是 factor-engine 本地状态（seriesstore、watermark、gapledger、param_cache.json）的格式版本。
	FactorEngineStateVersion = 1
	// LifecycleStateVersion 是 packages/lifecycle 持久化文件 lifecycle.json 的结构版本。
	LifecycleStateVersion = 1
)

// 格式组成部分的规范键名。
const (
	PartSQLiteSchema        = "sqlite_schema"
	PartStagingSchema       = "staging_schema"
	PartEvents              = "events"
	PartEventBusTopology    = "eventbus_topology"
	PartStoragePebbleLayout = "storage_pebble_layout"
	PartStorageViewIndex    = "storage_view_index"
	PartFactorEngineState   = "factor_engine_state"
	PartLifecycle           = "lifecycle"
)

// ErrIncompatibleFormat 表示本地已有数据对应的 data-format 与当前版本格式不符。
var ErrIncompatibleFormat = errors.New("本地数据格式与当前版本不符")

// Record 是落盘到 data-format 文件的组件格式快照。
type Record struct {
	Component string            `json:"component"`
	Format    string            `json:"format"`
	Parts     map[string]string `json:"parts"`
}

// LifecycleParts 返回仅含生命周期格式版本常量的附加部分映射，供使用 packages/lifecycle 的 SQLite 模块复用。
func LifecycleParts() map[string]string {
	return map[string]string{
		PartLifecycle: strconv.Itoa(LifecycleStateVersion),
	}
}

// Build 由组件 ID 与各组成部分计算复合格式摘要并返回格式记录。
func Build(component string, parts map[string]string) (Record, error) {
	component = strings.TrimSpace(component)
	if component == "" {
		return Record{}, errors.New("数据格式记录缺少组件名")
	}
	if len(parts) == 0 {
		return Record{}, fmt.Errorf("组件 %s 的数据格式组成部分不能为空", component)
	}
	keys := make([]string, 0, len(parts))
	cloned := make(map[string]string, len(parts))
	for k, v := range parts {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			return Record{}, fmt.Errorf("组件 %s 的数据格式部分键值不能为空", component)
		}
		keys = append(keys, k)
		cloned[k] = v
	}
	slices.Sort(keys)
	hash := sha256.New()
	fmt.Fprintf(hash, "component\x00%s\x00", component)
	for _, k := range keys {
		fmt.Fprintf(hash, "%s\x00%s\x00", k, cloned[k])
	}
	return Record{
		Component: component,
		Format:    "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		Parts:     cloned,
	}, nil
}

// Parse 解析并校验格式文件内容；内容缺失、非法或摘要与 parts 不一致时返回错误。
func Parse(raw []byte) (Record, error) {
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("解析数据格式文件失败：%w", err)
	}
	rebuilt, err := Build(rec.Component, rec.Parts)
	if err != nil {
		return Record{}, err
	}
	if rec.Format != rebuilt.Format {
		return Record{}, fmt.Errorf("组件 %s 的数据格式摘要与组成部分不匹配", rec.Component)
	}
	return rebuilt, nil
}

// ReadFile 读取并校验指定路径的格式文件。
func ReadFile(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	return Parse(raw)
}

// WriteDir 把格式记录原子写入 dir 下的 data-format 文件。
func WriteDir(dir string, rec Record) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("数据格式目录不能为空")
	}
	return WriteFile(filepath.Join(dir, FileName), rec)
}

// WriteFile 把格式记录原子写入指定文件（先写同目录临时文件并落盘，再改名替换）。
func WriteFile(path string, rec Record) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("数据格式文件路径不能为空")
	}
	validated, err := Build(rec.Component, rec.Parts)
	if err != nil {
		return err
	}
	if rec.Format != "" && rec.Format != validated.Format {
		return fmt.Errorf("组件 %s 的数据格式摘要与组成部分不匹配", rec.Component)
	}
	raw, err := json.MarshalIndent(validated, "", "  ")
	if err != nil {
		return fmt.Errorf("编码数据格式文件失败：%w", err)
	}
	raw = append(raw, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建数据格式目录 %s 失败：%w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建数据格式临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置数据格式文件权限失败：%w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入数据格式临时文件失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("刷新数据格式临时文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭数据格式临时文件失败：%w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换数据格式文件 %s 失败：%w", path, err)
	}
	cleanup = false
	return nil
}

// DiffParts 返回两份格式记录之间取值不同或增删的组成部分名（已排序）。
func DiffParts(want, got Record) []string {
	seen := make(map[string]struct{}, len(want.Parts)+len(got.Parts))
	for k := range want.Parts {
		seen[k] = struct{}{}
	}
	for k := range got.Parts {
		seen[k] = struct{}{}
	}
	var diff []string
	for k := range seen {
		if want.Parts[k] != got.Parts[k] {
			diff = append(diff, k)
		}
	}
	slices.Sort(diff)
	return diff
}

// VerifyOrWriteDir 在 dir 下的 data-format 文件执行启动时的格式核对与落盘（见 VerifyOrWriteFile）。
func VerifyOrWriteDir(dir string, rec Record, hasExistingData bool) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("数据格式目录不能为空")
	}
	return VerifyOrWriteFile(filepath.Join(dir, FileName), rec, hasExistingData)
}

// VerifyOrWriteFile 是各组件启动时共用的格式核对与落盘逻辑：
//   - 若尚无已有数据（hasExistingData 为假），或格式文件尚不存在：照常写入 rec。
//   - 若已有数据且格式文件存在：解析并比对组件名与格式值；不一致或格式文件损坏时拒绝启动，提示执行全新部署（setup install --wipe）；一致时放行。
func VerifyOrWriteFile(path string, rec Record, hasExistingData bool) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("数据格式文件路径不能为空")
	}
	validated, err := Build(rec.Component, rec.Parts)
	if err != nil {
		return err
	}
	if rec.Format != "" && rec.Format != validated.Format {
		return fmt.Errorf("组件 %s 的数据格式摘要与组成部分不匹配", rec.Component)
	}
	if !hasExistingData {
		return WriteFile(path, validated)
	}
	existing, err := ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WriteFile(path, validated)
		}
		return fmt.Errorf("%w：组件 %s 的格式文件 %s 无法解析（%v）；请备份后执行全新部署（moox-cli setup install --wipe）",
			ErrIncompatibleFormat, validated.Component, path, err)
	}
	if existing.Component != validated.Component {
		return fmt.Errorf("%w：目录 %s 的格式文件属于组件 %s，与当前组件 %s 不符；请备份后执行全新部署（moox-cli setup install --wipe）",
			ErrIncompatibleFormat, filepath.Dir(path), existing.Component, validated.Component)
	}
	if existing.Format != validated.Format {
		diff := DiffParts(validated, existing)
		detail := "格式摘要不一致"
		if len(diff) > 0 {
			detail = "变更项：" + strings.Join(diff, "、")
		}
		return fmt.Errorf("%w：组件 %s 的本地数据格式已变化（%s）；请备份后执行全新部署（moox-cli setup install --wipe）",
			ErrIncompatibleFormat, validated.Component, detail)
	}
	return nil
}

// DirHasEntries 报告目录是否存在且包含除 data-format 及临时格式文件外的任何条目。
func DirHasEntries(dir string) bool {
	entries, err := os.ReadDir(strings.TrimSpace(dir))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == FileName || strings.HasPrefix(name, "."+FileName+".tmp-") {
			continue
		}
		return true
	}
	return false
}
