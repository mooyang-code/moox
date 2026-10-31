package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type deleteViewOptions struct {
	metadataDB  string
	packageRoot string
	view        string
	yes         bool
}

// runDeleteView 离线删除一个 View 的元数据，用在没有别的入口（控制台没有删除视图的页面）时下线单个 View。
// 直接改 Metadata SQLite，所以 storage-view 必须已停止；不同的是它只删指定的一个，
// 不会波及因子结果视图等其他动态视图。物理索引文件由 storage-view 的清理定时器在确认无引用后回收。
func runDeleteView(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("delete-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := deleteViewOptions{}
	fs.StringVar(&opts.metadataDB, "metadata-db", "", "Metadata SQLite database path")
	fs.StringVar(&opts.packageRoot, "package-root", "", "Storage package root; the View process must be stopped")
	fs.StringVar(&opts.view, "view", "", "View to delete, in space_id/view_id form")
	fs.BoolVar(&opts.yes, "yes", false, "confirm permanent metadata deletion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected delete-view arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateDeleteViewOptions(opts); err != nil {
		return err
	}
	if err := ensureStorageViewStopped(opts.packageRoot); err != nil {
		return err
	}
	summary, err := deleteView(context.Background(), opts)
	if err != nil {
		return err
	}
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "delete-view", Status: "ready_for_physical_cleanup", Summary: summary})
}

func validateDeleteViewOptions(opts deleteViewOptions) error {
	if !opts.yes {
		return errors.New("delete-view permanently deletes View metadata; re-run with --yes")
	}
	if strings.TrimSpace(opts.metadataDB) == "" {
		return errors.New("--metadata-db is required")
	}
	if strings.TrimSpace(opts.packageRoot) == "" {
		return errors.New("--package-root is required so delete-view can verify storage-view is stopped")
	}
	if _, _, err := splitViewRef(opts.view); err != nil {
		return err
	}
	return nil
}

func splitViewRef(value string) (spaceID, viewID string, err error) {
	spaceID, viewID, ok := strings.Cut(strings.TrimSpace(value), "/")
	spaceID, viewID = strings.TrimSpace(spaceID), strings.TrimSpace(viewID)
	if !ok || spaceID == "" || viewID == "" || strings.Contains(viewID, "/") {
		return "", "", fmt.Errorf("invalid --view %q; expected space_id/view_id", value)
	}
	return spaceID, viewID, nil
}

func deleteView(ctx context.Context, opts deleteViewOptions) (removedViewsSummary, error) {
	spaceID, viewID, err := splitViewRef(opts.view)
	if err != nil {
		return removedViewsSummary{}, err
	}
	summary := removedViewsSummary{}
	err = withMetadataTx(ctx, opts.metadataDB, func(conn *sql.Conn) error {
		all, err := listViewRows(ctx, conn)
		if err != nil {
			return err
		}
		for _, item := range all {
			if item.spaceID == spaceID && item.viewID == viewID {
				return deleteViewMetadata(ctx, conn, item, &summary)
			}
		}
		return fmt.Errorf("View %s/%s does not exist", spaceID, viewID)
	})
	if err != nil {
		return removedViewsSummary{}, err
	}
	sortRetiredIndexes(&summary)
	return summary, nil
}
