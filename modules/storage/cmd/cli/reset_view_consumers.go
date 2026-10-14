package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/requestauth"
	_ "modernc.org/sqlite"
)

type resetViewConsumersOptions struct {
	storageConf, packageRoot, stream                               string
	timeout                                                        time.Duration
	yes, dryRun, resetAllStorageData, restart, maintenanceLockHeld bool
}

type resetViewConsumersSummary struct {
	Stream             string   `json:"stream"`
	Consumers          []string `json:"consumers"`
	Views              int      `json:"views"`
	RecordViews        int      `json:"record_views,omitempty"`
	Indexes            []string `json:"indexes,omitempty"`
	PrimaryDataRemoved bool     `json:"primary_data_removed"`
	QueuePurged        bool     `json:"queue_purged"`
	ViewMetadataReset  bool     `json:"view_metadata_reset"`
	DryRun             bool     `json:"dry_run"`
}

type resetViewRecord struct {
	SpaceID, ViewID, Engine, ActiveIndexID, PrimaryDatasetID string
	DesiredRevision                                          uint64
	DatasetIDs                                               []string
}

type resetIndexMove struct {
	original string
	staged   string
}

func runResetViewConsumers(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reset-view-consumers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream, timeout: 5 * time.Minute, restart: true}
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream to purge")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "overall operation timeout")
	fs.BoolVar(&opts.yes, "yes", false, "confirm permanent consumer, queue and View index deletion")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "inspect the operation without mutating state")
	fs.BoolVar(&opts.resetAllStorageData, "reset-all-storage-data", false, "also delete Primary Pebble/outbox data")
	fs.BoolVar(&opts.restart, "restart", opts.restart, "restart storage-view after reset")
	// The storage installer already owns the package maintenance lock. This
	// internal flag avoids a child process waiting on its parent's flock.
	fs.BoolVar(&opts.maintenanceLockHeld, "maintenance-lock-held", false, "internal: caller already holds the package maintenance lock")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected reset-view-consumers arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateResetViewConsumersOptions(opts); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	return resetViewConsumers(ctx, opts, stdout, stderr)
}

func validateResetViewConsumersOptions(opts resetViewConsumersOptions) error {
	if strings.TrimSpace(opts.stream) != events.StorageViewConsumerStream {
		return fmt.Errorf("--stream must be %s for destructive reset", events.StorageViewConsumerStream)
	}
	if opts.timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if !opts.dryRun && !opts.yes {
		return errors.New("reset-view-consumers permanently deletes View indexes and the EventBus stream; re-run with --yes, or use --dry-run")
	}
	return nil
}

