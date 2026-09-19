package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/spf13/cobra"
	"gorm.io/gorm"
)

type collectorTaskPurgeFlags struct {
	File             string
	DBPath           string
	SpaceID          string
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	StopCommand      string
	BackupDir        string
	SeedFile         string
	CollectorInitBin string
	Apply            bool
	Confirm          bool
}

type collectorTaskResultRef struct {
	TaskID          string `gorm:"column:c_task_id" json:"task_id"`
	TaskName        string `gorm:"column:c_task_name" json:"task_name"`
	ResultViewID    string `gorm:"column:c_result_view_id" json:"result_view_id,omitempty"`
	ResultDatasetID string `gorm:"column:c_result_dataset_id" json:"result_dataset_id,omitempty"`
}

type collectorTaskPurgeSummary struct {
	Status                    string                   `json:"status"`
	DryRun                    bool                     `json:"dry_run"`
	SpaceID                   string                   `json:"space_id"`
	DBPath                    string                   `json:"db_path"`
	BackupPath                string                   `json:"backup_path,omitempty"`
	TaskCount                 int64                    `json:"task_count"`
	TaskInstanceCount         int64                    `json:"task_instance_count"`
	FetchBatchCount           int64                    `json:"fetch_batch_count"`
	FetchRetryCount           int64                    `json:"fetch_retry_count"`
	CollectorOwnedResultCount int                      `json:"collector_owned_result_count"`
	UnlinkedResultCount       int64                    `json:"unlinked_result_count"`
	TaskIDs                   []string                 `json:"task_ids,omitempty"`
	Results                   []collectorTaskResultRef `json:"results,omitempty"`
	CompletedStages           []string                 `json:"completed_stages,omitempty"`
}

var collectorTaskPurgeFlagsValue collectorTaskPurgeFlags

var collectorTaskCmd = &cobra.Command{
	Use:   "task",
	Short: "采集任务维护工具",
}

var collectorTaskPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "清理 Collector 任务运行数据和结果引用",
	Long:  "默认只输出清单，不修改任何数据。执行清理必须同时指定 --apply --confirm；命令会先备份数据库，再调用 Collector CLI 初始化程序重建新 Schema。",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		summary, err := runCollectorTaskPurge(cmd.Context(), collectorTaskPurgeFlagsValue)
		if summary != nil {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if encodeErr := encoder.Encode(summary); encodeErr != nil && err == nil {
				return encodeErr
			}
		}
		return err
	},
}

func init() {
	collectorCmd.AddCommand(collectorTaskCmd)
	collectorTaskCmd.AddCommand(collectorTaskPurgeCmd)

	flags := collectorTaskPurgeCmd.Flags()
	flags.StringVar(&collectorTaskPurgeFlagsValue.File, "file", "", "部署配置文件，仅用于记录操作来源")
	flags.StringVar(&collectorTaskPurgeFlagsValue.DBPath, "db-path", "", "Collector SQLite 数据库路径；默认取 MOOX_COLLECTOR_DB_PATH 或 ./data/moox_collector.db")
	flags.StringVar(&collectorTaskPurgeFlagsValue.SpaceID, "space-id", "", "只清理指定空间；留空表示清理全部空间")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ControlURL, "control-url", "", "Collector 控制面地址；apply 时用于按任务删除结果资源")
	flags.StringVar(&collectorTaskPurgeFlagsValue.AccessToken, "access-token", "", "控制面登录态；默认取 MOOX_ACCESS_TOKEN")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ServiceAccessKey, "service-access-key", "", "控制面服务签名 key id")
	flags.StringVar(&collectorTaskPurgeFlagsValue.ServiceSecretKey, "service-secret-key", "", "控制面服务签名 secret")
	flags.StringVar(&collectorTaskPurgeFlagsValue.StopCommand, "stop-command", "", "删除结果后停止 Collector 写入的本地命令")
	flags.StringVar(&collectorTaskPurgeFlagsValue.BackupDir, "backup-dir", "", "备份目录；默认写入数据库所在目录")
	flags.StringVar(&collectorTaskPurgeFlagsValue.SeedFile, "seed-file", "", "重建 Schema 后使用的 Collector 任务种子文件")
	flags.StringVar(&collectorTaskPurgeFlagsValue.CollectorInitBin, "collector-init-bin", "moox-collector-cli", "Collector CLI 初始化程序路径")
	flags.BoolVar(&collectorTaskPurgeFlagsValue.Apply, "apply", false, "执行备份和重置")
	flags.BoolVar(&collectorTaskPurgeFlagsValue.Confirm, "confirm", false, "确认执行破坏性重置")
}

