package main

import (
	"context"
	"crypto/x509"
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
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	defaultRepairStream   = "MOOX_STORAGE"
	defaultRepairConsumer = events.StorageViewKlineConsumer
	defaultRepairTimeout  = 2 * time.Minute
)

type repairViewOptions struct {
	action         string
	spaceID        string
	viewID         string
	storageConf    string
	packageRoot    string
	stream         string
	consumer       string
	credentialFile string
	eventBusURL    string
	deliverPolicy  string
	lookback       time.Duration
	timeout        time.Duration
	yes            bool
	dryRun         bool
	resetConsumer  bool
	forceRebuild   bool
	restart        bool
	purgeInactive  bool
	resetView      bool
}

type repairViewSummary struct {
	SpaceID                 string   `json:"space_id"`
	ViewID                  string   `json:"view_id"`
	DBPath                  string   `json:"db_path"`
	BackupPath              string   `json:"backup_path,omitempty"`
	ActiveIndexID           string   `json:"active_index_id,omitempty"`
	InactiveIndexID         string   `json:"inactive_index_id,omitempty"`
	PreviousDesiredRevision uint64   `json:"previous_desired_revision"`
	DesiredRevision         uint64   `json:"desired_revision"`
	ConsumerReset           bool     `json:"consumer_reset"`
	RebuildTriggered        bool     `json:"rebuild_triggered"`
	InactiveIndexesRemoved  []string `json:"inactive_indexes_removed,omitempty"`
	ViewIndexesRemoved      []string `json:"view_indexes_removed,omitempty"`
	ViewReset               bool     `json:"view_reset"`
	RebuildLookback         string   `json:"rebuild_lookback,omitempty"`
	Restarted               bool     `json:"restarted"`
	DryRun                  bool     `json:"dry_run"`
	Warnings                []string `json:"warnings,omitempty"`
}

type repairViewRecord struct {
	Engine          string
	ActiveIndexID   string
	DesiredRevision uint64
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
	spaceID        string
	viewID         string
	storageConf    string
	packageRoot    string
	stream         string
	consumer       string
	credentialFile string
	eventBusURL    string
	lookback       time.Duration
	timeout        time.Duration
	yes            bool
	dryRun         bool
	restart        bool
}

func runForceRebuildView(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("force-rebuild-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := forceRebuildViewOptions{stream: defaultRepairStream, consumer: defaultRepairConsumer, timeout: defaultRepairTimeout, restart: true}
	fs.StringVar(&opts.spaceID, "space-id", "", "View space ID")
	fs.StringVar(&opts.viewID, "view-id", "", "View ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream")
	fs.StringVar(&opts.consumer, "consumer", opts.consumer, "durable storage View consumer")
	fs.StringVar(&opts.credentialFile, "credential-file", "", "NATS admin credential file")
	fs.StringVar(&opts.eventBusURL, "eventbus-url", "", "NATS URL override")
	fs.DurationVar(&opts.lookback, "lookback", 0, "preferred history to backfill; shortage does not block activation")
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
		action:         "force-rebuild-view",
		spaceID:        opts.spaceID,
		viewID:         opts.viewID,
		storageConf:    opts.storageConf,
		packageRoot:    opts.packageRoot,
		stream:         opts.stream,
		consumer:       opts.consumer,
		credentialFile: opts.credentialFile,
		eventBusURL:    opts.eventBusURL,
		deliverPolicy:  "all",
		lookback:       opts.lookback,
		timeout:        opts.timeout,
		yes:            opts.yes,
		dryRun:         opts.dryRun,
		resetConsumer:  true,
		restart:        opts.restart,
		resetView:      true,
	}, stdout, stderr)
}

