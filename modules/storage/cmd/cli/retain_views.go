package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

const requiredRetainedViewCount = 8

type retainViewsOptions struct {
	metadataDB  string
	packageRoot string
	keepViews   []string
	yes         bool
}

type retainedIndex struct {
	Engine  string `json:"engine"`
	IndexID string `json:"index_id"`
}

type retainViewsSummary struct {
	KeptViews      int             `json:"kept_views"`
	DeletedViews   int             `json:"deleted_views"`
	RetiredIndexes []retainedIndex `json:"retired_indexes,omitempty"`
}

func runRetainViews(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("retain-views", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := retainViewsOptions{}
	fs.StringVar(&opts.metadataDB, "metadata-db", "", "Metadata SQLite database path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "Storage package root; the View process must be stopped")
	fs.Var((*stringListValue)(&opts.keepViews), "keep-view", "View to retain, in space_id/view_id form (repeat exactly eight times)")
	fs.BoolVar(&opts.yes, "yes", false, "confirm permanent metadata deletion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected retain-views arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateRetainViewsOptions(opts); err != nil {
		return err
	}
	if err := ensureStorageViewStopped(opts.packageRoot); err != nil {
		return err
	}
	summary, err := retainViews(context.Background(), opts)
	if err != nil {
		return err
	}
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "retain-views", Status: "ready_for_physical_cleanup", Summary: summary})
}

type stringListValue []string

func (v *stringListValue) String() string { return strings.Join(*v, ",") }
func (v *stringListValue) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--keep-view cannot be empty")
	}
	*v = append(*v, value)
	return nil
}

func validateRetainViewsOptions(opts retainViewsOptions) error {
	if !opts.yes {
		return errors.New("retain-views permanently deletes View metadata; re-run with --yes")
	}
	if strings.TrimSpace(opts.metadataDB) == "" {
		return errors.New("--metadata-db is required")
	}
	if strings.TrimSpace(opts.packageRoot) == "" {
		return errors.New("--package-root is required so retain-views can verify storage-view is stopped")
	}
	if len(opts.keepViews) != requiredRetainedViewCount {
		return fmt.Errorf("exactly %d --keep-view values are required", requiredRetainedViewCount)
	}
	seen := make(map[string]struct{}, len(opts.keepViews))
	for _, value := range opts.keepViews {
		value = strings.TrimSpace(value)
		spaceID, viewID, ok := strings.Cut(value, "/")
		if !ok || strings.TrimSpace(spaceID) == "" || strings.TrimSpace(viewID) == "" || strings.Contains(viewID, "/") {
			return fmt.Errorf("invalid --keep-view %q; expected space_id/view_id", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate --keep-view %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func ensureStorageViewStopped(packageRoot string) error {
	packageRoot = strings.TrimSpace(packageRoot)
	statusPath := filepath.Join(packageRoot, "status.sh")
	if _, err := exec.LookPath(statusPath); err != nil {
		return fmt.Errorf("storage status script is unavailable: %w", err)
	}
	// status.sh is generated as a Bash script (it uses arrays and pipefail),
	// so invoke its shebang rather than forcing the POSIX sh interpreter.
	output, err := exec.Command(statusPath, "storage-view").CombinedOutput()
	if err != nil {
		return fmt.Errorf("check storage-view status: %w", err)
	}
	if strings.Contains(string(output), "storage-view: running") {
		return errors.New("storage-view must be stopped before retain-views")
	}
	if !strings.Contains(string(output), "storage-view: stopped") {
		return errors.New("storage-view status is indeterminate; refusing retain-views")
	}
	return nil
}

func retainViews(ctx context.Context, opts retainViewsOptions) (retainViewsSummary, error) {
	db, err := sql.Open("sqlite", opts.metadataDB)
	if err != nil {
		return retainViewsSummary{}, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return retainViewsSummary{}, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return retainViewsSummary{}, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return retainViewsSummary{}, fmt.Errorf("begin metadata transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	keep := make(map[string]struct{}, len(opts.keepViews))
	for _, value := range opts.keepViews {
		keep[strings.TrimSpace(value)] = struct{}{}
	}
	rows, err := conn.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_engine, c_status, c_active_index_id FROM t_views`)
	if err != nil {
		return retainViewsSummary{}, err
	}
	type viewRow struct{ spaceID, viewID, engine, status, activeIndexID string }
	var all []viewRow
	for rows.Next() {
		var item viewRow
		if err := rows.Scan(&item.spaceID, &item.viewID, &item.engine, &item.status, &item.activeIndexID); err != nil {
			_ = rows.Close()
			return retainViewsSummary{}, err
		}
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return retainViewsSummary{}, err
	}
	if err := rows.Close(); err != nil {
		return retainViewsSummary{}, err
	}
	summary := retainViewsSummary{}
	for key := range keep {
		spaceID, viewID, _ := strings.Cut(key, "/")
		found := false
		for _, item := range all {
			if item.spaceID != spaceID || item.viewID != viewID {
				continue
			}
			found = true
			if item.status != "active" && item.status != "" {
				return retainViewsSummary{}, fmt.Errorf("kept View %s is not active", key)
			}
			break
		}
		if !found {
			return retainViewsSummary{}, fmt.Errorf("kept View %s does not exist", key)
		}
	}
	for _, item := range all {
		key := item.spaceID + "/" + item.viewID
		if _, ok := keep[key]; ok {
			summary.KeptViews++
			continue
		}
		var buildIndexID, buildEngine string
		_ = conn.QueryRowContext(ctx, `SELECT c_index_id, c_engine FROM t_view_index_builds WHERE c_space_id = ? AND c_view_id = ?`, item.spaceID, item.viewID).Scan(&buildIndexID, &buildEngine)
		if strings.TrimSpace(item.activeIndexID) != "" {
			summary.RetiredIndexes = append(summary.RetiredIndexes, retainedIndex{Engine: item.engine, IndexID: item.activeIndexID})
		}
		if strings.TrimSpace(buildIndexID) != "" {
			summary.RetiredIndexes = append(summary.RetiredIndexes, retainedIndex{Engine: buildEngine, IndexID: buildIndexID})
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, item.spaceID, item.viewID); err != nil {
			return retainViewsSummary{}, err
		}
		summary.DeletedViews++
	}
	sort.Slice(summary.RetiredIndexes, func(i, j int) bool { return summary.RetiredIndexes[i].IndexID < summary.RetiredIndexes[j].IndexID })
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return retainViewsSummary{}, err
	}
	committed = true
	return summary, nil
}
