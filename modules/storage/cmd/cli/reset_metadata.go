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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	trpc "trpc.group/trpc-go/trpc-go"
)

// storage reset-metadata：系统重置里 Storage 的元数据部分（设计 5.2，`--scope all`）。
//
// 备份元数据库后清空全部业务定义——空间、数据源、标的与标签、数据集与字段、View 及其构建/同步状态——再用 --seed 重新导入种子，
// 回到刚完成部署、尚未激活数据集的状态。保留：库结构（t_schema_meta）、DataNode 登记与存储设备、
// 标的修订号（按空间单调递增，策略等调用方靠它判断缓存是否过期，不能回退）。
//
// 必须先停掉 Storage（元数据库由 Primary 进程持有）。行数据与事件流不在这里：先用
// `reset-view-consumers --reset-all-storage-data --restart=false` 清 Pebble、事件流和 View 消费者，再执行本命令，
// 然后启动 Storage，执行 activate-datasets，并让 Collector 重新登记（见部署与运维文档）。

// metadataResetClear 是要清空的表，子表在前（外键 RESTRICT 的父表在后）。
var metadataResetClear = []string{
	"t_view_columns", "t_view_sync_points", "t_view_period_dataset_states",
	"t_views", "t_dataset_columns", "t_datasets", "t_fields", "t_field_groups",
	"t_subject_tags", "t_tags", "t_subjects", "t_data_sources", "t_spaces",
}

// metadataResetKeep 是不清的表。
var metadataResetKeep = []string{"t_data_nodes", "t_schema_meta", "t_space_revisions", "t_storage_devices"}

type resetMetadataOptions struct {
	Storage    storageconfig.StorageConfig
	SchemaPath string
	SeedPath   string
	DryRun     bool
}

type resetMetadataSummary struct {
	Module    string           `json:"module"`
	Action    string           `json:"action"`
	Status    string           `json:"status"`
	DBPath    string           `json:"db_path"`
	Backup    string           `json:"backup,omitempty"`
	Seed      string           `json:"seed_path,omitempty"`
	Deleted   map[string]int64 `json:"deleted"`
	Keeps     []string         `json:"keeps"`
	Imported  *importSummary   `json:"imported,omitempty"`
	NextSteps []string         `json:"next_steps,omitempty"`
}

func runResetMetadata(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reset-metadata", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := commandOptions{}
	registerCommonFlags(fs, &opts)
	fs.StringVar(&opts.seedPath, "seed", "", "metadata seed yaml path")
	var yes, dryRun bool
	var packageRoot string
	fs.StringVar(&packageRoot, "package-root", "", "storage package root（配置里的相对路径据此解析，和 reset-view-consumers 一致）")
	fs.BoolVar(&yes, "yes", false, "确认清空全部业务定义（执行前先备份）；Storage 必须已停止")
	fs.BoolVar(&dryRun, "dry-run", false, "只列出会删除的行数，不改动")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected reset-metadata arguments: %s", strings.Join(fs.Args(), " "))
	}
	if !dryRun && !yes {
		return errors.New("reset-metadata 会清空全部业务定义，需要加 --yes 确认（先用 --dry-run 看会删什么；Storage 必须已停止）")
	}
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	storage = resolveRepairStoragePaths(storage, packageRoot, opts.storageConf)
	seed := resolveSeedPath(opts.seedPath, opts.storageConf)
	if seed == "" {
		return errors.New("需要 --seed（或环境变量 STORAGE_SEED_FILE）：清空之后要立刻导入种子")
	}
	summary, err := resetMetadata(trpc.BackgroundContext(), resetMetadataOptions{Storage: storage, SchemaPath: resolveSchemaPath(opts.schemaPath), SeedPath: seed, DryRun: dryRun})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(summary)
}

func resetMetadata(ctx context.Context, opts resetMetadataOptions) (resetMetadataSummary, error) {
	dbPath := metadataDBPath(opts.Storage)
	summary := resetMetadataSummary{Module: "storage", Action: "reset-metadata", DBPath: dbPath, Seed: opts.SeedPath, Deleted: map[string]int64{}, Keeps: metadataResetKeep}
	if _, err := os.Stat(dbPath); err != nil {
		return summary, fmt.Errorf("找不到元数据库 %s：%w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return summary, err
	}
	defer db.Close()
	if err := checkMetadataTables(ctx, db); err != nil {
		return summary, err
	}
	if opts.DryRun {
		summary.Status = "dry-run"
		for _, t := range metadataResetClear {
			var n int64
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
				return summary, err
			}
			summary.Deleted[t] = n
		}
		return summary, nil
	}
	dir := filepath.Join(filepath.Dir(dbPath), "reset-backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return summary, err
	}
	backup := filepath.Join(dir, "storage-metadata-"+time.Now().UTC().Format("20060102T150405.000")+".db")
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
		return summary, fmt.Errorf("备份元数据库到 %s：%w", backup, err)
	}
	pruneMetadataBackups(dir, 3)
	summary.Backup = backup

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return summary, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
		return summary, err
	}
	for _, t := range metadataResetClear {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+t)
		if err != nil {
			return summary, fmt.Errorf("清空 %s：%w", t, err)
		}
		summary.Deleted[t], _ = res.RowsAffected()
		_, _ = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name = ?", t)
	}
	if err := tx.Commit(); err != nil {
		return summary, err
	}
	_ = db.Close()

	result, err := metadata.ImportSeed(ctx, metadata.SeedOptions{Storage: opts.Storage, SchemaPath: opts.SchemaPath, SeedPath: opts.SeedPath})
	if err != nil {
		return summary, fmt.Errorf("定义已清空（备份在 %s），但种子导入失败，修好后重新执行 import-seed：%w", backup, err)
	}
	summary.Imported = &importSummary{
		Spaces: result.Spaces, DataSources: result.DataSources, Subjects: result.Subjects, Tags: result.Tags, Datasets: result.Datasets,
		Fields: result.Fields, DatasetColumns: result.DatasetColumns, Views: result.Views, ViewColumns: result.ViewColumns, Devices: result.Devices,
	}
	summary.Status = "ok"
	summary.NextSteps = []string{"启动 Storage", "activate-datasets 激活数据集", "Collector 重新登记（fetch-plan reregister）"}
	return summary, nil
}

// checkMetadataTables 要求库里每张业务表都已归类；schema 新增了表而清单漏了就拒绝，避免留下清不掉的数据。
func checkMetadataTables(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 't\\_%' ESCAPE '\\'")
	if err != nil {
		return err
	}
	defer rows.Close()
	known := map[string]bool{}
	for _, group := range [][]string{metadataResetClear, metadataResetKeep} {
		for _, t := range group {
			known[t] = true
		}
	}
	var unclassified []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if !known[name] {
			unclassified = append(unclassified, name)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return fmt.Errorf("有未归类的表，先更新重置清单：%s", strings.Join(unclassified, "、"))
	}
	return nil
}

func pruneMetadataBackups(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "storage-metadata-") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}