func resetViewConsumers(ctx context.Context, opts resetViewConsumersOptions, stdout, stderr io.Writer) (retErr error) {
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	if err := storage.View.ValidateConsumerPartitions(nil); err != nil {
		return fmt.Errorf("refusing destructive reset with invalid consumer topology: %w", err)
	}
	packageRoot := resolveRepairPackageRoot(opts.packageRoot, opts.storageConf)
	if !opts.maintenanceLockHeld {
		unlock, err := acquireResetMaintenanceLock(ctx, packageRoot)
		if err != nil {
			return err
		}
		defer unlock()
	}
	storage = resolveRepairStoragePaths(storage, packageRoot, opts.storageConf)
	dbPath := metadataDBPath(storage)
	views, err := listResetViewRecords(ctx, dbPath)
	if err != nil {
		return err
	}
	summary := resetViewConsumersSummary{
		Stream: opts.stream, Consumers: append([]string(nil), events.StorageViewConsumerDurables...),
		Views: len(views), DryRun: opts.dryRun,
	}
	for _, view := range views {
		if strings.EqualFold(strings.TrimSpace(view.Engine), "bleve") {
			summary.RecordViews++
			continue
		}
		for _, slot := range []string{"a", "b"} {
			id := viewindex.ViewIndexID(view.SpaceID, view.ViewID, viewindex.Slot(slot))
			summary.Indexes = append(summary.Indexes, id)
		}
	}
	// EventBus 的删除与清除经网关交给 Admin 执行（管理员令牌只在 Admin 的数据库里）。
	// 在动手前就打开通道：dry-run 与真正执行用同一条路径做预检。
	admin, err := openEventBusAdmin(opts.storageConf, packageRoot)
	if err != nil {
		return err
	}
	defer admin.Close()
	if opts.dryRun {
		// Dry-run is the normal preflight for the higher-level rebuild workflow.
		// Validate the gateway path to Admin, the stream and the purge subjects
		// before reporting an executable plan so a later destructive run cannot
		// fail only after writers have been quiesced.
		if err := preflightResetEventBus(ctx, admin, opts, storage.View, views); err != nil {
			return fmt.Errorf("preflight EventBus reset: %w", err)
		}
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: "reset-view-consumers", Status: "dry_run", Summary: summary})
	}
	// Complete all non-destructive EventBus checks before touching local
	// metadata or index files. An unreachable Admin, an unavailable stream or a
	// failed broker connection must remain fully recoverable.
	if err := preflightResetEventBus(ctx, admin, opts, storage.View, views); err != nil {
		return fmt.Errorf("preflight EventBus reset: %w", err)
	}
	// Metadata and Primary/DataNode share the same deployment transaction as
	// View. Stop the whole Storage role before touching SQLite/Pebble so the
	// backup and destructive reset cannot race an RPC writer or an open Pebble
	// handle. This also serializes with the storage watchdog via the lifecycle
	// maintenance lock acquired below.
	lifecycleService := "storage"
	if err := runStorageComponentLifecycle(ctx, packageRoot, "stop", lifecycleService, stderr); err != nil {
		return fmt.Errorf("stop storage-view: %w", err)
	}
	stopped := true
	started := false
	resetCommitted := false
	backupPath := ""
	recoveryOK := true
	defer func() {
		if stopped && !started && !resetCommitted && backupPath != "" {
			if restoreErr := restoreRepairDB(backupPath, dbPath); restoreErr != nil {
				recoveryOK = false
				retErr = fmt.Errorf("%w; storage metadata recovery failed: %v", retErr, restoreErr)
			}
		}
		if stopped && !started && !resetCommitted && recoveryOK {
			_ = runStorageComponentLifecycle(context.Background(), packageRoot, "start", lifecycleService, stderr)
		}
	}()
	backupPath, err = backupRepairDB(dbPath)
	if err != nil {
		return fmt.Errorf("backup storage metadata: %w", err)
	}
	stagingDir, stagedIndexes, err := stageResetViewIndexes(storage.Devices.ViewIndexRoot, views)
	if err != nil {
		return fmt.Errorf("stage View indexes for reset recovery: %w", err)
	}
	defer func() {
		if resetCommitted {
			_ = os.RemoveAll(stagingDir)
			return
		}
		if restoreErr := restoreResetViewIndexes(stagedIndexes); restoreErr != nil {
			recoveryOK = false
			retErr = fmt.Errorf("%w; View index recovery failed: %v", retErr, restoreErr)
		}
	}()
	for _, view := range views {
		if err := resetRepairViewMetadata(ctx, dbPath, view.SpaceID, view.ViewID, nextViewRevision(view.DesiredRevision)); err != nil {
			return fmt.Errorf("reset View metadata %s/%s: %w", view.SpaceID, view.ViewID, err)
		}
		if _, err := purgeRepairViewIndexes(storage.Devices.ViewIndexRoot, view.Engine,
			viewindex.ViewIndexID(view.SpaceID, view.ViewID, viewindex.SlotA),
			viewindex.ViewIndexID(view.SpaceID, view.ViewID, viewindex.SlotB)); err != nil {
			return fmt.Errorf("delete View indexes %s/%s: %w", view.SpaceID, view.ViewID, err)
		}
	}
	summary.ViewMetadataReset = true
	// Purge the EventBus only after local metadata and physical files have been
	// staged/reset successfully.
	mutationStarted, err := resetStorageViewConsumers(ctx, admin, opts, storage.View, views)
	if err != nil {
		// Once a broker mutation may have happened, the old local incarnation
		// cannot be restored safely because its pending events are gone. Keep the
		// reset state and leave Storage stopped so the operator can retry.
		if mutationStarted {
			resetCommitted = true
		}
		return err
	}
	// From this point both local state and EventBus state are committed.
	resetCommitted = true
	summary.QueuePurged = true
	if opts.resetAllStorageData {
		paths := []string{storage.Devices.PebblePath, filepath.Join(packageRoot, "data", "storage-node", "pebble")}
		seen := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			path, err = validateResetPrimaryPath(path, packageRoot)
			if err != nil {
				return err
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("delete Primary data %s: %w", path, err)
			}
		}
		summary.PrimaryDataRemoved = true
	}
	if opts.restart {
		if err := runStorageComponentLifecycle(ctx, packageRoot, "start", lifecycleService, stderr); err != nil {
			return fmt.Errorf("start storage-view: %w", err)
		}
		started = true
		// A full Primary reset recreates metadata from an empty store; there
		// are no Views to wait for yet.
		if err := waitResetViewReady(ctx, dbPath, !opts.resetAllStorageData, packageRoot); err != nil {
			return fmt.Errorf("wait storage-view ready: %w", err)
		}
	}
	stopped = false
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "reset-view-consumers", Status: "ok", Summary: summary})
}

