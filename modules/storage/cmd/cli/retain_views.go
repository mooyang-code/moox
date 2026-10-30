package main

import (
	"context"
	"database/sql"
	"encoding/json"
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

const requiredRetainedViewCount = 7

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
	fs.Var((*stringListValue)(&opts.keepViews), "keep-view", "View to retain, in space_id/view_id form (repeat exactly seven times)")
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
	// status.sh 转给 moox-runtime：不接受位置参数，用 --components 选择组件，输出是 JSON。
	output, err := exec.Command(statusPath, "--components", "storage-view").Output()
	if err != nil {
		return fmt.Errorf("check storage-view status: %w", err)
	}
	state, err := storageViewState(output)
	if err != nil {
		return fmt.Errorf("storage-view status is indeterminate; refusing retain-views: %w", err)
	}
	switch state {
	case "stopped", "paused":
		// paused 是 moox-runtime pause 之后的状态：进程已停，主机守护也不会拉起。
		return nil
	case "running":
		return errors.New("storage-view must be stopped before retain-views")
	default:
		return fmt.Errorf("storage-view state is %q; refusing retain-views", state)
	}
}

// storageViewState 从 status.sh 的 JSON 输出里取出 storage-view 的状态。
func storageViewState(output []byte) (string, error) {
	var status struct {
		Components []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"components"`
	}
	if err := json.Unmarshal(output, &status); err != nil {
		return "", fmt.Errorf("decode status output: %w", err)
	}
	for _, component := range status.Components {
		if component.ID == "storage-view" {
			return component.State, nil
		}
	}
	return "", errors.New("status output has no storage-view component")
}

// retainViewRow 是 t_views 里和删除有关的几列。
type retainViewRow struct{ spaceID, viewID, engine, status, activeIndexID string }

// withMetadataTx 在 Metadata SQLite 的一个 BEGIN IMMEDIATE 事务里执行 fn：fn 返回 nil 就提交，否则回滚。
// 外键级联打开，删除 t_views 的行会连带删除它的列与周期状态。
func withMetadataTx(ctx context.Context, metadataDB string, fn func(conn *sql.Conn) error) error {
	db, err := sql.Open("sqlite", metadataDB)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin metadata transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func listViewRows(ctx context.Context, conn *sql.Conn) ([]retainViewRow, error) {
	rows, err := conn.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_engine, c_status, c_active_index_id FROM t_views`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []retainViewRow
	for rows.Next() {
		var item retainViewRow
		if err := rows.Scan(&item.spaceID, &item.viewID, &item.engine, &item.status, &item.activeIndexID); err != nil {
			return nil, err
		}
		all = append(all, item)
	}
	return all, rows.Err()
}

// deleteViewMetadata 删除一个 View 的元数据，并把它占用的活动索引记进 summary，
// 供之后由 Storage View 的清理定时器回收物理文件。
func deleteViewMetadata(ctx context.Context, conn *sql.Conn, item retainViewRow, summary *retainViewsSummary) error {
	if strings.TrimSpace(item.activeIndexID) != "" {
		summary.RetiredIndexes = append(summary.RetiredIndexes, retainedIndex{Engine: item.engine, IndexID: item.activeIndexID})
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, item.spaceID, item.viewID); err != nil {
		return err
	}
	summary.DeletedViews++
	return nil
}

func retainViews(ctx context.Context, opts retainViewsOptions) (retainViewsSummary, error) {
	summary := retainViewsSummary{}
	err := withMetadataTx(ctx, opts.metadataDB, func(conn *sql.Conn) error {
		keep := make(map[string]struct{}, len(opts.keepViews))
		for _, value := range opts.keepViews {
			keep[strings.TrimSpace(value)] = struct{}{}
		}
		all, err := listViewRows(ctx, conn)
		if err != nil {
			return err
		}
		for key := range keep {
			spaceID, viewID, _ := strings.Cut(key, "/")
			found := false
			for _, item := range all {
				if item.spaceID != spaceID || item.viewID != viewID {
					continue
				}
				found = true
				if item.status != "active" && item.status != "" {
					return fmt.Errorf("kept View %s is not active", key)
				}
				break
			}
			if !found {
				return fmt.Errorf("kept View %s does not exist", key)
			}
		}
		for _, item := range all {
			if _, ok := keep[item.spaceID+"/"+item.viewID]; ok {
				summary.KeptViews++
				continue
			}
			if err := deleteViewMetadata(ctx, conn, item, &summary); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return retainViewsSummary{}, err
	}
	sortRetiredIndexes(&summary)
	return summary, nil
}

func sortRetiredIndexes(summary *retainViewsSummary) {
	sort.Slice(summary.RetiredIndexes, func(i, j int) bool { return summary.RetiredIndexes[i].IndexID < summary.RetiredIndexes[j].IndexID })
}