func validateForceRebuildViewOptions(opts forceRebuildViewOptions) error {
	if strings.TrimSpace(opts.spaceID) == "" || strings.TrimSpace(opts.viewID) == "" {
		return errors.New("--space-id and --view-id are required")
	}
	if opts.lookback <= 0 {
		return errors.New("--lookback must be a positive duration")
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if strings.TrimSpace(opts.stream) == "" || strings.TrimSpace(opts.consumer) == "" {
		return errors.New("--stream and --consumer must not be empty")
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
	fmt.Fprintln(stdout, "用法: moox-cli storage force-rebuild-view --space-id <space> --view-id <view> --lookback <duration> --yes")
	fmt.Fprintln(stdout, "操作: 停止 storage-view、删除 durable consumer、删除旧 View 索引和运行时指针，从可重放事件重新创建 View")
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
		SpaceID:                 opts.spaceID,
		ViewID:                  opts.viewID,
		DBPath:                  dbPath,
		ActiveIndexID:           record.ActiveIndexID,
		InactiveIndexID:         viewindex.InactiveViewIndexID(opts.spaceID, opts.viewID, record.ActiveIndexID),
		PreviousDesiredRevision: record.DesiredRevision,
		DesiredRevision:         nextViewRevision(record.DesiredRevision),
		DryRun:                  opts.dryRun,
	}
	if opts.lookback > 0 {
		summary.RebuildLookback = opts.lookback.String()
	}
	if summary.ActiveIndexID != "" {
		summary.Warnings = append(summary.Warnings, "active view index is preserved; the rebuild uses the normal A/B switch path")
	}
	if opts.purgeInactive {
		summary.Warnings = append(summary.Warnings, "only the inactive slot is purged; the active slot is never deleted by this command")
	}
	if opts.resetView {
		summary.Warnings = append(summary.Warnings, "reset-view-indexes deletes both physical slots and requires replayable source events")
	}
	if opts.dryRun {
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: opts.action, Status: "dry_run", Summary: summary})
	}
	if !opts.yes {
		return errors.New("repair-view changes the durable consumer and metadata; re-run with --yes, or use --dry-run")
	}

	if err := runRepairLifecycle(ctx, packageRoot, "stop", opts.deliverPolicy, opts.lookback, stderr); err != nil {
		return fmt.Errorf("stop storage-view: %w", err)
	}
	stopped := true
	started := false
	defer func() {
		if stopped && !started && opts.restart {
			_ = runRepairLifecycle(context.Background(), packageRoot, "start", opts.deliverPolicy, opts.lookback, stderr)
		}
	}()

	backupPath, err := backupRepairDB(dbPath)
	if err != nil {
		return fmt.Errorf("backup storage metadata: %w", err)
	}
	summary.BackupPath = backupPath
	if opts.resetConsumer {
		deleted, err := deleteRepairConsumer(ctx, repairConsumerOptions{
			Stream:         opts.stream,
			Consumer:       opts.consumer,
			CredentialFile: opts.credentialFile,
			EventBusURL:    opts.eventBusURL,
		})
		if err != nil {
			return fmt.Errorf("reset storage view consumer: %w", err)
		}
		summary.ConsumerReset = deleted
	}
	if opts.forceRebuild && !opts.resetView {
		if err := forceRepairViewRevision(ctx, dbPath, opts.spaceID, opts.viewID, summary.DesiredRevision); err != nil {
			return fmt.Errorf("force View rebuild: %w", err)
		}
		summary.RebuildTriggered = true
	}
	if opts.resetView {
		if err := resetRepairViewMetadata(ctx, dbPath, opts.spaceID, opts.viewID, summary.DesiredRevision); err != nil {
			return fmt.Errorf("reset View metadata: %w", err)
		}
		// Metadata may already have lost its active pointer after an interrupted
		// reset, while the other physical slot still exists. Derive both stable
		// slot IDs instead of relying only on the recorded active pointer.
		removed, err := purgeRepairViewIndexes(storage.Devices.ViewIndexRoot, record.Engine,
			viewindex.ViewIndexID(opts.spaceID, opts.viewID, viewindex.SlotA),
			viewindex.ViewIndexID(opts.spaceID, opts.viewID, viewindex.SlotB),
		)
		if err != nil {
			return fmt.Errorf("delete View indexes: %w", err)
		}
		summary.ViewIndexesRemoved = removed
		summary.ViewReset = true
		summary.RebuildTriggered = true
	}
	if opts.purgeInactive {
		removed, err := purgeRepairViewIndexes(storage.Devices.ViewIndexRoot, record.Engine, summary.InactiveIndexID)
		if err != nil {
			return fmt.Errorf("purge inactive View index: %w", err)
		}
		summary.InactiveIndexesRemoved = removed
	}
	if opts.restart {
		if err := runRepairLifecycle(ctx, packageRoot, "start", opts.deliverPolicy, opts.lookback, stderr); err != nil {
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
	fs.StringVar(&opts.stream, "stream", defaultRepairStream, "JetStream stream")
	fs.StringVar(&opts.consumer, "consumer", defaultRepairConsumer, "durable storage View consumer")
	fs.StringVar(&opts.credentialFile, "credential-file", "", "NATS admin credential file")
	fs.StringVar(&opts.eventBusURL, "eventbus-url", "", "NATS URL override")
	fs.StringVar(&opts.deliverPolicy, "deliver-policy", "new", "consumer policy after reset: new or all")
	fs.DurationVar(&opts.lookback, "lookback", 0, "preferred history for every rebuilt View; shortage does not block activation")
	fs.DurationVar(&opts.timeout, "timeout", defaultRepairTimeout, "overall operation timeout")
	fs.BoolVar(&opts.yes, "yes", false, "confirm the maintenance operation")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "inspect the View without stopping services or changing state")
	fs.BoolVar(&opts.resetConsumer, "reset-consumer", true, "delete the durable View consumer before restart")
	fs.BoolVar(&opts.forceRebuild, "force-rebuild", true, "bump the desired View revision to trigger A/B rebuild")
	fs.BoolVar(&opts.restart, "restart", true, "restart storage-view after maintenance")
	fs.BoolVar(&opts.purgeInactive, "purge-inactive-index", false, "delete only the inactive physical index slot")
	fs.BoolVar(&opts.resetView, "reset-view-indexes", false, "delete both physical slots and rebuild from retained source events")
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
	fmt.Fprintln(stdout, "默认操作: 停止 storage-view、删除 durable View consumer、备份元数据、触发 A/B 重建并重启服务")
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
	policy := strings.ToLower(strings.TrimSpace(opts.deliverPolicy))
	if policy != "new" && policy != "all" {
		return fmt.Errorf("--deliver-policy %q is unsupported; want new or all", opts.deliverPolicy)
	}
	opts.deliverPolicy = policy
	if !opts.resetConsumer && !opts.forceRebuild && !opts.purgeInactive && !opts.resetView {
		return errors.New("nothing to repair: enable --reset-consumer, --force-rebuild, --purge-inactive-index, or --reset-view-indexes")
	}
	if (opts.purgeInactive || opts.resetView) && !opts.yes && !opts.dryRun {
		return errors.New("index deletion requires --yes")
	}
	if opts.resetView && opts.deliverPolicy != "all" {
		return errors.New("--reset-view-indexes requires --deliver-policy=all so retained source events can replay")
	}
	if opts.resetView && opts.lookback <= 0 {
		return errors.New("--reset-view-indexes requires a positive --lookback")
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

func resolveRepairStoragePaths(storage storageconfig.StorageConfig, packageRoot, configPath string) storageconfig.StorageConfig {
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
	err = db.QueryRowContext(ctx, `SELECT c_engine, c_active_index_id, c_desired_view_revision, c_attrs_json FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID).Scan(&record.Engine, &record.ActiveIndexID, &record.DesiredRevision, &attrs)
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

func nextViewRevision(current uint64) uint64 {
	if current == 0 {
		return 2
	}
	return current + 1
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

func forceRepairViewRevision(ctx context.Context, dbPath, spaceID, viewID string, revision uint64) error {
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
	view.DesiredViewRevision = revision
	view.Columns = nil
	view.IndexBuild = nil
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(view)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE t_views SET c_desired_view_revision = ?, c_attrs_json = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_view_id = ?`, revision, string(raw), spaceID, viewID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("View %s/%s disappeared while updating metadata", spaceID, viewID)
	}
	return tx.Commit()
}

func resetRepairViewMetadata(ctx context.Context, dbPath, spaceID, viewID string, revision uint64) error {
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
	view.DesiredViewRevision = revision
	view.ActiveIndexId = ""
	view.ActiveViewRevision = 0
	view.ActiveColumns = nil
	view.ActiveViewSchemaHash = ""
	view.ActiveSlot = "slot-a"
	view.IndexedFrom = ""
	view.IndexedTo = ""
	view.IndexBuild = nil
	raw, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(view)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE t_views SET c_active_index_id = '', c_desired_view_revision = ?, c_active_view_revision = 0, c_active_columns_json = '[]', c_active_view_schema_hash = '', c_active_slot = 'slot-a', c_indexed_from = '', c_indexed_to = '', c_attrs_json = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_space_id = ? AND c_view_id = ?`, revision, string(raw), spaceID, viewID)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID); err != nil {
		return err
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

type repairConsumerOptions struct {
	Stream, Consumer, CredentialFile, EventBusURL string
}

func deleteJetStreamConsumer(ctx context.Context, opts repairConsumerOptions) (bool, error) {
	credentialPath := resolveRepairCredentialFile(opts.CredentialFile)
	if credentialPath == "" {
		return false, errors.New("NATS admin credential is required; pass --credential-file or MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE")
	}
	cred, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return false, err
	}
	urls := cred.URLs
	if strings.TrimSpace(opts.EventBusURL) != "" {
		urls = []string{strings.TrimSpace(opts.EventBusURL)}
	}
	if len(urls) == 0 {
		urls = []string{"tls://127.0.0.1:4222"}
	}
	natsOpts := []nats.Option{nats.Name("moox-storage-cli-repair-view"), nats.UserInfo(cred.Username, cred.Password)}
	if cred.CAFile != "" {
		caPath := jetstream.ExpandCredentialPath(cred.CAFile)
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		if err := appendNATSTLSOptions(&natsOpts, caPath); err != nil {
			return false, err
		}
	}
	// nats.Connect has its own dial timeout; keep consumer reset from consuming
	// the entire maintenance deadline when the local broker is unavailable.
	natsOpts = append(natsOpts, nats.Timeout(15*time.Second))
	nc, err := nats.Connect(strings.Join(urls, ","), natsOpts...)
	if err != nil {
		return false, err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return false, err
	}
	err = js.DeleteConsumer(opts.Stream, opts.Consumer)
	if errors.Is(err, nats.ErrConsumerNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func appendNATSTLSOptions(opts *[]nats.Option, caPath string) error {
	caPath = jetstream.ExpandCredentialPath(caPath)
	ca, err := os.ReadFile(caPath)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return errors.New("NATS CA file contains no certificates")
	}
	*opts = append(*opts, nats.RootCAs(caPath), nats.Secure(), nats.TLSHandshakeFirst())
	return nil
}

func resolveRepairCredentialFile(configured string) string {
	for _, value := range []string{
		configured,
		os.Getenv("MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE"),
		os.Getenv("MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE"),
		os.Getenv("MOOX_EVENTBUS_INTERNAL_CREDENTIAL_FILE"),
		"~/.config/moox/eventbus/internal-admin.yaml",
	} {
		if value = strings.TrimSpace(value); value != "" {
			return jetstream.ExpandCredentialPath(value)
		}
	}
	return ""
}

func runStorageViewLifecycle(ctx context.Context, packageRoot, action, deliverPolicy string, lookback time.Duration, stderr io.Writer) error {
	return runStorageComponentLifecycle(ctx, packageRoot, action, "storage-view", lookback, stderr)
}

func runStorageComponentLifecycle(ctx context.Context, packageRoot, action, serviceName string, lookback time.Duration, stderr io.Writer) error {
	return runStorageComponentLifecycleWithOptions(ctx, packageRoot, action, serviceName, lookback, false, stderr)
}

func runStorageComponentLifecycleWithReplay(ctx context.Context, packageRoot, action, serviceName string, lookback time.Duration, stderr io.Writer) error {
	return runStorageComponentLifecycleWithOptions(ctx, packageRoot, action, serviceName, lookback, true, stderr)
}

func runStorageComponentLifecycleWithOptions(ctx context.Context, packageRoot, action, serviceName string, lookback time.Duration, replayPending bool, stderr io.Writer) error {
	packageRoot = strings.TrimSpace(packageRoot)
	if packageRoot == "" {
		return errors.New("storage package root is empty; pass --package-root")
	}
	script := filepath.Join(packageRoot, action+".sh")
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return fmt.Errorf("storage package %s is unavailable", packageRoot)
	}
	cmd := exec.CommandContext(ctx, script, serviceName)
	cmd.Dir = packageRoot
	cmd.Stderr = stderr
	cmd.Stdout = stderr
	env := append([]string(nil), os.Environ()...)
	// Rebuild lookback is owned by storage-view/config/maintenance.json. The
	// lifecycle helper keeps the argument for readiness validation but never
	// mutates the service's policy through an ad-hoc environment override.
	_ = lookback
	if replayPending {
		env = append(env, "MOOX_STORAGE_VIEW_REPLAY_PENDING=1")
	}
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