func resetViewIndexPaths(root, engine, id string) []string {
	if id == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "duckdb":
		return []string{filepath.Join(root, "duckdb", id+".duckdb"), filepath.Join(root, "duckdb", id+".duckdb.wal")}
	case "bleve":
		return []string{filepath.Join(root, "bleve", id)}
	default:
		return nil
	}
}

// stageResetViewIndexes moves both A/B slots out of the live inventory before
// any metadata or stream mutation. A failed reset can therefore put the
// physical files back before the old package is restarted.
func stageResetViewIndexes(root string, views []resetViewRecord) (string, []resetIndexMove, error) {
	if strings.TrimSpace(root) == "" {
		return "", nil, errors.New("View index root is required")
	}
	stagingDir := filepath.Join(root, ".reset-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", nil, err
	}
	var moves []resetIndexMove
	restore := func() {
		_ = restoreResetViewIndexes(moves)
		_ = os.RemoveAll(stagingDir)
	}
	for _, view := range views {
		ids := []string{
			viewindex.ViewIndexID(view.SpaceID, view.ViewID, viewindex.SlotA),
			viewindex.ViewIndexID(view.SpaceID, view.ViewID, viewindex.SlotB),
		}
		for _, original := range resetViewIndexPaths(root, view.Engine, ids[0]) {
			if _, err := os.Stat(original); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				restore()
				return "", nil, err
			}
			rel, err := filepath.Rel(root, original)
			if err != nil {
				restore()
				return "", nil, err
			}
			staged := filepath.Join(stagingDir, rel)
			if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
				restore()
				return "", nil, err
			}
			if err := os.Rename(original, staged); err != nil {
				restore()
				return "", nil, err
			}
			moves = append(moves, resetIndexMove{original: original, staged: staged})
		}
		for _, original := range resetViewIndexPaths(root, view.Engine, ids[1]) {
			if _, err := os.Stat(original); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				restore()
				return "", nil, err
			}
			rel, err := filepath.Rel(root, original)
			if err != nil {
				restore()
				return "", nil, err
			}
			staged := filepath.Join(stagingDir, rel)
			if err := os.MkdirAll(filepath.Dir(staged), 0o700); err != nil {
				restore()
				return "", nil, err
			}
			if err := os.Rename(original, staged); err != nil {
				restore()
				return "", nil, err
			}
			moves = append(moves, resetIndexMove{original: original, staged: staged})
		}
	}
	return stagingDir, moves, nil
}

func restoreResetViewIndexes(moves []resetIndexMove) error {
	var firstErr error
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		if _, err := os.Stat(move.staged); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(move.original), 0o700); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := os.Stat(move.original); err == nil {
			_ = os.RemoveAll(move.original)
		}
		if err := os.Rename(move.staged, move.original); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func acquireResetMaintenanceLock(ctx context.Context, packageRoot string) (func(), error) {
	path := strings.TrimSpace(packageRoot) + ".maintenance.lock"
	if strings.TrimSpace(packageRoot) == "" {
		return nil, errors.New("storage package root is empty")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage maintenance lock: %w", err)
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire storage maintenance lock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("wait for storage maintenance lock: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func validateResetPrimaryPath(path, packageRoot string) (string, error) {
	cleanPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve Primary path %q: %w", path, err)
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(packageRoot))
	if err != nil {
		return "", fmt.Errorf("resolve package root %q: %w", packageRoot, err)
	}
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to delete Primary path outside package root: %s", cleanPath)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 || parts[0] != "data" || cleanPath == cleanRoot {
		return "", fmt.Errorf("refusing to delete unsafe Primary path %s; expected a child of %s/data", cleanPath, cleanRoot)
	}
	return cleanPath, nil
}

