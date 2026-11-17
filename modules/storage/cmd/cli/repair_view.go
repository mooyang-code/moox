package main

import (
	"context"
	"database/sql"
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

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
)

// defaultRepairTimeout 是 repair-view 的整体时限。要明显大于 storage-view 单元的 TimeoutStopSec（120 秒）：
// 停得慢时不能让命令自己的超时先到。
const defaultRepairTimeout = 10 * time.Minute

// restoreStorageViewCommand 是 storage-view 被停掉之后恢复它的命令，报错与提示里原样给出。
const restoreStorageViewCommand = "moox-cli setup start-component --host <存储机> --component storage-view --yes"

// repairViewOptions 是 repair-view 的参数：重建指定 View。
type repairViewOptions struct {
	spaceID     string
	viewID      string
	storageConf string
	viewConf    string
	timeout     time.Duration
	yes         bool
	dryRun      bool
	restart     bool
}

type repairViewSummary struct {
	SpaceID            string   `json:"space_id"`
	ViewID             string   `json:"view_id"`
	DBPath             string   `json:"db_path"`
	IndexID            string   `json:"index_id"`
	ViewIndexesRemoved []string `json:"view_indexes_removed,omitempty"`
	Restarted          bool     `json:"restarted"`
	DryRun             bool     `json:"dry_run"`
	Warnings           []string `json:"warnings,omitempty"`
}

// storageViewUnit 是 storage-view 的 systemd 用户服务。
const storageViewUnit = "moox-storage-view.service"

// systemctlUser 执行 systemctl --user 并返回输出；测试替换它，不真的停启服务。
var systemctlUser = func(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
}

// stopStorageView 停掉 storage-view 并取消开机自启（和 moox-cli setup stop 是同一个操作），停着的期间部署也不会把它拉起。
func stopStorageView(ctx context.Context) error {
	return runSystemctlUser(ctx, "disable", "--now", storageViewUnit)
}

// startStorageView 恢复 storage-view 并开机自启（和 moox-cli setup start-component 是同一个操作）。
func startStorageView(ctx context.Context) error {
	return runSystemctlUser(ctx, "enable", "--now", storageViewUnit)
}

func runSystemctlUser(ctx context.Context, args ...string) error {
	if output, err := systemctlUser(ctx, args...); err != nil {
		return fmt.Errorf("systemctl --user %s：%w：%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// runRepairView 重建指定 View：停掉 storage-view、删除 View 的索引文件，恢复后由维护流程从 Primary 重新建索引并跟随变更日志。
// View 的已建状态只在索引文件里，元数据不用改。
func runRepairView(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("repair-view", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := repairViewOptions{timeout: defaultRepairTimeout, restart: true}
	fs.StringVar(&opts.spaceID, "space-id", "", "View 所在空间 ID")
	fs.StringVar(&opts.viewID, "view-id", "", "View ID")
	fs.StringVar(&opts.storageConf, "storage-conf", defaultStorageConfigPath(), "storage-primary 的 storage.yaml 路径（元数据库在这里）")
	fs.StringVar(&opts.viewConf, "view-conf", "", "storage-view 的 storage.yaml 路径（View 索引目录在这里）；不给时用 --storage-conf")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "整个操作的超时")
	fs.BoolVar(&opts.yes, "yes", false, "确认删除 View 的索引并重建")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "只查看 View，不停服务、不改状态")
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
	fmt.Fprintln(stdout, "操作: 停掉 storage-view、删除 View 的索引文件，恢复后从 Primary 重新建索引")
	fs.SetOutput(stdout)
	fs.PrintDefaults()
}

func runRepairViewOperation(ctx context.Context, opts repairViewOptions, stdout, stderr io.Writer) error {
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	viewIndexRoot := storage.Devices.ViewIndexRoot
	if strings.TrimSpace(opts.viewConf) != "" {
		view, err := loadStorage(opts.viewConf)
		if err != nil {
			return err
		}
		viewIndexRoot = view.Devices.ViewIndexRoot
	}
	dbPath := metadataDBPath(storage)
	engine, err := inspectRepairView(ctx, dbPath, opts.spaceID, opts.viewID)
	if err != nil {
		return err
	}
	indexID := viewindex.ViewIndexID(opts.spaceID, opts.viewID)
	summary := repairViewSummary{
		SpaceID:  opts.spaceID,
		ViewID:   opts.viewID,
		DBPath:   dbPath,
		IndexID:  indexID,
		DryRun:   opts.dryRun,
		Warnings: []string{"删除索引后 View 不可读，直到从 Primary 重新建完；记录型 View 重建后为空，只跟随新写入"},
	}
	if opts.dryRun {
		return writeOperationResult(stdout, operationResult{Module: "storage", Action: "repair-view", Status: "dry_run", Summary: summary})
	}
	// 恢复要在停之前就登记好：systemctl disable --now 停得慢、被命令的超时打断时，单元可能已经被停用（disabled），
	// 不恢复的话之后每次部署都会把它当成用户停用的、不再拉起。--restart=false 时不自动恢复，但把恢复命令告诉用户。
	started := false
	defer func() {
		if started {
			return
		}
		if !opts.restart {
			fmt.Fprintf(stderr, "storage-view 保持停用（--restart=false）；要恢复时执行 %s\n", restoreStorageViewCommand)
			return
		}
		// 用新的上下文：走到这里时命令自己的时限可能已经到了。
		if err := startStorageView(context.Background()); err != nil {
			fmt.Fprintf(stderr, "恢复 storage-view 失败，请执行 %s：%v\n", restoreStorageViewCommand, err)
		}
	}()
	if err := stopStorageView(ctx); err != nil {
		return fmt.Errorf("停掉 storage-view：%w。storage-view 可能已经被停用；命令结束时会尝试恢复，没恢复成功就执行 %s", err, restoreStorageViewCommand)
	}
	removed, err := purgeRepairViewIndexes(viewIndexRoot, engine, indexID)
	if err != nil {
		return fmt.Errorf("删除 View 索引：%w", err)
	}
	summary.ViewIndexesRemoved = removed
	if opts.restart {
		if err := startStorageView(ctx); err != nil {
			return fmt.Errorf("恢复 storage-view：%w", err)
		}
		started = true
		summary.Restarted = true
	}
	return writeOperationResult(stdout, operationResult{Module: "storage", Action: "repair-view", Status: "ok", Summary: summary})
}

// inspectRepairView 确认 View 存在并返回它的引擎。
func inspectRepairView(ctx context.Context, dbPath, spaceID, viewID string) (string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return "", fmt.Errorf("metadata DB %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var engine string
	err = db.QueryRowContext(ctx, `SELECT c_engine FROM t_views WHERE c_space_id = ? AND c_view_id = ?`, spaceID, viewID).Scan(&engine)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("View %s/%s 不存在", spaceID, viewID)
	}
	if err != nil {
		return "", err
	}
	if engine == "" {
		engine = "duckdb"
	}
	return engine, nil
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
