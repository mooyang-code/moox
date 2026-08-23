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
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/nats-io/nats.go"
	_ "modernc.org/sqlite"
)

type resetViewConsumersOptions struct {
	storageConf, packageRoot, stream, credentialFile, eventBusURL  string
	timeout, lookback                                              time.Duration
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
	Lookback           string   `json:"rebuild_lookback,omitempty"`
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
	// Keep destructive resets aligned with the bounded View rebuild window.
	// Operators can still opt into a longer, explicit lookback when they have
	// a deliberate historical-rebuild use case.
	opts := resetViewConsumersOptions{stream: events.StorageViewConsumerStream, lookback: 24 * time.Hour, timeout: 5 * time.Minute, restart: true}
	fs.StringVar(&opts.storageConf, "storage-conf", defaultRepairStorageConfigPath(), "storage.yaml path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "storage package root containing start.sh/stop.sh")
	fs.StringVar(&opts.stream, "stream", opts.stream, "JetStream stream to purge")
	fs.StringVar(&opts.credentialFile, "credential-file", "", "NATS admin credential YAML")
	fs.StringVar(&opts.eventBusURL, "eventbus-url", "", "NATS URL override")
	fs.DurationVar(&opts.lookback, "lookback", opts.lookback, "minimum history every rebuilt View must cover")
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
	if opts.lookback <= 0 || opts.timeout <= 0 {
		return errors.New("--lookback and --timeout must be positive")
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
		Stream: opts.stream, Consumers: append([]string{
			events.StorageViewLegacyBroadConsumer,
			events.StorageViewLegacyConsumer,
			events.StorageViewLegacyOtherConsumer,
			events.StorageViewLegacyKlineConsumer,
			events.StorageViewLegacyFactorConsumer,
			events.StorageViewLegacyMetricsConsumer,
			events.StorageViewLegacyMiscConsumer,
		}, events.StorageViewConsumerDurables...),
		Views: len(views), DryRun: opts.dryRun, Lookback: opts.lookback.String(),
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
	if opts.dryRun {
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: "reset-view-consumers", Status: "dry_run", Summary: summary})
	}
	// Complete all non-destructive EventBus checks before touching local
	// metadata or index files. A missing credential, invalid URL, unavailable
	// stream, or failed NATS connection must remain fully recoverable.
	if err := preflightResetEventBus(ctx, opts, storage.View, views); err != nil {
		return fmt.Errorf("preflight EventBus reset: %w", err)
	}
	// Metadata and Primary/DataNode share the same deployment transaction as
	// View. Stop the whole Storage role before touching SQLite/Pebble so the
	// backup and destructive reset cannot race an RPC writer or an open Pebble
	// handle. This also serializes with the storage watchdog via the lifecycle
	// maintenance lock acquired below.
	lifecycleService := "storage"
	if err := runStorageComponentLifecycle(ctx, packageRoot, "stop", lifecycleService, opts.lookback, stderr); err != nil {
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
			_ = runStorageComponentLifecycle(context.Background(), packageRoot, "start", lifecycleService, opts.lookback, stderr)
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
	mutationStarted, err := resetStorageViewConsumers(ctx, opts, storage.View, views)
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
		if err := runStorageComponentLifecycle(ctx, packageRoot, "start", lifecycleService, opts.lookback, stderr); err != nil {
			return fmt.Errorf("start storage-view: %w", err)
		}
		started = true
		readyLookback := opts.lookback
		if opts.resetAllStorageData {
			// A full Primary reset recreates metadata from an empty store; there
			// is no pre-existing View coverage to validate yet.
			readyLookback = 0
		}
		if err := waitResetViewReady(ctx, dbPath, readyLookback, packageRoot); err != nil {
			return fmt.Errorf("wait storage-view lookback ready: %w", err)
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

func waitResetViewReady(ctx context.Context, dbPath string, lookback time.Duration, packageRoot string) error {
	url := strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_HEALTH_URL"))
	if url == "" {
		url = "http://127.0.0.1:20211/readyz"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if header, authErr := resetHealthAuthHeader(req.URL, packageRoot); authErr == nil && header != "" {
			req.Header.Set("X-Moox-Health-Auth", header)
		} else if authErr != nil {
			return fmt.Errorf("build health authentication: %w", authErr)
		}
		resp, requestErr := client.Do(req)
		if requestErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
				if lookback <= 0 {
					return nil
				}
				ready, err := resetViewsLookbackReady(ctx, dbPath, lookback)
				if err == nil && ready {
					return nil
				}
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

func resetViewsLookbackReady(ctx context.Context, dbPath string, lookback time.Duration) (bool, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return false, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT c_engine, c_active_index_id, c_indexed_from FROM t_views WHERE c_status = 'active' OR c_status = ''`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	cutoff := time.Now().UTC().Add(-lookback).Truncate(time.Minute)
	for rows.Next() {
		var engine, activeID, indexedFrom string
		if err := rows.Scan(&engine, &activeID, &indexedFrom); err != nil {
			return false, err
		}
		if strings.TrimSpace(activeID) == "" {
			return false, nil
		}
		if strings.EqualFold(strings.TrimSpace(engine), "bleve") || lookback <= 0 {
			continue
		}
		from, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(indexedFrom))
		if err != nil || from.After(cutoff) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	// A deployment may contain only disabled/archived Views. They do not need
	// an active index before the reset command can complete; returning true
	// here avoids waiting forever for a View that is intentionally not running.
	return true, nil
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
	rows, err := db.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_engine, c_active_index_id, c_desired_view_revision, c_primary_dataset_id, c_dataset_ids_json FROM t_views`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []resetViewRecord
	for rows.Next() {
		var record resetViewRecord
		var datasetIDs string
		if err := rows.Scan(&record.SpaceID, &record.ViewID, &record.Engine, &record.ActiveIndexID, &record.DesiredRevision, &record.PrimaryDatasetID, &datasetIDs); err != nil {
			return nil, err
		}
		if strings.TrimSpace(datasetIDs) != "" {
			if err := json.Unmarshal([]byte(datasetIDs), &record.DatasetIDs); err != nil {
				return nil, fmt.Errorf("decode View datasets %s/%s: %w", record.SpaceID, record.ViewID, err)
			}
		}
		if len(record.DatasetIDs) == 0 && record.PrimaryDatasetID != "" {
			record.DatasetIDs = []string{record.PrimaryDatasetID}
		}
		if record.Engine == "" {
			record.Engine = "duckdb"
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func preflightResetEventBus(ctx context.Context, opts resetViewConsumersOptions, viewConfig storageconfig.StorageView, views []resetViewRecord) error {
	credentialPath := resolveRepairCredentialFile(opts.credentialFile)
	if credentialPath == "" {
		return errors.New("NATS admin credential is required; pass --credential-file or MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE")
	}
	cred, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return err
	}
	urls := cred.URLs
	if strings.TrimSpace(opts.eventBusURL) != "" {
		urls = []string{strings.TrimSpace(opts.eventBusURL)}
	}
	if err := validatePurgeEventBusURLs(urls, cred.CAFile); err != nil {
		return err
	}
	natsOpts := []nats.Option{nats.Name("moox-storage-cli-reset-view-consumers"), nats.UserInfo(cred.Username, cred.Password), nats.Timeout(15 * time.Second)}
	if cred.CAFile != "" {
		caPath := jetstream.ExpandCredentialPath(cred.CAFile)
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		if err := appendNATSTLSOptions(&natsOpts, caPath); err != nil {
			return err
		}
	}
	nc, err := nats.Connect(strings.Join(urls, ","), natsOpts...)
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	if _, err := js.StreamInfo(opts.stream, nats.Context(ctx)); err != nil {
		return fmt.Errorf("inspect stream %s: %w", opts.stream, err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	if _, err := resetEventSubjects(registry, viewConfig, views); err != nil {
		return err
	}
	return nil
}

func resetStorageViewConsumers(ctx context.Context, opts resetViewConsumersOptions, viewConfig storageconfig.StorageView, views []resetViewRecord) (mutationStarted bool, err error) {
	credentialPath := resolveRepairCredentialFile(opts.credentialFile)
	if credentialPath == "" {
		return false, errors.New("NATS admin credential is required; pass --credential-file or MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE")
	}
	cred, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return false, err
	}
	urls := cred.URLs
	if strings.TrimSpace(opts.eventBusURL) != "" {
		urls = []string{strings.TrimSpace(opts.eventBusURL)}
	}
	if err := validatePurgeEventBusURLs(urls, cred.CAFile); err != nil {
		return false, err
	}
	natsOpts := []nats.Option{nats.Name("moox-storage-cli-reset-view-consumers"), nats.UserInfo(cred.Username, cred.Password), nats.Timeout(15 * time.Second)}
	if cred.CAFile != "" {
		caPath := jetstream.ExpandCredentialPath(cred.CAFile)
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		if err := appendNATSTLSOptions(&natsOpts, caPath); err != nil {
			return false, err
		}
	}
	nc, err := nats.Connect(strings.Join(urls, ","), natsOpts...)
	if err != nil {
		return false, err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return false, err
	}
	for _, durable := range []string{
		events.StorageViewLegacyBroadConsumer,
		events.StorageViewLegacyConsumer,
		events.StorageViewLegacyOtherConsumer,
		events.StorageViewLegacyKlineConsumer,
		events.StorageViewLegacyFactorConsumer,
		events.StorageViewLegacyMetricsConsumer,
		events.StorageViewLegacyMiscConsumer,
		events.StorageViewKlineConsumer,
		events.StorageViewFactorConsumer,
		events.StorageViewMetricsConsumer,
		events.StorageViewMiscConsumer,
	} {
		mutationStarted = true
		if err := js.DeleteConsumer(opts.stream, durable, nats.Context(ctx)); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
			return mutationStarted, fmt.Errorf("delete consumer %s: %w", durable, err)
		}
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return mutationStarted, err
	}
	ordered, err := resetEventSubjects(registry, viewConfig, views)
	if err != nil {
		return mutationStarted, err
	}
	for _, subject := range ordered {
		mutationStarted = true
		if err := js.PurgeStream(opts.stream, &nats.StreamPurgeRequest{Subject: subject}, nats.Context(ctx)); err != nil {
			return mutationStarted, fmt.Errorf("purge stream %s subject %s: %w", opts.stream, subject, err)
		}
	}
	return mutationStarted, nil
}

func resetEventSubjects(registry *events.Registry, viewConfig storageconfig.StorageView, views []resetViewRecord) ([]string, error) {
	// This command is intentionally destructive: all View histories, including
	// record/Bleve histories, are discarded and recreated from the post-reset
	// stream. There is no compatibility-preservation exception.
	eventsToPurge := []events.Event{events.DatasetRowsUpserted, events.DatasetPeriodCollected, events.FactorPeriodComputed, events.DatasetSyncPoint}
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
				for _, event := range eventsToPurge {
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