// waitResetViewReady waits for storage-view to be ready and, when
// requireActive is set, for every active View to have an active index.
// nativeStorageViewReady 用原生部署的 healthcheck.sh 判断 storage-view 是否就绪：健康检查需要认证，凭据由运行助手持有，
// 自己拼 HTTP 请求在原生部署里只会一直得到 401。脚本不存在（非原生布局）时 available 为 false。
func nativeStorageViewReady(ctx context.Context, packageRoot string) (ready, available bool) {
	script := filepath.Join(strings.TrimSpace(packageRoot), "healthcheck.sh")
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return false, false
	}
	// 就绪与否看输出里的 ready 字段；未就绪时脚本可能以非零退出，不能把退出码当成“不可用”。
	output, _ := exec.CommandContext(ctx, script, "--components", "storage-view").Output()
	var status struct {
		Components []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"components"`
	}
	if json.Unmarshal(output, &status) != nil {
		return false, true
	}
	for _, component := range status.Components {
		if component.ID == "storage-view" {
			return component.Ready, true
		}
	}
	return false, true
}

func waitResetViewReady(ctx context.Context, dbPath string, requireActive bool, packageRoot string) error {
	url := strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_HEALTH_URL"))
	if url == "" {
		url = "http://127.0.0.1:20211/readyz"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		healthy := false
		if native, available := nativeStorageViewReady(ctx, packageRoot); available {
			healthy = native
		} else {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			if header, authErr := resetHealthAuthHeader(req.URL, packageRoot); authErr == nil && header != "" {
				req.Header.Set("X-Moox-Health-Auth", header)
			} else if authErr != nil {
				return fmt.Errorf("build health authentication: %w", authErr)
			}
			if resp, requestErr := client.Do(req); requestErr == nil {
				_ = resp.Body.Close()
				healthy = resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices
			}
		}
		if healthy {
			if !requireActive {
				return nil
			}
			ready, err := resetViewsActive(ctx, dbPath)
			if err == nil && ready {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func resetHealthAuthHeader(target *url.URL, packageRoot string) (string, error) {
	if target == nil {
		return "", errors.New("health URL is nil")
	}
	values := map[string]string{
		"MOOX_HEALTH_AUTH_VERSION":    strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION")),
		"MOOX_HEALTH_AUTH_ACCESS_KEY": strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY")),
		"MOOX_HEALTH_AUTH_SECRET_KEY": strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")),
	}
	if values["MOOX_HEALTH_AUTH_VERSION"] == "" {
		values["MOOX_HEALTH_AUTH_VERSION"] = "moox-health-v1"
	}
	if values["MOOX_HEALTH_AUTH_ACCESS_KEY"] == "" || values["MOOX_HEALTH_AUTH_SECRET_KEY"] == "" {
		path := filepath.Join(strings.TrimSpace(packageRoot), "secrets", "health-auth.env")
		if raw, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
				if ok {
					if _, exists := values[name]; exists && values[name] == "" {
						values[name] = strings.TrimSpace(value)
					}
				}
			}
		}
	}
	if values["MOOX_HEALTH_AUTH_ACCESS_KEY"] == "" || values["MOOX_HEALTH_AUTH_SECRET_KEY"] == "" {
		// Local installations may intentionally disable health authentication.
		return "", nil
	}
	nonce, err := requestauth.NewNonce()
	if err != nil {
		return "", err
	}
	timestamp := time.Now().Unix()
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	signature, err := requestauth.Sign(values["MOOX_HEALTH_AUTH_SECRET_KEY"], requestauth.Material{Method: http.MethodGet, Path: path, Timestamp: timestamp, Nonce: nonce})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%d/%s/%s", values["MOOX_HEALTH_AUTH_VERSION"], values["MOOX_HEALTH_AUTH_ACCESS_KEY"], timestamp, nonce, signature), nil
}

// resetViewsActive reports whether every active View has an active index.
func resetViewsActive(ctx context.Context, dbPath string) (bool, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()
	var pending int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM t_views WHERE (c_status = 'active' OR c_status = '') AND c_active_index_id = ''`).Scan(&pending); err != nil {
		return false, err
	}
	// A deployment may contain only disabled/archived Views. They do not need
	// an active index before the reset command can complete.
	return pending == 0, nil
}

