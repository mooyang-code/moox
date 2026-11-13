package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
)

const defaultRepairTimeout = 2 * time.Minute

// repairViewOptions 是 repair-view 的参数：重建指定 View。
type repairViewOptions struct {
	spaceID     string
	viewID      string
	storageConf string
	packageRoot string
	timeout     time.Duration
	yes         bool
	dryRun      bool
	restart     bool
}

type repairViewSummary struct {
	SpaceID            string   `json:"space_id"`
	ViewID             string   `json:"view_id"`
	DBPath             string   `json:"db_path"`
	BackupPath         string   `json:"backup_path,omitempty"`
	ActiveIndexID      string   `json:"active_index_id,omitempty"`
	ViewIndexesRemoved []string `json:"view_indexes_removed,omitempty"`
	Restarted          bool     `json:"restarted"`
	DryRun             bool     `json:"dry_run"`
	Warnings           []string `json:"warnings,omitempty"`
}

type repairViewRecord struct {
	Engine        string
	ActiveIndexID string
}

// runRepairLifecycle 暂停、恢复 storage-view（pause / resume：stop 之后主机守护会把它拉起）；测试替换它，不真的启动服务。
var runRepairLifecycle = runStorageViewLifecycle

// runRepairView 重建指定 View：暂停 storage-view、备份元数据、清掉 View 的活动索引指针、删除物理索引，
// 恢复后由维护流程从 Primary 重新建索引并跟随变更日志。
func runRepairView(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("repair-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := repairViewOptions{timeout: defaultRepairTimeout, restart: true}
	fs.StringVar(&opts.spaceID, "space-id", "", "View 所在空间 ID")
	fs.StringVar(&opts.viewID, "view-id", "", "View ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml 路径")
	fs.StringVar(&opts.packageRoot, "package-root", "", "含 pause.sh、resume.sh 的 Storage 部署包根目录")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "整个操作的超时")
	fs.BoolVar(&opts.yes, "yes", false, "确认删除 View 的索引并重建")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "只查看 View，不暂停服务、不改状态")
	fs.BoolVar(&opts.restart, "restart", opts.restart, "完成后恢复 storage-view")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeRepairViewUsage(fs, stdout)
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("repair-view 不接受多余的参数：%s", strings.Join(fs.Args(), " "))
	}
	if err := validateRepairViewOptions(opts); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return runRepairViewOperation(ctx, opts, stdout, stderr)
}

func validateRepairViewOptions(opts repairViewOptions) error {
	if strings.TrimSpace(opts.spaceID) == "" || strings.TrimSpace(opts.viewID) == "" {
		return errors.New("必须指定 --space-id 与 --view-id")
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout 必须为正数")
	}
	if !opts.yes && !opts.dryRun {
		return errors.New("repair-view 会删除 View 的索引并从 Primary 重建；确认后加 --yes 重新执行，或用 --dry-run 预览")
	}
	return nil
}

func writeRepairViewUsage(fs *flag.FlagSet, stdout io.Writer) {
	if stdout == nil {
		stdout = io.Discard
	}
	fmt.Fprintln(stdout, "用法: moox-cli storage repair-view --space-id <space> --view-id <view> --yes")
	fmt.Fprintln(stdout, "操作: 暂停 storage-view、备份元数据、清掉 View 的活动索引指针并删除物理索引，恢复后从 Primary 重新建索引")
	fs.SetOutput(stdout)
	fs.PrintDefaults()
}

func runRepairViewOperation(ctx context.Context, opts repairViewOptions, stdout, stderr io.Writer) error {
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	packageRoot := resolveRepairPackageRoot(opts.packageRoot, opts.storageConf)
	storage = resolveRepairStoragePaths(storage, packageRoot, opts.storageConf)
	dbPath := metadataDBPath(storage)
	record, err := inspectRepairView(ctx, dbPath, opts.spaceID, opts.viewID)
	if err != nil {
		return err
	}
	summary := repairViewSummary{
		SpaceID:       opts.spaceID,
		ViewID:        opts.viewID,
		DBPath:        dbPath,
		ActiveIndexID: record.ActiveIndexID,
		DryRun:        opts.dryRun,
		Warnings:      []string{"删除索引后 View 不可读，直到从 Primary 重新建完；记录型 View 重建后为空，只跟随新写入"},
	}
	if opts.dryRun {
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: "repair-view", Status: "dry_run", Summary: summary})
	}
	if err := runRepairLifecycle(ctx, packageRoot, "pause", stderr); err != nil {
		return fmt.Errorf("暂停 storage-view：%w", err)
	}
	started := false
	defer func() {
		if !started && opts.restart {
			_ = runRepairLifecycle(context.Background(), packageRoot, "resume", stderr)
		}
	}()
	backupPath, err := backupRepairDB(dbPath)
	if err != nil {
		return fmt.Errorf("备份 Storage 元数据：%w", err)
	}
	summary.BackupPath = backupPath
	if err := resetRepairViewMetadata(ctx, dbPath, opts.spaceID, opts.viewID); err != nil {
		return fmt.Errorf("清掉 View 的活动索引指针：%w", err)
	}
	removed, err := purgeRepairViewIndexes(storage.Devices.ViewIndexRoot, record.Engine, viewindex.ViewIndexID(opts.spaceID, opts.viewID))
	if err != nil {
		return fmt.Errorf("删除 View 索引：%w", err)
	}
	summary.ViewIndexesRemoved = removed
	if opts.restart {
		if err := runRepairLifecycle(ctx, packageRoot, "resume", stderr); err != nil {
			return fmt.Errorf("恢复 storage-view：%w", err)
		}
		started = true
		summary.Restarted = true
	}
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "repair-view", Status: "ok", Summary: summary})
}

