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
	"github.com/mooyang-code/moox/packages/events"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	defaultRepairJSName = events.StorageViewConsumerStream
	// Each View has its own durable, so the consumer to reset is named
	// explicitly with --consumer.
	defaultRepairConsumer = ""
	defaultRepairTimeout  = 2 * time.Minute
)

type repairViewOptions struct {
	action        string
	spaceID       string
	viewID        string
	storageConf   string
	packageRoot   string
	stream        string
	consumer      string
	deliverPolicy string
	timeout       time.Duration
	yes           bool
	dryRun        bool
	resetConsumer bool
	restart       bool
	resetView     bool
}

type repairViewSummary struct {
	SpaceID            string   `json:"space_id"`
	ViewID             string   `json:"view_id"`
	DBPath             string   `json:"db_path"`
	BackupPath         string   `json:"backup_path,omitempty"`
	ActiveIndexID      string   `json:"active_index_id,omitempty"`
	ConsumerReset      bool     `json:"consumer_reset"`
	ViewIndexesRemoved []string `json:"view_indexes_removed,omitempty"`
	ViewReset          bool     `json:"view_reset"`
	Restarted          bool     `json:"restarted"`
	DryRun             bool     `json:"dry_run"`
	Warnings           []string `json:"warnings,omitempty"`
}

type repairViewRecord struct {
	Engine        string
	ActiveIndexID string
}

// These hooks keep the destructive orchestration testable without connecting
// a unit test to the operator's NATS broker or starting a real service.
var deleteRepairConsumer = deleteJetStreamConsumer
var runRepairLifecycle = runStorageViewLifecycle

func runRepairView(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := newRepairViewFlagSet()
	opts := repairViewOptions{}
	opts.action = "repair-view"
	bindRepairViewFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeRepairViewUsage(fs, stdout)
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected repair-view arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateRepairViewOptions(&opts); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return runRepairViewOperation(ctx, opts, stdout, stderr)
}

type forceRebuildViewOptions struct {
	spaceID     string
	viewID      string
	storageConf string
	packageRoot string
	stream      string
	consumer    string
	timeout     time.Duration
	yes         bool
	dryRun      bool
	restart     bool
}

func runForceRebuildView(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("force-rebuild-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := forceRebuildViewOptions{stream: defaultRepairJSName, consumer: defaultRepairConsumer, timeout: defaultRepairTimeout, restart: true}
	fs.StringVar(&opts.spaceID, "space-id", "", "View space ID")
	fs.StringVar(&opts.viewID, "view-id", "", "View ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream")
	fs.StringVar(&opts.consumer, "consumer", opts.consumer, "durable storage View consumer")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall operation timeout")
	fs.BoolVar(&opts.yes, "yes", false, "confirm permanent View/index/consumer deletion")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "inspect the View without changing state")
	fs.BoolVar(&opts.restart, "restart", opts.restart, "restart storage-view after maintenance")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			writeForceRebuildViewUsage(fs, stdout)
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected force-rebuild-view arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateForceRebuildViewOptions(opts); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return runRepairViewOperation(ctx, repairViewOptions{
		action:        "force-rebuild-view",
		spaceID:       opts.spaceID,
		viewID:        opts.viewID,
		storageConf:   opts.storageConf,
		packageRoot:   opts.packageRoot,
		stream:        opts.stream,
		consumer:      opts.consumer,
		deliverPolicy: "all",
		timeout:       opts.timeout,
		yes:           opts.yes,
		dryRun:        opts.dryRun,
		resetConsumer: true,
		restart:       opts.restart,
		resetView:     true,
	}, stdout, stderr)
}