func runCollectorTaskPurge(ctx context.Context, flags collectorTaskPurgeFlags) (*collectorTaskPurgeSummary, error) {
	dbPath := strings.TrimSpace(flags.DBPath)
	if strings.TrimSpace(flags.File) != "" {
		root, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve collector purge config root: %w", err)
		}
		snapshot, err := setupconfig.Load(flags.File, root)
		if err != nil {
			return nil, fmt.Errorf("load collector purge config: %w", err)
		}
		if dbPath == "" {
			paths := snapshot.Manifest.Paths.Resolved()
			dbPath = filepath.Join(paths.ControlRoot, "data", "moox_collector.db")
		}
	}
	if dbPath == "" {
		dbPath = strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_DB_PATH"))
	}
	if dbPath == "" {
		dbPath = "./data/moox_collector.db"
	}
	dbPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve collector database path: %w", err)
	}

	summary := &collectorTaskPurgeSummary{
		Status:  "dry_run",
		DryRun:  true,
		SpaceID: strings.TrimSpace(flags.SpaceID),
		DBPath:  dbPath,
		Results: []collectorTaskResultRef{},
	}
	if err := inspectCollectorTaskPurge(dbPath, summary); err != nil {
		return summary, err
	}
	if !flags.Apply {
		return summary, nil
	}
	if !flags.Confirm {
		return summary, fmt.Errorf("collector task purge requires both --apply and --confirm")
	}
	if len(summary.TaskIDs) > 0 && strings.TrimSpace(flags.ControlURL) == "" {
		return summary, fmt.Errorf("collector task purge apply requires --control-url to delete task-owned results")
	}
	if strings.TrimSpace(flags.StopCommand) == "" {
		return summary, fmt.Errorf("collector task purge apply requires --stop-command to stop Collector writes")
	}
	if err := applyCollectorTaskPurge(ctx, flags, summary); err != nil {
		return summary, err
	}
	summary.Status = "applied"
	summary.DryRun = false
	return summary, nil
}

func inspectCollectorTaskPurge(dbPath string, summary *collectorTaskPurgeSummary) error {
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("collector database not found: %s", dbPath)
		}
		return fmt.Errorf("stat collector database: %w", err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+dbPath+"?mode=ro"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("open collector database read-only: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get collector database handle: %w", err)
	}
	defer sqlDB.Close()
	space := strings.TrimSpace(summary.SpaceID)
	where := ""
	args := []any{}
	if space != "" {
		where = " WHERE c_space_id = ?"
		args = append(args, space)
	}
	for _, item := range []struct {
		name  string
		table string
		out   *int64
	}{
		{name: "tasks", table: "t_collector_tasks", out: &summary.TaskCount},
		{name: "task instances", table: "t_collector_task_instances", out: &summary.TaskInstanceCount},
		{name: "fetch batches", table: "t_collector_fetch_batches", out: &summary.FetchBatchCount},
		{name: "fetch retries", table: "t_collector_fetch_retry_items", out: &summary.FetchRetryCount},
	} {
		if !collectorTableExists(db, item.table) {
			continue
		}
		query := "SELECT count(*) FROM " + item.table + where
		if err := db.Raw(query, args...).Scan(item.out).Error; err != nil {
			return fmt.Errorf("count collector %s: %w", item.name, err)
		}
	}
	if collectorTableExists(db, "t_collector_tasks") {
		var refs []collectorTaskResultRef
		query := db.Raw("SELECT c_task_id, c_task_name, c_result_view_id, c_result_dataset_id FROM t_collector_tasks"+where+" ORDER BY c_id", args...)
		if err := query.Scan(&refs).Error; err != nil {
			return fmt.Errorf("list collector-owned results: %w", err)
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref.TaskID) != "" {
				summary.TaskIDs = append(summary.TaskIDs, ref.TaskID)
			}
		}
		for _, ref := range refs {
			if strings.TrimSpace(ref.ResultViewID) == "" || strings.TrimSpace(ref.ResultDatasetID) == "" {
				summary.UnlinkedResultCount++
				continue
			}
			summary.Results = append(summary.Results, ref)
		}
		summary.CollectorOwnedResultCount = len(summary.Results)
	}
	return nil
}