func defaultRepairStorageConfigPath() string {
	if configured := strings.TrimSpace(os.Getenv("MOOX_STORAGE_CONFIG")); configured != "" {
		return configured
	}
	if packageRoot := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PACKAGE_ROOT")); packageRoot != "" {
		return filepath.Join(packageRoot, "config", "storage.yaml")
	}
	for _, candidate := range []string{filepath.Join("config", "storage.yaml"), filepath.Join("storage", "config", "storage.yaml")} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return defaultStorageConfigPath()
}

func resolveRepairPackageRoot(configured, configPath string) string {
	if root := strings.TrimSpace(configured); root != "" {
		return findLifecycleRoot(root)
	}
	if root := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PACKAGE_ROOT")); root != "" {
		return findLifecycleRoot(root)
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		return findLifecycleRoot(filepath.Dir(filepath.Dir(abs)))
	}
	return "."
}

// Config files live under <deploy-root>/storage/config while lifecycle
// scripts live at <deploy-root>. Accept either layout so the destructive CLI
// cannot accidentally operate on a data-only subdirectory.
func findLifecycleRoot(candidate string) string {
	candidate = filepath.Clean(candidate)
	for current := candidate; ; current = filepath.Dir(current) {
		if _, err := os.Stat(filepath.Join(current, "start.sh")); err == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return candidate
		}
	}
}

// nativeComponentDirs 返回原生部署里 storage-primary、storage-view 两个组件的目录（都存在才算原生布局）。
func nativeComponentDirs(packageRoot string) (primary, view string, ok bool) {
	root := strings.TrimSpace(packageRoot)
	if root == "" {
		return "", "", false
	}
	dirs := [2]string{filepath.Join(root, "storage-primary"), filepath.Join(root, "storage-view")}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", "", false
		}
	}
	return dirs[0], dirs[1], true
}

// resolveRepairStoragePaths 把配置里的相对数据路径解析成绝对路径。
//
// 原生部署里两个角色各有目录与配置（<包根>/<组件>/config/storage.yaml），配置里的数据路径（./var/storage/...）
// 相对于所属组件的目录，而不是相对于被读取的那一份配置：元数据库与 Pebble 在 storage-primary，
// View 索引在 storage-view。只按被读取配置所在的组件目录解析，会把索引指到不存在的目录。
// 没有这两个组件目录时（单目录布局）按配置所在目录解析。
func resolveRepairStoragePaths(storage storageconfig.StorageConfig, packageRoot, configPath string) storageconfig.StorageConfig {
	if primary, view, ok := nativeComponentDirs(packageRoot); ok {
		join := func(dir, path string) string {
			if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
				return path
			}
			return filepath.Clean(filepath.Join(dir, path))
		}
		storage.Root = join(primary, storage.Root)
		storage.Metadata.Path = join(primary, storage.Metadata.Path)
		storage.Devices.PebblePath = join(primary, storage.Devices.PebblePath)
		storage.Devices.ViewIndexRoot = join(view, storage.Devices.ViewIndexRoot)
		return storage
	}
	return resolveSingleDirStoragePaths(storage, packageRoot, configPath)
}

func resolveSingleDirStoragePaths(storage storageconfig.StorageConfig, packageRoot, configPath string) storageconfig.StorageConfig {
	// Runtime configs use paths relative to their component directory while
	// lifecycle scripts live at the deployment root. Keep those bases separate:
	// <deploy>/storage/config/storage.yaml with root ../data/storage resolves to
	// <deploy>/data/storage, not <deploy>/../data/storage.
	base := strings.TrimSpace(packageRoot)
	if abs, err := filepath.Abs(configPath); err == nil {
		base = filepath.Dir(filepath.Dir(abs))
	}
	resolve := func(path string) string {
		if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Clean(filepath.Join(base, path))
	}
	storage.Root = resolve(storage.Root)
	storage.Metadata.Path = resolve(storage.Metadata.Path)
	storage.Devices.PebblePath = resolve(storage.Devices.PebblePath)
	storage.Devices.ViewIndexRoot = resolve(storage.Devices.ViewIndexRoot)
	return storage
}