func validateForceRebuildViewOptions(opts forceRebuildViewOptions) error {
	if strings.TrimSpace(opts.spaceID) == "" || strings.TrimSpace(opts.viewID) == "" {
		return errors.New("--space-id and --view-id are required")
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if strings.TrimSpace(opts.stream) == "" || strings.TrimSpace(opts.consumer) == "" {
		return errors.New("--stream and --consumer must not be empty")
	}
	if err := checkViewConsumers(opts.consumer); err != nil {
		return fmt.Errorf("--consumer: %w", err)
	}
	if !opts.yes && !opts.dryRun {
		return errors.New("force-rebuild-view permanently deletes the View indexes and consumer; re-run with --yes, or use --dry-run")
	}
	return nil
}

func writeForceRebuildViewUsage(fs *flag.FlagSet, stdout io.Writer) {
	if stdout == nil {
		stdout = io.Discard
	}
	fmt.Fprintln(stdout, "用法: moox-cli storage force-rebuild-view --space-id <space> --view-id <view> --yes")
	fmt.Fprintln(stdout, "操作: 停止 storage-view、删除 durable consumer、删除 View 索引和活动索引指针，重启后从 Primary 重新建索引")
	fs.SetOutput(stdout)
	fs.PrintDefaults()
}

func runRepairViewOperation(ctx context.Context, opts repairViewOptions, stdout, stderr io.Writer) error {
	if strings.TrimSpace(opts.action) == "" {
		opts.action = "repair-view"
	}
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
	}
	if opts.resetView {
		summary.Warnings = append(summary.Warnings, "reset-view-indexes 删除索引后 View 不可读，直到从 Primary 重新建完")
	}
	// 要删消费者时，在停止任何服务之前先确认网关、Admin 与 EventBus 都可用、流存在：
	// 否则可能 View 已被停止才发现连不上。dry-run 也走这一步，让它真正成为预检。
	if opts.resetConsumer {
		if err := preflightRepairConsumer(ctx, opts.storageConf, packageRoot, opts.stream, opts.consumer); err != nil {
			return fmt.Errorf("preflight EventBus: %w", err)
		}
	}
	if opts.dryRun {
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: opts.action, Status: "dry_run", Summary: summary})
	}
	if !opts.yes {
		return errors.New("repair-view changes the durable consumer and metadata; re-run with --yes, or use --dry-run")
	}

	if err := runRepairLifecycle(ctx, packageRoot, "stop", opts.deliverPolicy, stderr); err != nil {
		return fmt.Errorf("stop storage-view: %w", err)
	}
	stopped := true
	started := false
	defer func() {
		if stopped && !started && opts.restart {
			_ = runRepairLifecycle(context.Background(), packageRoot, "start", opts.deliverPolicy, stderr)
		}
	}()

	backupPath, err := backupRepairDB(dbPath)
	if err != nil {
		return fmt.Errorf("backup storage metadata: %w", err)
	}
	summary.BackupPath = backupPath
	if opts.resetConsumer {
		deleted, err := deleteRepairConsumer(ctx, opts.storageConf, packageRoot, opts.stream, opts.consumer)
		if err != nil {
			return fmt.Errorf("reset storage view consumer: %w", err)
		}
		summary.ConsumerReset = deleted
	}
	if opts.resetView {
		if err := resetRepairViewMetadata(ctx, dbPath, opts.spaceID, opts.viewID); err != nil {
			return fmt.Errorf("reset View metadata: %w", err)
		}
		removed, err := purgeRepairViewIndexes(storage.Devices.ViewIndexRoot, record.Engine, viewindex.ViewIndexID(opts.spaceID, opts.viewID))
		if err != nil {
			return fmt.Errorf("delete View indexes: %w", err)
		}
		summary.ViewIndexesRemoved = removed
		summary.ViewReset = true
	}
	if opts.restart {
		if err := runRepairLifecycle(ctx, packageRoot, "start", opts.deliverPolicy, stderr); err != nil {
			return fmt.Errorf("start storage-view: %w", err)
		}
		started = true
		summary.Restarted = true
	}
	stopped = false
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: opts.action, Status: "ok", Summary: summary})
}

func newRepairViewFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("repair-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func bindRepairViewFlags(fs *flag.FlagSet, opts *repairViewOptions) {
	fs.StringVar(&opts.spaceID, "space-id", "", "View space ID")
	fs.StringVar(&opts.viewID, "view-id", "", "View ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", defaultRepairJSName, "JetStream stream")
	fs.StringVar(&opts.consumer, "consumer", defaultRepairConsumer, "durable storage View consumer")
	fs.StringVar(&opts.deliverPolicy, "deliver-policy", "new", "consumer policy after reset: new or all")
	fs.DurationVar(&opts.timeout, "timeout", defaultRepairTimeout, "overall operation timeout")
	fs.BoolVar(&opts.yes, "yes", false, "confirm the maintenance operation")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "inspect the View without stopping services or changing state")
	fs.BoolVar(&opts.resetConsumer, "reset-consumer", true, "delete the durable View consumer before restart")
	fs.BoolVar(&opts.restart, "restart", true, "restart storage-view after maintenance")
	fs.BoolVar(&opts.resetView, "reset-view-indexes", false, "delete the physical index and rebuild it from Primary")
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

func writeRepairViewUsage(fs *flag.FlagSet, stdout io.Writer) {
	if stdout == nil {
		stdout = io.Discard
	}
	fmt.Fprintln(stdout, "用法: moox-cli storage repair-view --space-id <space> --view-id <view> --yes")
	fmt.Fprintln(stdout, "默认操作: 停止 storage-view、删除 durable View consumer、备份元数据并重启服务；加 --reset-view-indexes 时删除索引，重启后从 Primary 重新建")
	fs.SetOutput(stdout)
	fs.PrintDefaults()
}

