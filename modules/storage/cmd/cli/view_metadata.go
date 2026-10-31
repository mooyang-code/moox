package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

type retainedIndex struct {
	Engine  string `json:"engine"`
	IndexID string `json:"index_id"`
}

type removedViewsSummary struct {
	KeptViews      int             `json:"kept_views"`
	DeletedViews   int             `json:"deleted_views"`
	RetiredIndexes []retainedIndex `json:"retired_indexes,omitempty"`
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
		return fmt.Errorf("storage-view status is indeterminate; refusing to change View metadata: %w", err)
	}
	switch state {
	case "stopped", "paused":
		// paused 是 moox-runtime pause 之后的状态：进程已停，主机守护也不会拉起。
		return nil
	case "running":
		return errors.New("storage-view must be stopped before changing View metadata")
	default:
		return fmt.Errorf("storage-view state is %q; refusing to change View metadata", state)
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

// viewRow 是 t_views 里和删除有关的几列。
type viewRow struct{ spaceID, viewID, engine, status, activeIndexID string }

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

func listViewRows(ctx context.Context, conn *sql.Conn) ([]viewRow, error) {
	rows, err := conn.QueryContext(ctx, `SELECT c_space_id, c_view_id, c_engine, c_status, c_active_index_id FROM t_views`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []viewRow
	for rows.Next() {
		var item viewRow
		if err := rows.Scan(&item.spaceID, &item.viewID, &item.engine, &item.status, &item.activeIndexID); err != nil {
			return nil, err
		}
		all = append(all, item)
	}
	return all, rows.Err()
}

// deleteViewMetadata 删除一个 View 的元数据，并把它占用的活动索引记进 summary，
// 供之后由 Storage View 的清理定时器回收物理文件。
func deleteViewMetadata(ctx context.Context, conn *sql.Conn, item viewRow, summary *removedViewsSummary) error {
	if strings.TrimSpace(item.activeIndexID) != "" {
		summary.RetiredIndexes = append(summary.RetiredIndexes, retainedIndex{Engine: item.engine, IndexID: item.activeIndexID})
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, item.spaceID, item.viewID); err != nil {
		return err
	}
	summary.DeletedViews++
	return nil
}

func sortRetiredIndexes(summary *removedViewsSummary) {
	sort.Slice(summary.RetiredIndexes, func(i, j int) bool { return summary.RetiredIndexes[i].IndexID < summary.RetiredIndexes[j].IndexID })
}