func inspectRepairView(ctx context.Context, dbPath, spaceID, viewID string) (repairViewRecord, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return repairViewRecord{}, fmt.Errorf("metadata DB %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return repairViewRecord{}, err
	}
	defer db.Close()
	var record repairViewRecord
	var attrs string
	err = db.QueryRowContext(ctx, `SELECT c_engine, c_active_index_id, c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID).Scan(&record.Engine, &record.ActiveIndexID, &attrs)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return repairViewRecord{}, fmt.Errorf("View %s/%s was not found", spaceID, viewID)
		}
		return repairViewRecord{}, err
	}
	if record.Engine == "" {
		record.Engine = "duckdb"
	}
	if strings.TrimSpace(attrs) == "" || !json.Valid([]byte(attrs)) {
		return repairViewRecord{}, errors.New("View metadata attributes are empty or invalid JSON")
	}
	return record, nil
}

func backupRepairDB(path string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backup := path + ".repair-" + stamp + ".bak"
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
		return "", err
	}
	dst, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(backup)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(backup)
		return "", closeErr
	}
	// SQLite may keep committed metadata in sidecar files while the database
	// is in WAL mode. Preserve those files alongside the main backup so a
	// recovery copy cannot silently lose the latest View state.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := copyOptionalFile(path+suffix, backup+suffix); err != nil {
			_ = os.Remove(backup)
			_ = os.Remove(backup + "-wal")
			_ = os.Remove(backup + "-shm")
			return "", err
		}
	}
	return backup, nil
}

func copyOptionalFile(source, target string) error {
	input, err := os.Open(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return closeErr
	}
	return nil
}

func resetRepairViewMetadata(ctx context.Context, dbPath, spaceID, viewID string) error {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var attrs string
	if err := tx.QueryRowContext(ctx, `SELECT c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID).Scan(&attrs); err != nil {
		return err
	}
	view := &storagepb.View{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(attrs), view); err != nil {
		return fmt.Errorf("decode View attributes: %w", err)
	}
	view.ActiveIndexId = ""
	view.ViewRevision = 0
	view.ActiveColumns = nil
	view.ActiveViewSchemaHash = ""
	view.IndexedFrom = ""
	view.IndexedTo = ""
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(view)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE t_views SET c_active_index_id = '', c_view_revision = 0, c_active_columns_json = '[]', c_active_view_schema_hash = '', c_indexed_from = '', c_indexed_to = '', c_attrs_json = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_view_id = ?`, string(raw), spaceID, viewID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("View %s/%s disappeared while resetting metadata", spaceID, viewID)
	}
	return tx.Commit()
}

func purgeRepairViewIndexes(root, engine string, indexIDs ...string) ([]string, error) {
	if len(indexIDs) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(indexIDs))
	for _, id := range indexIDs {
		if id == "" {
			continue
		}
		if _, err := viewindex.ParseViewIndexID(id); err != nil {
			return nil, fmt.Errorf("invalid View index ID %q: %w", id, err)
		}
		seen[id] = struct{}{}
	}
	var paths []string
	for id := range seen {
		switch strings.ToLower(strings.TrimSpace(engine)) {
		case "duckdb":
			paths = append(paths, filepath.Join(root, "duckdb", id+".duckdb"), filepath.Join(root, "duckdb", id+".duckdb.wal"))
		case "bleve":
			paths = append(paths, filepath.Join(root, "bleve", id))
		default:
			return nil, fmt.Errorf("unsupported View engine %q", engine)
		}
	}
	slices.Sort(paths)
	removed := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return removed, err
		}
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
		removed = append(removed, path)
	}
	return removed, nil
}

// lifecycleScriptArgs 是传给 start.sh / stop.sh 的参数。原生部署的脚本转给 moox-runtime，它不接受位置参数：
// 整个 Storage 单元（serviceName 为 "storage"）不带参数，单个组件用 --components 选择。
func lifecycleScriptArgs(serviceName string) []string {
	serviceName = strings.TrimSpace(serviceName)
	if serviceName == "" || serviceName == "storage" {
		return nil
	}
	return []string{"--components", serviceName}
}

func runStorageViewLifecycle(ctx context.Context, packageRoot, action string, stderr io.Writer) error {
	return runStorageComponentLifecycle(ctx, packageRoot, action, "storage-view", stderr)
}

func runStorageComponentLifecycle(ctx context.Context, packageRoot, action, serviceName string, stderr io.Writer) error {
	packageRoot = strings.TrimSpace(packageRoot)
	if packageRoot == "" {
		return errors.New("storage package root is empty; pass --package-root")
	}
	script := filepath.Join(packageRoot, action+".sh")
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return fmt.Errorf("storage package %s is unavailable", packageRoot)
	}
	cmd := exec.CommandContext(ctx, script, lifecycleScriptArgs(serviceName)...)
	cmd.Dir = packageRoot
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	cmd.Env = append([]string(nil), os.Environ()...)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