func collectorTableExists(db *gorm.DB, table string) bool {
	var count int64
	return db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error == nil && count > 0
}

func applyCollectorTaskPurge(ctx context.Context, flags collectorTaskPurgeFlags, summary *collectorTaskPurgeSummary) error {
	if summary == nil {
		return fmt.Errorf("collector task purge summary is nil")
	}
	control := adminclient.New(strings.TrimSpace(flags.ControlURL))
	control.AccessToken = firstNonEmpty(strings.TrimSpace(flags.AccessToken), os.Getenv("MOOX_ACCESS_TOKEN"))
	if key, secret := strings.TrimSpace(flags.ServiceAccessKey), strings.TrimSpace(flags.ServiceSecretKey); key != "" || secret != "" {
		if key == "" || secret == "" {
			return fmt.Errorf("service access key and secret must be provided together")
		}
		control.ServiceAuth = &adminclient.ServiceAuthConfig{AccessKey: key, SecretKey: secret}
	}
	for _, taskID := range summary.TaskIDs {
		deleteResultData := false
		for _, result := range summary.Results {
			if result.TaskID == taskID && strings.TrimSpace(result.ResultViewID) != "" && strings.TrimSpace(result.ResultDatasetID) != "" {
				deleteResultData = true
				break
			}
		}
		if err := control.DeleteTask(ctx, firstNonEmpty(strings.TrimSpace(flags.SpaceID), summary.SpaceID), taskID, deleteResultData); err != nil {
			return fmt.Errorf("delete collector task %s: %w", taskID, err)
		}
	}
	if len(summary.TaskIDs) > 0 {
		summary.CompletedStages = append(summary.CompletedStages, "collector_task_results_deleted")
	}
	stopCommand := strings.TrimSpace(flags.StopCommand)
	if output, err := exec.CommandContext(ctx, "sh", "-c", stopCommand).CombinedOutput(); err != nil {
		return fmt.Errorf("stop Collector before database reset: %w: %s", err, strings.TrimSpace(string(output)))
	}
	summary.CompletedStages = append(summary.CompletedStages, "collector_writes_stopped")
	if strings.TrimSpace(flags.SpaceID) != "" {
		summary.CompletedStages = append(summary.CompletedStages, "collector_space_purged")
		return nil
	}

	initBin := strings.TrimSpace(flags.CollectorInitBin)
	if initBin == "" {
		return fmt.Errorf("collector init program is required")
	}
	if _, err := exec.LookPath(initBin); err != nil && !filepath.IsAbs(initBin) {
		return fmt.Errorf("collector init program %q is unavailable: %w", initBin, err)
	}
	backupDir := strings.TrimSpace(flags.BackupDir)
	if backupDir == "" {
		backupDir = filepath.Join(filepath.Dir(summary.DBPath), "collector-reset-backups")
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return fmt.Errorf("create collector backup directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, filepath.Base(summary.DBPath)+"."+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(summary.DBPath, backupPath); err != nil {
		return fmt.Errorf("backup collector database: %w", err)
	}
	summary.BackupPath = backupPath
	summary.CompletedStages = append(summary.CompletedStages, "collector_database_backed_up")

	args := []string{"init", "--db-path", summary.DBPath}
	if strings.TrimSpace(flags.SeedFile) != "" {
		args = append(args, "--seed-file", flags.SeedFile)
	}
	command := exec.CommandContext(ctx, initBin, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("initialize collector schema after backup: %w: %s", err, strings.TrimSpace(string(output)))
	}
	summary.CompletedStages = append(summary.CompletedStages, "collector_schema_initialized")
	return nil
}