func validateRepairViewOptions(opts *repairViewOptions) error {
	if strings.TrimSpace(opts.spaceID) == "" || strings.TrimSpace(opts.viewID) == "" {
		return errors.New("--space-id and --view-id are required")
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if strings.TrimSpace(opts.stream) == "" || strings.TrimSpace(opts.consumer) == "" {
		return errors.New("--stream and --consumer must not be empty")
	}
	if opts.resetConsumer {
		if err := checkViewConsumers(opts.consumer); err != nil {
			return fmt.Errorf("--consumer: %w", err)
		}
	}
	policy := strings.ToLower(strings.TrimSpace(opts.deliverPolicy))
	if policy != "new" && policy != "all" {
		return fmt.Errorf("--deliver-policy %q is unsupported; want new or all", opts.deliverPolicy)
	}
	opts.deliverPolicy = policy
	if !opts.resetConsumer && !opts.resetView {
		return errors.New("nothing to repair: enable --reset-consumer or --reset-view-indexes")
	}
	if opts.resetView && !opts.yes && !opts.dryRun {
		return errors.New("index deletion requires --yes")
	}
	if opts.resetView && opts.deliverPolicy != "all" {
		return errors.New("--reset-view-indexes requires --deliver-policy=all so retained source events can replay")
	}
	return nil
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

// nativeComponentDirs 返回原生部署里 storage-primary、storage-node、storage-view 三个组件的目录（都存在才算原生布局）。
func nativeComponentDirs(packageRoot string) (primary, node, view string, ok bool) {
	root := strings.TrimSpace(packageRoot)
	if root == "" {
		return "", "", "", false
	}
	dirs := [3]string{filepath.Join(root, "storage-primary"), filepath.Join(root, "storage-node"), filepath.Join(root, "storage-view")}
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return "", "", "", false
		}
	}
	return dirs[0], dirs[1], dirs[2], true
}

// resolveRepairStoragePaths 把配置里的相对数据路径解析成绝对路径。
//
// 原生部署里三个角色各有目录与配置（<包根>/<组件>/config/storage.yaml），配置里的数据路径（./var/storage/...）
// 相对于所属组件的目录，而不是相对于被读取的那一份配置：元数据库在 storage-primary，Pebble 在 storage-node，
// View 索引在 storage-view。只按被读取配置所在的组件目录解析，会把 Pebble、索引指到不存在的目录，
// “重置全部数据”删不到真正的数据却照样清空事件流。没有这三个组件目录时（单目录布局）按配置所在目录解析。
func resolveRepairStoragePaths(storage storageconfig.StorageConfig, packageRoot, configPath string) storageconfig.StorageConfig {
	if primary, node, view, ok := nativeComponentDirs(packageRoot); ok {
		join := func(dir, path string) string {
			if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
				return path
			}
			return filepath.Clean(filepath.Join(dir, path))
		}
		storage.Root = join(primary, storage.Root)
		storage.Metadata.Path = join(primary, storage.Metadata.Path)
		storage.Devices.PebblePath = join(node, storage.Devices.PebblePath)
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

// restoreRepairDB restores the SQLite main file and its WAL sidecars from a
// reset backup. Reset is deliberately destructive, so callers use this only
// on a pre-commit failure while the storage role is stopped. Copying to
// temporary files before rename keeps a partial restore from becoming the
// next failure mode.
func restoreRepairDB(backup, path string) error {
	if strings.TrimSpace(backup) == "" || strings.TrimSpace(path) == "" {
		return errors.New("repair database backup and destination are required")
	}
	copyReplace := func(source, target string) error {
		tmp := target + ".restore-tmp"
		_ = os.Remove(tmp)
		if err := copyFile(source, tmp); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, target); err != nil {
			_ = os.Remove(tmp)
			return err
		}
		return nil
	}
	if err := copyReplace(backup, path); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		source := backup + suffix
		target := path + suffix
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(target)
			continue
		} else if err != nil {
			return err
		}
		if err := copyReplace(source, target); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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
	// These protocol checkpoints belong to the old View incarnation. Keeping
	// them would make replayed period/sync events look already applied and
	// would prevent a true from-scratch rebuild from publishing readiness.
	for _, table := range []string{"t_view_period_dataset_states", "t_view_sync_points"} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID); err != nil {
			return err
		}
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

// preflightRepairConsumer 不改动任何状态，确认能经 Admin 访问 EventBus 且流存在。
var preflightRepairConsumer = func(ctx context.Context, storageConf, packageRoot, stream, consumer string) error {
	if err := checkViewConsumers(consumer); err != nil {
		return err
	}
	admin, err := openEventBusAdmin(storageConf, packageRoot)
	if err != nil {
		return err
	}
	defer admin.Close()
	_, err = admin.Inspect(ctx, stream, []string{consumer})
	return err
}

// deleteJetStreamConsumer 经 Admin 删除 View 消费者；消费者本来就不存在时返回 false。
func deleteJetStreamConsumer(ctx context.Context, storageConf, packageRoot, stream, consumer string) (bool, error) {
	if err := checkViewConsumers(consumer); err != nil {
		return false, err
	}
	admin, err := openEventBusAdmin(storageConf, packageRoot)
	if err != nil {
		return false, err
	}
	defer admin.Close()
	deleted, _, err := admin.DeleteConsumers(ctx, stream, []string{consumer})
	return len(deleted) > 0, err
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

func runStorageViewLifecycle(ctx context.Context, packageRoot, action, deliverPolicy string, stderr io.Writer) error {
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