func listResetViewRecords(ctx context.Context, dbPath string) ([]resetViewRecord, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// A destructive reset must also clear disabled/building/archived rows. They
	// may still own physical A/B files or an unfinished build even though they
	// are not currently visible in the active View list.
	rows, err := db.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_engine, c_active_index_id, c_desired_view_revision, c_dataset_id FROM t_views`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []resetViewRecord
	for rows.Next() {
		var record resetViewRecord
		if err := rows.Scan(&record.SpaceID, &record.ViewID, &record.Engine, &record.ActiveIndexID, &record.DesiredRevision, &record.PrimaryDatasetID); err != nil {
			return nil, err
		}
		if record.PrimaryDatasetID != "" {
			record.DatasetIDs = []string{record.PrimaryDatasetID}
		}
		if record.Engine == "" {
			record.Engine = "duckdb"
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// preflightResetEventBus 在不改动任何状态的前提下确认：经网关能到 Admin、Admin 能连上 EventBus、流存在，并且清除主题能渲染出来。
// preflightResetEventBus 在不改动任何状态的前提下确认：经网关能到 Admin、Admin 能连上 EventBus、流存在，
// 并且要清除的主题都能渲染、都在 Admin 会接受的范围内（否则会在本地状态已经重置之后才被拒绝）。
func preflightResetEventBus(ctx context.Context, admin eventBusAdmin, opts resetViewConsumersOptions, viewConfig storageconfig.StorageView, views []resetViewRecord) error {
	if _, err := admin.Inspect(ctx, opts.stream, events.StorageViewConsumerDurables); err != nil {
		return fmt.Errorf("inspect stream %s: %w", opts.stream, err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	subjects, err := resetEventSubjects(registry, viewConfig, views)
	if err != nil {
		return err
	}
	for _, subject := range subjects {
		if !events.IsStorageDatasetSubject(subject) {
			return fmt.Errorf("主题 %q 不在 Admin 允许清除的范围内", subject)
		}
	}
	return nil
}

// resetStorageViewConsumers 删除 View 消费者并清除数据集事件。mutationStarted 表示 EventBus 是否可能已被改动：
// 为 false 时失败发生在任何改动之前（Admin 连不上 EventBus 等），本地状态还能安全恢复；为 true 时旧事件可能已经丢失，
// 旧的本地状态不能再恢复，调用方要保留已重置的状态并让操作员重试。
func resetStorageViewConsumers(ctx context.Context, admin eventBusAdmin, opts resetViewConsumersOptions, viewConfig storageconfig.StorageView, views []resetViewRecord) (mutationStarted bool, err error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return false, err
	}
	ordered, err := resetEventSubjects(registry, viewConfig, views)
	if err != nil {
		return false, err
	}
	_, attempted, err := admin.DeleteConsumers(ctx, opts.stream, events.StorageViewConsumerDurables)
	mutationStarted = attempted
	if err != nil {
		return mutationStarted, fmt.Errorf("delete consumers: %w", err)
	}
	attempted, err = purgeSubjects(ctx, admin, opts.stream, ordered)
	mutationStarted = mutationStarted || attempted
	if err != nil {
		return mutationStarted, fmt.Errorf("purge stream %s: %w", opts.stream, err)
	}
	return mutationStarted, nil
}

func resetEventSubjects(registry *events.Registry, viewConfig storageconfig.StorageView, views []resetViewRecord) ([]string, error) {
	// This command is intentionally destructive: all View histories, including
	// record/Bleve histories, are discarded and recreated from the post-reset
	// stream. There is no compatibility-preservation exception.
	subjects := make(map[string]struct{})
	for _, partition := range viewConfig.ConsumerPartitions {
		for _, dataset := range partition.Datasets() {
			datasetIDs := []string{dataset.DatasetID}
			if dataset.DatasetID == "*" {
				datasetIDs = datasetIDsForWildcardRoute(dataset.SpaceID, views)
				if len(datasetIDs) == 0 {
					return nil, fmt.Errorf("cannot expand wildcard purge route for space %s: no View dataset is known", dataset.SpaceID)
				}
			}
			for _, datasetID := range datasetIDs {
				for _, event := range events.StorageDatasetEvents {
					subject, renderErr := registry.RenderSubject(event, dataset.SpaceID, datasetID)
					if renderErr != nil {
						return nil, fmt.Errorf("render purge subject %s/%s: %w", dataset.SpaceID, datasetID, renderErr)
					}
					subjects[subject] = struct{}{}
				}
			}
		}
	}
	ordered := make([]string, 0, len(subjects))
	for subject := range subjects {
		ordered = append(ordered, subject)
	}
	slices.Sort(ordered)
	return ordered, nil
}

func datasetIDsForWildcardRoute(spaceID string, views []resetViewRecord) []string {
	seen := make(map[string]struct{})
	for _, view := range views {
		if view.SpaceID != spaceID {
			continue
		}
		ids := view.DatasetIDs
		if len(ids) == 0 && view.PrimaryDatasetID != "" {
			ids = []string{view.PrimaryDatasetID}
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && id != "*" {
				seen[id] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	slices.Sort(result)
	return result
}
