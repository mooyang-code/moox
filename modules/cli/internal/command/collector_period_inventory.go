package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/glebarez/sqlite"
	"github.com/spf13/cobra"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	collectorPeriodInventoryVersion              = "collector-period-inventory/v1"
	collectorPeriodInventoryMaxLimit             = 100000
	collectorPeriodInventoryDefault              = 10000
	collectorPeriodInventoryTimerMaxItems        = 40
	collectorPeriodInventoryMaxTimerRequestBytes = 256 * 1024
)

type collectorPeriodInventoryFlags struct {
	DBPath   string
	SpaceID  string
	MaxItems int
}

type collectorPeriodInventoryActiveBatch struct {
	BatchRef    string `json:"batch_ref"`
	ScheduleRef string `json:"schedule_ref"`
	Kind        string `json:"kind"`
	ShardIndex  int64  `json:"shard_index"`
	Frequency   string `json:"frequency"`
	Status      string `json:"status"`
	DeadlineAt  string `json:"deadline_at,omitempty"`
}

type collectorPeriodInventoryPendingRetry struct {
	RetryRef                string   `json:"retry_ref"`
	SourceBatchRef          string   `json:"source_batch_ref"`
	DatasetIDs              []string `json:"dataset_ids,omitempty"`
	Frequency               string   `json:"frequency"`
	TargetDataTime          string   `json:"target_data_time"`
	Status                  string   `json:"status"`
	FailureReportState      string   `json:"failure_report_state"`
	FailureDeadlineExceeded bool     `json:"failure_deadline_exceeded"`
}

type collectorPeriodInventoryFailureTarget struct {
	WriteTargetID string `json:"write_target_id"`
	SpaceID       string `json:"space_id"`
	DatasetID     string `json:"dataset_id"`
	SeriesHash    string `json:"series_hash"`
	ExpectedCount uint32 `json:"expected_count"`
	SeriesIndex   uint32 `json:"series_index"`
}

type collectorPeriodInventoryFailureResult struct {
	WriteTargetID string `json:"write_target_id"`
	SpaceID       string `json:"space_id"`
	DatasetID     string `json:"dataset_id"`
	Frequency     string `json:"frequency"`
	PeriodTime    string `json:"period_time"`
	SeriesHash    string `json:"series_hash"`
	ExpectedCount uint32 `json:"expected_count"`
	SeriesIndex   uint32 `json:"series_index"`
	Disposition   string `json:"disposition"`
	ObservedAt    string `json:"observed_at"`
}

type collectorPeriodInventorySchema struct {
	Profile           string   `json:"profile"`
	Complete          bool     `json:"complete"`
	MissingTables     []string `json:"missing_tables"`
	MissingColumns    []string `json:"missing_columns"`
	UnavailableTables []string `json:"unavailable_tables"`
	Incompatible      []string `json:"incompatible_features"`
}

type collectorPeriodInventoryPeriod struct {
	DatasetID                  string `json:"dataset_id"`
	Frequency                  string `json:"frequency"`
	PeriodTime                 string `json:"period_time"`
	WorkType                   string `json:"work_type"`
	SnapshotRequired           bool   `json:"snapshot_required"`
	RawPeriodTime              string `json:"-" gorm:"column:raw_period_time"`
	SnapshotSeriesRows         int64  `json:"snapshot_series_rows"`
	SnapshotExpectedCountMin   int64  `json:"snapshot_expected_count_min"`
	SnapshotExpectedCountMax   int64  `json:"snapshot_expected_count_max"`
	SnapshotHashVariants       int64  `json:"snapshot_hash_variants"`
	StorageStatus              string `json:"storage_status,omitempty"`
	StorageExpectedCount       int64  `json:"storage_expected_count"`
	StorageDeadlineAt          string `json:"storage_deadline_at,omitempty"`
	StorageConfirmedAt         string `json:"-" gorm:"column:storage_confirmed_at"`
	StorageStatePresent        bool   `json:"-" gorm:"column:storage_state_present"`
	StorageStateValid          bool   `json:"-"`
	TimerBatchCount            int64  `json:"timer_batch_count"`
	TimerClaimedBatchCount     int64  `json:"timer_claimed_batch_count"`
	TimerDeadlineMin           string `json:"timer_deadline_min,omitempty"`
	TimerDeadlineMax           string `json:"timer_deadline_max,omitempty"`
	ReadinessStatus            string `json:"readiness_status,omitempty"`
	ReadinessReportState       string `json:"readiness_report_state,omitempty"`
	TimerManifestValid         bool   `json:"timer_manifest_valid"`
	ReadinessDeadlineAt        string `json:"readiness_deadline_at,omitempty"`
	ReadinessItemCount         int64  `json:"readiness_item_count"`
	ReadinessPendingItemCount  int64  `json:"readiness_pending_item_count"`
	ReadinessSuccessItemCount  int64  `json:"readiness_success_item_count"`
	ReadinessTimedOutItemCount int64  `json:"readiness_timed_out_item_count"`
	SnapshotValid              bool   `json:"snapshot_valid"`
	SnapshotSeriesHash         string `json:"-" gorm:"column:snapshot_series_hash"`
	StorageSeriesHash          string `json:"-" gorm:"column:storage_series_hash"`
	TimerSeriesHash            string `json:"-" gorm:"column:timer_series_hash"`
	TimerExpectedCountMin      int64  `json:"-" gorm:"column:timer_expected_count_min"`
	TimerExpectedCountMax      int64  `json:"-" gorm:"column:timer_expected_count_max"`
	TimerHashVariants          int64  `json:"-" gorm:"column:timer_hash_variants"`
	TimerGroupIDVariants       int64  `json:"-" gorm:"column:timer_group_id_variants"`
	TimerGroupCountVariants    int64  `json:"-" gorm:"column:timer_group_count_variants"`
	TimerShardIndexVariants    int64  `json:"-" gorm:"column:timer_shard_index_variants"`
	TimerInvalidGroupRows      int64  `json:"-" gorm:"column:timer_invalid_group_rows"`
	TimerClaimMetadataIssues   int64  `json:"-" gorm:"column:timer_claim_metadata_issues"`
}

type collectorPeriodInventoryTimerItem struct {
	InstanceID          string   `json:"instance_id"`
	SubjectID           string   `json:"subject_id"`
	Symbol              string   `json:"symbol"`
	TargetDataTime      string   `json:"target_data_time,omitempty"`
	StartTime           string   `json:"start_time,omitempty"`
	EndTime             string   `json:"end_time,omitempty"`
	SnapshotAt          string   `json:"snapshot_at,omitempty"`
	BarLimit            int      `json:"bar_limit,omitempty"`
	Canary              bool     `json:"canary,omitempty"`
	RequirePeriodCommit bool     `json:"require_period_commit,omitempty"`
	CandidateIndex      int      `json:"candidate_index,omitempty"`
	RateBudgetRatio     float64  `json:"rate_budget_ratio,omitempty"`
	SourceEventID       string   `json:"source_event_id,omitempty"`
	Provider            string   `json:"provider"`
	SourceID            string   `json:"source_id"`
	MarketID            string   `json:"market_id,omitempty"`
	InstrumentType      string   `json:"instrument_type,omitempty"`
	MarketType          string   `json:"market_type"`
	DataType            string   `json:"data_type"`
	DatasetID           string   `json:"dataset_id"`
	Frequency           string   `json:"frequency"`
	OutputFields        []string `json:"output_fields,omitempty"`
	SnapshotShardIndex  int      `json:"snapshot_shard_index,omitempty"`
	SnapshotShardCount  int      `json:"snapshot_shard_count,omitempty"`
	SeriesIndex         uint32   `json:"series_index"`
	SeriesHash          string   `json:"series_hash"`
	ExpectedCount       uint32   `json:"expected_count"`
}

type collectorPeriodInventoryTimerTarget struct {
	WriteTargetID  string     `json:"write_target_id"`
	SpaceID        string     `json:"space_id"`
	InstanceID     string     `json:"instance_id"`
	TaskID         string     `json:"task_id"`
	DatasetID      string     `json:"dataset_id"`
	ViewID         string     `json:"view_id,omitempty"`
	OutputFields   string     `json:"output_fields_json,omitempty"`
	Frequency      string     `json:"frequency"`
	TargetDataTime string     `json:"target_data_time"`
	SeriesIndex    uint32     `json:"series_index"`
	SeriesHash     string     `json:"series_hash"`
	ExpectedCount  uint32     `json:"expected_count"`
	Status         string     `json:"status"`
	Attempt        int        `json:"attempt"`
	LastError      string     `json:"last_error,omitempty"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"`
}

type collectorPeriodInventoryDNSResolution struct {
	IPs        []string          `json:"ips,omitempty"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
	LatencyMS  map[string]uint32 `json:"latency_ms,omitempty"`
}

type collectorPeriodInventoryTimerOwner struct {
	TaskID       string
	FirstRunID   string
	RouteVersion string
}

type collectorPeriodInventoryTimerRequest struct {
	BatchID             string                                           `json:"batch_id"`
	SyncPointID         string                                           `json:"sync_point_id,omitempty"`
	ScheduleID          string                                           `json:"schedule_id,omitempty"`
	BatchKind           string                                           `json:"batch_kind"`
	SpaceID             string                                           `json:"space_id"`
	MarketID            string                                           `json:"market_id,omitempty"`
	InstrumentType      string                                           `json:"instrument_type,omitempty"`
	DatasetID           string                                           `json:"dataset_id"`
	Frequency           string                                           `json:"frequency"`
	Provider            string                                           `json:"provider"`
	SourceID            string                                           `json:"source_id"`
	MarketType          string                                           `json:"market_type"`
	Region              string                                           `json:"region"`
	NodeID              string                                           `json:"node_id"`
	FunctionName        string                                           `json:"function_name"`
	RequestID           string                                           `json:"request_id,omitempty"`
	ShardIndex          int                                              `json:"shard_index,omitempty"`
	GroupID             int                                              `json:"group_id,omitempty"`
	GroupCount          int                                              `json:"group_count,omitempty"`
	BindingHash         string                                           `json:"binding_hash"`
	RouteVersion        string                                           `json:"route_version,omitempty"`
	RequirePeriodCommit bool                                             `json:"require_period_commit,omitempty"`
	Concurrency         int                                              `json:"concurrency,omitempty"`
	DNSRoutes           map[string]collectorPeriodInventoryDNSResolution `json:"dns_routes,omitempty"`
	Items               []collectorPeriodInventoryTimerItem              `json:"items"`
	Targets             []collectorPeriodInventoryTimerTarget            `json:"targets,omitempty"`
}

type collectorPeriodInventoryReport struct {
	Version               string                                 `json:"version"`
	ReadOnly              bool                                   `json:"read_only"`
	SnapshotConsistent    bool                                   `json:"snapshot_consistent"`
	Complete              bool                                   `json:"complete"`
	ScopePresent          bool                                   `json:"scope_present"`
	GeneratedAt           time.Time                              `json:"generated_at"`
	DatabasePath          string                                 `json:"database_path"`
	SpaceID               string                                 `json:"space_id"`
	Schema                collectorPeriodInventorySchema         `json:"schema"`
	TableCounts           map[string]int64                       `json:"table_counts"`
	StateCounts           map[string]map[string]int64            `json:"state_counts"`
	UnknownStateRows      map[string]int64                       `json:"unknown_state_rows"`
	IntegrityIssues       map[string]int64                       `json:"integrity_issues"`
	GlobalIntegrityIssues map[string]int64                       `json:"global_integrity_issues"`
	PeriodKeys            []collectorPeriodInventoryPeriod       `json:"period_keys"`
	PeriodKeysTruncated   bool                                   `json:"period_keys_truncated"`
	ActiveFetchBatches    []collectorPeriodInventoryActiveBatch  `json:"active_fetch_batches"`
	PendingRetries        []collectorPeriodInventoryPendingRetry `json:"pending_retries"`
	ActivityTruncated     bool                                   `json:"activity_truncated"`
}

type collectorPeriodInventoryTableSpec struct {
	name     string
	columns  []string
	optional bool
}

var collectorPeriodInventoryTables = []collectorPeriodInventoryTableSpec{
	{name: "t_collector_tasks", columns: []string{"c_space_id", "c_enabled"}},
	{name: "t_collector_task_tags", columns: []string{"c_space_id"}},
	{name: "t_collector_task_series", columns: []string{"c_space_id"}},
	{name: "t_collector_task_period_series", columns: []string{"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_series_index", "c_series_key", "c_subject_id", "c_provider", "c_source_id", "c_market_type", "c_provider_symbol", "c_series_tag", "c_series_hash", "c_expected_count"}},
	{name: "t_collector_period_storage_states", optional: true, columns: []string{"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_status", "c_series_hash", "c_expected_count", "c_deadline_at", "c_confirmed_at"}},
	{name: "t_collector_timer_period_batches", optional: true, columns: []string{"c_space_id", "c_dataset_id", "c_frequency", "c_period_time", "c_task_id", "c_first_run_id", "c_series_hash", "c_expected_count", "c_group_id", "c_group_count", "c_shard_index", "c_binding_hash", "c_route_version", "c_batch_id", "c_function_name", "c_node_id", "c_region", "c_claim_request_id", "c_claimed_at", "c_deadline_at"}},
	{name: "t_collector_runs", columns: []string{"c_space_id", "c_status"}},
	{name: "t_collector_task_instances", columns: []string{"c_space_id", "c_last_exec_status"}},
	{name: "t_collector_instance_write_targets", columns: []string{"c_space_id", "c_write_target_id", "c_instance_id", "c_task_id", "c_dataset_id", "c_output_fields_json", "c_series_index", "c_series_hash", "c_expected_count", "c_status"}},
	{name: "t_collector_fetch_batches", columns: []string{"c_space_id", "c_batch_id", "c_parent_batch_id", "c_schedule_id", "c_batch_kind", "c_shard_index", "c_instance_id", "c_write_target_id", "c_retry_scope", "c_frequency", "c_function_name", "c_request_id", "c_status", "c_attempt", "c_planned_count", "c_dispatched_at", "c_deadline_at", "c_request_json", "c_node_id", "c_region"}},
	{name: "t_collector_fetch_batch_items", columns: []string{"c_space_id", "c_batch_id", "c_instance_id", "c_status"}},
	{name: "t_collector_fetch_retry_items", columns: []string{"c_space_id", "c_retry_key", "c_source_batch_id", "c_write_target_id", "c_instance_id", "c_retry_scope", "c_frequency", "c_target_data_time", "c_task_json", "c_failure_targets_json", "c_status"}},
	{name: "t_period_readiness", columns: []string{"c_id", "c_space_id", "c_dataset_id", "c_frequency", "c_work_type", "c_period_time", "c_deadline_at", "c_status", "c_report_state"}},
	{name: "t_period_readiness_items", columns: []string{"c_readiness_id", "c_state"}},
}

var collectorPeriodInventoryCmd = &cobra.Command{
	Use:   "period-inventory",
	Short: "只读盘点 Collector SQLite 周期与运行态",
	Long:  "以 SQLite read-only URI 和一致只读事务盘点指定 Space 的运行状态。报告不包含 Subject、Provider symbol、任务参数、错误内容或 payload；Schema 不完整、目标 Space 无数据、状态非法或输出结果超限时返回非零。--max-items 分别限制周期键、active batch、pending retry 三类数组。此报告不是 producer 已停止或 drain 完成的证明。",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		report, err := runCollectorPeriodInventory(cmd.Context(), collectorPeriodInventoryFlagsValue)
		if report != nil {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if encodeErr := encoder.Encode(report); encodeErr != nil {
				return encodeErr
			}
		}
		return err
	},
}

var collectorPeriodInventoryFlagsValue collectorPeriodInventoryFlags

func init() {
	collectorCmd.AddCommand(collectorPeriodInventoryCmd)
	flags := collectorPeriodInventoryCmd.Flags()
	flags.StringVar(&collectorPeriodInventoryFlagsValue.DBPath, "db-path", "", "Collector SQLite 数据库的绝对路径")
	flags.StringVar(&collectorPeriodInventoryFlagsValue.SpaceID, "space-id", "", "唯一目标 Space")
	flags.IntVar(&collectorPeriodInventoryFlagsValue.MaxItems, "max-items", collectorPeriodInventoryDefault, "每类最多输出数量；周期键、active batch、pending retry 分别限额，上限 100000")
	_ = collectorPeriodInventoryCmd.MarkFlagRequired("db-path")
	_ = collectorPeriodInventoryCmd.MarkFlagRequired("space-id")
}

func runCollectorPeriodInventory(ctx context.Context, flags collectorPeriodInventoryFlags) (*collectorPeriodInventoryReport, error) {
	dbPath := strings.TrimSpace(flags.DBPath)
	spaceID := strings.TrimSpace(flags.SpaceID)
	if spaceID == "" {
		return nil, fmt.Errorf("collector period inventory requires an explicit space id")
	}
	if len(spaceID) > 256 || !utf8.ValidString(spaceID) || strings.ContainsFunc(spaceID, unicode.IsControl) {
		return nil, fmt.Errorf("collector period inventory space id is invalid")
	}
	if !filepath.IsAbs(dbPath) {
		return nil, fmt.Errorf("collector period inventory requires an absolute database path")
	}
	if flags.MaxItems == 0 {
		flags.MaxItems = collectorPeriodInventoryDefault
	}
	if flags.MaxItems < 1 || flags.MaxItems > collectorPeriodInventoryMaxLimit {
		return nil, fmt.Errorf("collector period inventory max-items must be between 1 and %d", collectorPeriodInventoryMaxLimit)
	}
	info, err := os.Lstat(dbPath)
	if err != nil {
		return nil, fmt.Errorf("stat collector database: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("collector database must be a regular non-symlink file")
	}

	report := &collectorPeriodInventoryReport{
		Version:               collectorPeriodInventoryVersion,
		ReadOnly:              true,
		DatabasePath:          dbPath,
		SpaceID:               spaceID,
		Schema:                collectorPeriodInventorySchema{MissingTables: []string{}, MissingColumns: []string{}, UnavailableTables: []string{}, Incompatible: []string{}},
		TableCounts:           map[string]int64{},
		StateCounts:           map[string]map[string]int64{},
		UnknownStateRows:      map[string]int64{},
		IntegrityIssues:       map[string]int64{},
		GlobalIntegrityIssues: map[string]int64{},
		PeriodKeys:            []collectorPeriodInventoryPeriod{},
		ActiveFetchBatches:    []collectorPeriodInventoryActiveBatch{},
		PendingRetries:        []collectorPeriodInventoryPendingRetry{},
		GeneratedAt:           time.Now().UTC(),
	}

	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath)}).String() + "?mode=ro"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return report, fmt.Errorf("open Collector SQLite read-only: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return report, fmt.Errorf("get Collector SQLite handle: %w", err)
	}
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if _, err := sqlDB.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return report, fmt.Errorf("enable Collector SQLite query_only: %w", err)
	}
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return report, fmt.Errorf("begin Collector SQLite read transaction: %w", tx.Error)
	}
	defer tx.Rollback()
	tx = tx.WithContext(ctx)

	available, err := inspectCollectorPeriodInventorySchema(tx, report)
	if err != nil {
		report.Complete = false
		return report, err
	}
	report.SnapshotConsistent = true
	if err := collectCollectorPeriodInventoryTables(ctx, tx, spaceID, report, available); err != nil {
		report.Complete = false
		return report, err
	}
	for _, count := range report.TableCounts {
		if count > 0 {
			report.ScopePresent = true
			break
		}
	}
	if !report.ScopePresent {
		report.IntegrityIssues["space_scope_empty"] = 1
	}
	if err := collectCollectorPeriodInventoryActivity(ctx, tx, spaceID, flags.MaxItems, report, available); err != nil {
		report.Complete = false
		return report, err
	}
	if err := collectCollectorPeriodInventoryKeys(ctx, tx, spaceID, flags.MaxItems, report, available); err != nil {
		report.Complete = false
		return report, err
	}
	if !report.Schema.Complete || !report.ScopePresent || len(report.UnknownStateRows) > 0 || len(report.IntegrityIssues) > 0 || len(report.GlobalIntegrityIssues) > 0 || report.PeriodKeysTruncated || report.ActivityTruncated {
		report.Complete = false
		return report, fmt.Errorf("collector period inventory is incomplete; inspect scope_present, schema, unknown_state_rows, integrity_issues, global_integrity_issues, and truncation flags")
	}
	report.Complete = true
	return report, nil
}

func inspectCollectorPeriodInventorySchema(tx *gorm.DB, report *collectorPeriodInventoryReport) (map[string]bool, error) {
	available := make(map[string]bool, len(collectorPeriodInventoryTables))
	columnSets := make(map[string]map[string]struct{}, len(collectorPeriodInventoryTables))
	for _, table := range collectorPeriodInventoryTables {
		var exists int64
		if err := tx.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table.name).Scan(&exists).Error; err != nil {
			return available, fmt.Errorf("inspect Collector table %s: %w", table.name, err)
		}
		if exists != 1 {
			if table.optional {
				report.Schema.UnavailableTables = append(report.Schema.UnavailableTables, table.name)
			} else {
				report.Schema.MissingTables = append(report.Schema.MissingTables, table.name)
			}
			continue
		}
		var columns []struct {
			Name string `gorm:"column:name"`
		}
		if err := tx.Raw("PRAGMA table_info(" + table.name + ")").Scan(&columns).Error; err != nil {
			return available, fmt.Errorf("inspect Collector columns for %s: %w", table.name, err)
		}
		columnSet := make(map[string]struct{}, len(columns))
		for _, column := range columns {
			columnSet[column.Name] = struct{}{}
		}
		columnSets[table.name] = columnSet
		complete := true
		for _, required := range table.columns {
			if _, ok := columnSet[required]; !ok {
				report.Schema.MissingColumns = append(report.Schema.MissingColumns, table.name+"."+required)
				complete = false
			}
		}
		available[table.name] = complete
	}
	receiptColumns := columnSets["t_collector_fetch_retry_items"]
	if available["t_collector_fetch_retry_items"] {
		receiptCurrent := true
		for _, column := range []string{
			"c_period_failure_report_state", "c_period_failure_results_json",
			"c_period_failure_last_error", "c_period_failure_deadline_exceeded_at",
		} {
			if _, ok := receiptColumns[column]; !ok {
				report.Schema.MissingColumns = append(report.Schema.MissingColumns, "t_collector_fetch_retry_items."+column)
				receiptCurrent = false
			}
		}
		if receiptCurrent {
			available["retry_receipt_current"] = true
		} else {
			report.Schema.Incompatible = append(report.Schema.Incompatible, "retry failure receipt columns are missing")
		}
	}
	storageAvailable := available["t_collector_period_storage_states"]
	timerAvailable := available["t_collector_timer_period_batches"]
	switch {
	case available["retry_receipt_current"] && storageAvailable && timerAvailable:
		report.Schema.Profile = "current"
	case !available["t_collector_fetch_retry_items"]:
		report.Schema.Profile = "unknown"
	default:
		report.Schema.Profile = "unsupported"
		report.Schema.Incompatible = append(report.Schema.Incompatible, "retry receipt mode and period tables do not match a supported schema profile")
	}
	sort.Strings(report.Schema.MissingTables)
	sort.Strings(report.Schema.MissingColumns)
	sort.Strings(report.Schema.UnavailableTables)
	sort.Strings(report.Schema.Incompatible)
	report.Schema.Complete = report.Schema.Profile != "unsupported" && report.Schema.Profile != "unknown" && len(report.Schema.MissingTables) == 0 && len(report.Schema.MissingColumns) == 0 && len(report.Schema.Incompatible) == 0
	report.Complete = report.Schema.Complete
	return available, nil
}

func collectCollectorPeriodInventoryTables(ctx context.Context, tx *gorm.DB, spaceID string, report *collectorPeriodInventoryReport, available map[string]bool) error {
	for _, table := range collectorPeriodInventoryTables {
		if !available[table.name] {
			continue
		}
		if table.name == "t_period_readiness_items" && !available["t_period_readiness"] {
			continue
		}
		query := "SELECT count(*) FROM " + table.name + " WHERE c_space_id = ?"
		args := []any{spaceID}
		if table.name == "t_period_readiness_items" {
			query = `SELECT count(*) FROM t_period_readiness_items i
JOIN t_period_readiness r ON r.c_id = i.c_readiness_id WHERE r.c_space_id = ?`
		}
		var count int64
		if err := tx.Raw(query, args...).Scan(&count).Error; err != nil {
			return fmt.Errorf("count Collector table %s: %w", table.name, err)
		}
		report.TableCounts[table.name] = count
	}
	if available["t_period_readiness_items"] && available["t_period_readiness"] {
		var orphanRows int64
		query := `SELECT count(*) FROM t_period_readiness_items i
LEFT JOIN t_period_readiness r ON r.c_id = i.c_readiness_id WHERE r.c_id IS NULL`
		if err := tx.Raw(query).Scan(&orphanRows).Error; err != nil {
			return fmt.Errorf("count orphan period readiness items: %w", err)
		}
		if orphanRows > 0 {
			report.GlobalIntegrityIssues["orphan_readiness_items"] = orphanRows
		}
	}
	if available["t_collector_fetch_batch_items"] && available["t_collector_fetch_batches"] {
		var orphanRows int64
		query := `SELECT count(*) FROM t_collector_fetch_batch_items i
LEFT JOIN t_collector_fetch_batches b ON b.c_space_id = i.c_space_id AND b.c_batch_id = i.c_batch_id
WHERE i.c_space_id = ? AND b.c_id IS NULL`
		if err := tx.Raw(query, spaceID).Scan(&orphanRows).Error; err != nil {
			return fmt.Errorf("count orphan fetch batch items: %w", err)
		}
		if orphanRows > 0 {
			report.IntegrityIssues["orphan_fetch_batch_items"] = orphanRows
		}
	}

	for _, metric := range []struct {
		name          string
		table         string
		column        string
		allowed       []string
		joinReadiness bool
	}{
		{name: "tasks.enabled", table: "t_collector_tasks", column: "c_enabled", allowed: []string{"enabled", "disabled"}},
		{name: "runs.status", table: "t_collector_runs", column: "c_status", allowed: []string{"planned", "active", "succeeded", "partial_failed", "failed"}},
		{name: "instances.status", table: "t_collector_task_instances", column: "c_last_exec_status", allowed: []string{"1", "2", "3"}},
		{name: "write_targets.status", table: "t_collector_instance_write_targets", column: "c_status", allowed: []string{"pending", "dispatched", "succeeded", "failed", "permanent_failed", "superseded", "timed_out"}},
		{name: "fetch_batches.status", table: "t_collector_fetch_batches", column: "c_status", allowed: []string{"planned", "dispatched", "succeeded", "partial_failed", "failed", "timed_out"}},
		{name: "fetch_batch_items.status", table: "t_collector_fetch_batch_items", column: "c_status", allowed: []string{"pending", "success", "succeeded", "failed", "timed_out", "partial_failed"}},
		{name: "fetch_retries.status", table: "t_collector_fetch_retry_items", column: "c_status", allowed: []string{"pending", "dispatched", "succeeded", "permanent_failed", "superseded"}},
		{name: "fetch_retries.failure_report_state", table: "t_collector_fetch_retry_items", column: "c_period_failure_report_state", allowed: []string{"pending", "acknowledged", "missed_deadline"}},
		{name: "storage_periods.status", table: "t_collector_period_storage_states", column: "c_status", allowed: []string{"waiting", "complete", "degraded"}},
		{name: "timer_period_batches.claim", table: "t_collector_timer_period_batches", column: "c_claim_request_id", allowed: []string{"claimed", "unclaimed"}},
		{name: "period_readiness.status", table: "t_period_readiness", column: "c_status", allowed: []string{"waiting", "complete", "degraded"}},
		{name: "period_readiness.report_state", table: "t_period_readiness", column: "c_report_state", allowed: []string{"waiting", "pending", "reported"}},
		{name: "period_readiness_items.state", table: "t_period_readiness_items", column: "c_state", allowed: []string{"pending", "success", "timed_out"}, joinReadiness: true},
	} {
		if !available[metric.table] {
			continue
		}
		if metric.joinReadiness && !available["t_period_readiness"] {
			continue
		}
		if metric.name == "fetch_retries.failure_report_state" && !available["retry_receipt_current"] {
			continue
		}
		query := "SELECT " + metric.column + " AS state, count(*) AS row_count FROM " + metric.table + " WHERE c_space_id = ? GROUP BY " + metric.column
		if metric.name == "tasks.enabled" {
			query = `SELECT CASE WHEN c_enabled = 1 THEN 'enabled' WHEN c_enabled = 0 THEN 'disabled' ELSE 'unknown' END AS state,
count(*) AS row_count FROM t_collector_tasks WHERE c_space_id = ? GROUP BY state`
		}
		if metric.name == "timer_period_batches.claim" {
			query = `SELECT CASE WHEN c_claim_request_id = '' THEN 'unclaimed' ELSE 'claimed' END AS state,
count(*) AS row_count FROM t_collector_timer_period_batches WHERE c_space_id = ? GROUP BY state`
		}
		if metric.joinReadiness {
			query = `SELECT i.c_state AS state, count(*) AS row_count FROM t_period_readiness_items i
JOIN t_period_readiness r ON r.c_id = i.c_readiness_id WHERE r.c_space_id = ? GROUP BY i.c_state`
		}
		var groups []struct {
			State    string `gorm:"column:state"`
			RowCount int64  `gorm:"column:row_count"`
		}
		if err := tx.Raw(query, spaceID).Scan(&groups).Error; err != nil {
			return fmt.Errorf("group Collector state %s: %w", metric.name, err)
		}
		counts := make(map[string]int64, len(groups))
		allowed := make(map[string]struct{}, len(metric.allowed))
		for _, state := range metric.allowed {
			allowed[state] = struct{}{}
		}
		for _, group := range groups {
			key := group.State
			if _, ok := allowed[key]; !ok {
				key = "unknown"
				report.UnknownStateRows[metric.name] += group.RowCount
			}
			counts[key] += group.RowCount
		}
		report.StateCounts[metric.name] = counts
	}
	return nil
}

func collectCollectorPeriodInventoryActivity(ctx context.Context, tx *gorm.DB, spaceID string, limit int, report *collectorPeriodInventoryReport, available map[string]bool) error {
	if available["t_collector_fetch_batches"] {
		var rows []struct {
			BatchID    string `gorm:"column:batch_id"`
			ScheduleID string `gorm:"column:schedule_id"`
			Kind       string `gorm:"column:kind"`
			ShardIndex int64  `gorm:"column:shard_index"`
			Frequency  string `gorm:"column:frequency"`
			Status     string `gorm:"column:status"`
			DeadlineAt string `gorm:"column:deadline_at"`
		}
		query := `SELECT c_batch_id AS batch_id, c_schedule_id AS schedule_id,
  c_batch_kind AS kind, c_shard_index AS shard_index, c_frequency AS frequency,
  c_status AS status, coalesce(c_deadline_at, '') AS deadline_at
FROM t_collector_fetch_batches
WHERE c_space_id = ? AND c_status IN ('planned', 'dispatched')
ORDER BY c_deadline_at, c_batch_id LIMIT ?`
		if err := tx.WithContext(ctx).Raw(query, spaceID, limit+1).Scan(&rows).Error; err != nil {
			return fmt.Errorf("list active Collector fetch batches: %w", err)
		}
		if len(rows) > limit {
			report.ActivityTruncated = true
			rows = rows[:limit]
		}
		for _, row := range rows {
			kind := row.Kind
			if !safeInventoryState(kind, "realtime", "catchup", "backfill", "gap_repair") {
				kind = "unknown"
				report.UnknownStateRows["active_fetch_batches.kind"]++
			}
			batchRef := inventoryOpaqueRef(row.BatchID)
			scheduleRef := inventoryOpaqueRef(row.ScheduleID)
			if batchRef == "" {
				report.UnknownStateRows["active_fetch_batches.batch_id"]++
			}
			if scheduleRef == "" {
				report.UnknownStateRows["active_fetch_batches.schedule_id"]++
			}
			report.ActiveFetchBatches = append(report.ActiveFetchBatches, collectorPeriodInventoryActiveBatch{
				BatchRef:    batchRef,
				ScheduleRef: scheduleRef,
				Kind:        kind,
				ShardIndex:  row.ShardIndex,
				Frequency:   safeInventoryIdentifier(row.Frequency, "active_fetch_batches.frequency", report),
				Status:      row.Status,
				DeadlineAt:  safeInventoryOptionalTimestamp(row.DeadlineAt, "active_fetch_batches.deadline_at", report),
			})
		}
	}
	if available["t_collector_fetch_retry_items"] && available["t_collector_instance_write_targets"] && available["retry_receipt_current"] {
		var rows []struct {
			RetryKey           string `gorm:"column:retry_key"`
			SourceBatchID      string `gorm:"column:source_batch_id"`
			DatasetID          string `gorm:"column:dataset_id"`
			Frequency          string `gorm:"column:frequency"`
			TargetDataTime     string `gorm:"column:target_data_time"`
			TaskJSON           string `gorm:"column:task_json"`
			FailureResultsJSON string `gorm:"column:failure_results_json"`
			Status             string `gorm:"column:status"`
			FailureReportState string `gorm:"column:failure_report_state"`
			FailureTargetsJSON string `gorm:"column:failure_targets_json"`
			DeadlineExceeded   int64  `gorm:"column:deadline_exceeded"`
		}
		reportStateExpr := "r.c_period_failure_report_state"
		deadlineExpr := "CASE WHEN r.c_period_failure_deadline_exceeded_at IS NULL THEN 0 ELSE 1 END"
		resultsExpr := "coalesce(r.c_period_failure_results_json, '[]')"
		query := fmt.Sprintf(`SELECT r.c_retry_key AS retry_key, r.c_source_batch_id AS source_batch_id,
  coalesce(w.c_dataset_id, '') AS dataset_id, r.c_frequency AS frequency,
  r.c_target_data_time AS target_data_time, r.c_task_json AS task_json, r.c_status AS status,
  %s AS failure_report_state, r.c_failure_targets_json AS failure_targets_json, %s AS failure_results_json,
  %s AS deadline_exceeded
FROM t_collector_fetch_retry_items r
LEFT JOIN t_collector_instance_write_targets w
  ON w.c_space_id = r.c_space_id AND w.c_write_target_id = r.c_write_target_id
WHERE r.c_space_id = ?
  AND (r.c_status IN ('pending', 'dispatched') OR (r.c_status = 'permanent_failed' AND (%s) = 'pending'))
ORDER BY r.c_target_data_time, r.c_retry_key LIMIT ?`, reportStateExpr, resultsExpr, deadlineExpr, reportStateExpr)
		if err := tx.WithContext(ctx).Raw(query, spaceID, limit+1).Scan(&rows).Error; err != nil {
			return fmt.Errorf("list pending Collector retries: %w", err)
		}
		if len(rows) > limit {
			report.ActivityTruncated = true
			rows = rows[:limit]
		}
		for _, row := range rows {
			status := row.Status
			if !safeInventoryState(status, "pending", "dispatched", "succeeded", "permanent_failed", "superseded") {
				status = "unknown"
				report.UnknownStateRows["pending_retries.status"]++
			}
			reportState := row.FailureReportState
			if !safeInventoryState(reportState, "pending", "acknowledged", "missed_deadline") {
				reportState = "unknown"
				report.UnknownStateRows["pending_retries.failure_report_state"]++
			}
			datasetIDs := make(map[string]struct{})
			if row.DatasetID != "" {
				datasetID := safeInventoryIdentifier(row.DatasetID, "pending_retries.dataset_id", report)
				if datasetID != "invalid" {
					datasetIDs[datasetID] = struct{}{}
				}
			}
			for _, datasetID := range inventoryRetryTargetDatasetIDs(row.FailureTargetsJSON, row.TaskJSON, row.Frequency, row.TargetDataTime,
				row.FailureResultsJSON, spaceID, row.Status == "permanent_failed" && reportState == "pending",
				available["retry_receipt_current"], report) {
				datasetIDs[datasetID] = struct{}{}
			}
			orderedDatasetIDs := make([]string, 0, len(datasetIDs))
			for datasetID := range datasetIDs {
				orderedDatasetIDs = append(orderedDatasetIDs, datasetID)
			}
			sort.Strings(orderedDatasetIDs)
			if row.Status == "permanent_failed" && reportState == "pending" && len(orderedDatasetIDs) == 0 {
				report.IntegrityIssues["pending_failure_receipt_without_dataset_target"]++
			}
			retryRef := inventoryOpaqueRef(row.RetryKey)
			if retryRef == "" {
				report.UnknownStateRows["pending_retries.retry_key"]++
			}
			report.PendingRetries = append(report.PendingRetries, collectorPeriodInventoryPendingRetry{
				RetryRef:                retryRef,
				SourceBatchRef:          inventoryOpaqueRef(row.SourceBatchID),
				DatasetIDs:              orderedDatasetIDs,
				Frequency:               safeInventoryIdentifier(row.Frequency, "pending_retries.frequency", report),
				TargetDataTime:          safeInventoryTimestamp(row.TargetDataTime, "pending_retries.target_data_time", report),
				Status:                  status,
				FailureReportState:      reportState,
				FailureDeadlineExceeded: row.DeadlineExceeded != 0,
			})
		}
	}
	return nil
}

func inventoryOpaqueRef(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:12])
}

func inventoryRetryTargetDatasetIDs(raw, taskJSON, frequency, targetDataTime, resultsJSON, spaceID string, required, validateResults bool, report *collectorPeriodInventoryReport) []string {
	if !required {
		return nil
	}
	var item struct {
		Frequency      string `json:"frequency"`
		TargetDataTime string `json:"target_data_time"`
		MarketType     string `json:"market_type"`
	}
	taskValid := json.Unmarshal([]byte(taskJSON), &item) == nil
	if !taskValid {
		report.IntegrityIssues["pending_failure_receipt_invalid"]++
	}
	if strings.TrimSpace(frequency) == "" {
		frequency = item.Frequency
	}
	canonicalFrequency, frequencyValid := normalizedInventoryMarketFrequency(frequency)
	if !frequencyValid {
		report.IntegrityIssues["pending_failure_receipt_invalid"]++
	}
	periodTime, periodErr := parseInventoryTimestamp(targetDataTime)
	periodNeedsFallback := periodErr != nil && strings.TrimSpace(targetDataTime) == ""
	if periodErr == nil && periodTime.IsZero() {
		periodNeedsFallback = true
		periodErr = fmt.Errorf("retry target period is zero")
	}
	if periodNeedsFallback {
		periodTime, periodErr = time.Parse(time.RFC3339Nano, strings.TrimSpace(item.TargetDataTime))
		if periodErr == nil && periodTime.IsZero() {
			periodErr = fmt.Errorf("task target period is zero")
		}
	}
	if periodErr != nil || !supportedInventoryMarketType(item.MarketType) {
		report.IntegrityIssues["pending_failure_receipt_invalid"]++
	}
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "[]" {
		report.IntegrityIssues["pending_failure_targets_invalid"]++
		return nil
	}
	var targets []collectorPeriodInventoryFailureTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil || len(targets) == 0 {
		report.IntegrityIssues["pending_failure_targets_invalid"]++
		return nil
	}
	datasetIDs := make(map[string]struct{}, len(targets))
	targetByID := make(map[string]collectorPeriodInventoryFailureTarget, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.WriteTargetID)
		datasetID := strings.TrimSpace(target.DatasetID)
		if id == "" || target.WriteTargetID != id || (strings.TrimSpace(target.SpaceID) != "" && strings.TrimSpace(target.SpaceID) != spaceID) ||
			datasetID == "" || target.DatasetID != datasetID || strings.TrimSpace(target.SeriesHash) == "" ||
			target.SeriesHash != strings.TrimSpace(target.SeriesHash) || target.ExpectedCount == 0 || target.SeriesIndex >= target.ExpectedCount {
			report.IntegrityIssues["pending_failure_targets_invalid"]++
			continue
		}
		if _, exists := targetByID[id]; exists {
			report.IntegrityIssues["pending_failure_targets_invalid"]++
			continue
		}
		target.WriteTargetID = id
		target.DatasetID = datasetID
		targetByID[id] = target
		datasetIDs[datasetID] = struct{}{}
	}
	if validateResults {
		validateInventoryFailureResults(resultsJSON, targetByID, spaceID, canonicalFrequency, periodTime, frequencyValid && periodErr == nil, report)
	}
	result := make([]string, 0, len(datasetIDs))
	for datasetID := range datasetIDs {
		result = append(result, safeInventoryIdentifier(datasetID, "pending_retries.dataset_id", report))
	}
	sort.Strings(result)
	return result
}

func validateInventoryFailureResults(raw string, targets map[string]collectorPeriodInventoryFailureTarget, spaceID, frequency string, period time.Time, periodValid bool, report *collectorPeriodInventoryReport) {
	var results []collectorPeriodInventoryFailureResult
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "null" || json.Unmarshal([]byte(raw), &results) != nil {
		report.IntegrityIssues["pending_failure_receipt_results_invalid"]++
		return
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		id := result.WriteTargetID
		target, exists := targets[id]
		resultFrequency, frequencyValid := normalizedInventoryMarketFrequency(result.Frequency)
		resultPeriod, periodErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(result.PeriodTime))
		observedAt, observedErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(result.ObservedAt))
		if id == "" || id != strings.TrimSpace(id) || !exists || !periodValid || result.SpaceID != spaceID || result.DatasetID != target.DatasetID || !frequencyValid || resultFrequency != frequency ||
			periodErr != nil || resultPeriod.IsZero() || !resultPeriod.Equal(period) || result.SeriesHash != target.SeriesHash ||
			result.ExpectedCount != target.ExpectedCount || result.SeriesIndex != target.SeriesIndex || observedErr != nil || observedAt.IsZero() ||
			!safeInventoryState(result.Disposition, "recorded", "already_succeeded", "missed_deadline") {
			report.IntegrityIssues["pending_failure_receipt_results_invalid"]++
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			report.IntegrityIssues["pending_failure_receipt_results_invalid"]++
			continue
		}
		seen[id] = struct{}{}
	}
	if len(targets) > 0 && len(seen) == len(targets) {
		report.IntegrityIssues["pending_failure_receipt_results_invalid"]++
	}
}

func supportedInventoryMarketType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "spot", "swap", "equity", "stockcn":
		return true
	default:
		return false
	}
}

func safeInventoryIdentifier(value, metric string, report *collectorPeriodInventoryReport) string {
	if len(value) == 0 || len(value) > 256 || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		report.UnknownStateRows[metric]++
		return "invalid"
	}
	return value
}

func safeInventoryOptionalTimestamp(value, metric string, report *collectorPeriodInventoryReport) string {
	if value == "" {
		return ""
	}
	return safeInventoryTimestamp(value, metric, report)
}

func safeInventoryTimestamp(value, metric string, report *collectorPeriodInventoryReport) string {
	parsed, err := parseInventoryTimestamp(value)
	if err != nil {
		report.UnknownStateRows[metric]++
		return "invalid"
	}
	return parsed.UTC().Format(time.RFC3339Nano)
}

func parseInventoryTimestamp(value string) (time.Time, error) {
	var parsed time.Time
	var err error
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999",
	} {
		parsed, err = time.Parse(layout, value)
		if err == nil {
			break
		}
	}
	if err != nil {
		parsed, err = time.ParseInLocation("2006-01-02 15:04:05.999999999", value, time.UTC)
	}
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func collectCollectorPeriodInventoryKeys(ctx context.Context, tx *gorm.DB, spaceID string, limit int, report *collectorPeriodInventoryReport, available map[string]bool) error {
	for _, name := range []string{"t_collector_task_period_series", "t_period_readiness", "t_period_readiness_items"} {
		if !available[name] {
			return nil
		}
	}
	periodSources := []string{
		`SELECT c_dataset_id, c_frequency, c_period_time FROM t_collector_task_period_series WHERE c_space_id = ?`,
		`SELECT c_dataset_id, c_frequency, c_period_time FROM t_period_readiness WHERE c_space_id = ?`,
	}
	args := []any{spaceID, spaceID}
	if available["t_collector_period_storage_states"] {
		periodSources = append(periodSources, `SELECT c_dataset_id, c_frequency, c_period_time FROM t_collector_period_storage_states WHERE c_space_id = ?`)
		args = append(args, spaceID)
	}
	if available["t_collector_timer_period_batches"] {
		periodSources = append(periodSources, `SELECT c_dataset_id, c_frequency, c_period_time FROM t_collector_timer_period_batches WHERE c_space_id = ?`)
		args = append(args, spaceID)
	}
	storageCTE := `storage_states AS (
  SELECT c_dataset_id, c_frequency, c_period_time, c_series_hash, c_status, c_expected_count, c_deadline_at, c_confirmed_at
  FROM t_collector_period_storage_states WHERE c_space_id = ?
)`
	if !available["t_collector_period_storage_states"] {
		storageCTE = `storage_states AS (
  SELECT NULL AS c_dataset_id, NULL AS c_frequency, NULL AS c_period_time, NULL AS c_series_hash,
         NULL AS c_status, NULL AS c_expected_count, NULL AS c_deadline_at, NULL AS c_confirmed_at WHERE 0
)`
	}
	timerCTE := `timer_batches AS (
  SELECT c_dataset_id, c_frequency, c_period_time, count(*) AS batch_count,
    sum(CASE WHEN c_claim_request_id <> '' THEN 1 ELSE 0 END) AS claimed_count,
    min(c_deadline_at) AS deadline_min, max(c_deadline_at) AS deadline_max,
    min(c_series_hash) AS series_hash, min(c_expected_count) AS expected_min,
    max(c_expected_count) AS expected_max, count(DISTINCT c_series_hash) AS hash_variants,
    count(DISTINCT c_group_id) AS group_id_variants,
    count(DISTINCT c_group_count) AS group_count_variants,
    count(DISTINCT c_shard_index) AS shard_index_variants,
    sum(CASE WHEN c_group_id < 0 OR c_group_count <= 0 OR c_group_id >= c_group_count THEN 1 ELSE 0 END) AS invalid_group_rows,
    sum(CASE WHEN (c_claim_request_id = '' AND c_claimed_at IS NOT NULL) OR
      (c_claim_request_id <> '' AND c_claimed_at IS NULL) THEN 1 ELSE 0 END) AS claim_metadata_issues
  FROM t_collector_timer_period_batches WHERE c_space_id = ?
  GROUP BY c_dataset_id, c_frequency, c_period_time
)`
	if !available["t_collector_timer_period_batches"] {
		timerCTE = `timer_batches AS (
  SELECT NULL AS c_dataset_id, NULL AS c_frequency, NULL AS c_period_time, 0 AS batch_count,
    0 AS claimed_count, NULL AS deadline_min, NULL AS deadline_max, NULL AS series_hash,
    0 AS expected_min, 0 AS expected_max, 0 AS hash_variants, 0 AS group_id_variants,
    0 AS group_count_variants, 0 AS shard_index_variants, 0 AS invalid_group_rows,
    0 AS claim_metadata_issues WHERE 0
)`
	}
	args = append(args, spaceID)
	if available["t_collector_period_storage_states"] {
		args = append(args, spaceID)
	}
	if available["t_collector_timer_period_batches"] {
		args = append(args, spaceID)
	}
	args = append(args, spaceID, spaceID, limit+1)
	periods := make([]collectorPeriodInventoryPeriod, 0)
	query := fmt.Sprintf(`WITH period_keys AS (
  %s
), snapshots AS (
  SELECT c_dataset_id, c_frequency, c_period_time, count(*) AS series_rows,
    min(c_expected_count) AS expected_min, max(c_expected_count) AS expected_max,
    count(DISTINCT c_series_hash) AS hash_variants, min(c_series_hash) AS series_hash
  FROM t_collector_task_period_series WHERE c_space_id = ?
  GROUP BY c_dataset_id, c_frequency, c_period_time
), %s, %s, readiness AS (
  SELECT c_dataset_id, c_frequency, c_period_time, c_work_type, c_status, c_report_state, c_deadline_at
  FROM t_period_readiness WHERE c_space_id = ?
), readiness_items AS (
  SELECT r.c_dataset_id, r.c_frequency, r.c_period_time, count(i.c_readiness_id) AS item_count,
    sum(CASE WHEN i.c_state = 'pending' THEN 1 ELSE 0 END) AS pending_count,
    sum(CASE WHEN i.c_state = 'success' THEN 1 ELSE 0 END) AS success_count,
    sum(CASE WHEN i.c_state = 'timed_out' THEN 1 ELSE 0 END) AS timed_out_count
  FROM t_period_readiness r LEFT JOIN t_period_readiness_items i ON i.c_readiness_id = r.c_id
  WHERE r.c_space_id = ? GROUP BY r.c_dataset_id, r.c_frequency, r.c_period_time
)
SELECT k.c_dataset_id AS dataset_id, k.c_frequency AS frequency,
  k.c_period_time AS period_time, k.c_period_time AS raw_period_time,
  coalesce(s.series_rows, 0) AS snapshot_series_rows,
  coalesce(s.expected_min, 0) AS snapshot_expected_count_min,
  coalesce(s.expected_max, 0) AS snapshot_expected_count_max,
  coalesce(s.hash_variants, 0) AS snapshot_hash_variants,
  coalesce(s.series_hash, '') AS snapshot_series_hash,
  coalesce(st.c_status, '') AS storage_status,
  coalesce(st.c_expected_count, 0) AS storage_expected_count,
  coalesce(st.c_series_hash, '') AS storage_series_hash,
  coalesce(st.c_deadline_at, '') AS storage_deadline_at,
  coalesce(st.c_confirmed_at, '') AS storage_confirmed_at,
  CASE WHEN st.c_dataset_id IS NULL THEN 0 ELSE 1 END AS storage_state_present,
  coalesce(tb.batch_count, 0) AS timer_batch_count,
  coalesce(tb.claimed_count, 0) AS timer_claimed_batch_count,
  coalesce(tb.deadline_min, '') AS timer_deadline_min,
  coalesce(tb.deadline_max, '') AS timer_deadline_max,
  coalesce(tb.series_hash, '') AS timer_series_hash,
  coalesce(tb.expected_min, 0) AS timer_expected_count_min,
  coalesce(tb.expected_max, 0) AS timer_expected_count_max,
  coalesce(tb.hash_variants, 0) AS timer_hash_variants,
  coalesce(tb.group_id_variants, 0) AS timer_group_id_variants,
  coalesce(tb.group_count_variants, 0) AS timer_group_count_variants,
  coalesce(tb.shard_index_variants, 0) AS timer_shard_index_variants,
  coalesce(tb.invalid_group_rows, 0) AS timer_invalid_group_rows,
  coalesce(tb.claim_metadata_issues, 0) AS timer_claim_metadata_issues,
  coalesce(r.c_work_type, '') AS work_type,
  coalesce(r.c_status, '') AS readiness_status,
  coalesce(r.c_report_state, '') AS readiness_report_state,
  coalesce(r.c_deadline_at, '') AS readiness_deadline_at,
  coalesce(ri.item_count, 0) AS readiness_item_count,
  coalesce(ri.pending_count, 0) AS readiness_pending_item_count,
  coalesce(ri.success_count, 0) AS readiness_success_item_count,
  coalesce(ri.timed_out_count, 0) AS readiness_timed_out_item_count
FROM period_keys k
LEFT JOIN snapshots s USING (c_dataset_id, c_frequency, c_period_time)
LEFT JOIN storage_states st USING (c_dataset_id, c_frequency, c_period_time)
LEFT JOIN timer_batches tb USING (c_dataset_id, c_frequency, c_period_time)
LEFT JOIN readiness r USING (c_dataset_id, c_frequency, c_period_time)
LEFT JOIN readiness_items ri USING (c_dataset_id, c_frequency, c_period_time)
ORDER BY k.c_period_time, k.c_dataset_id, k.c_frequency
LIMIT ?`, strings.Join(periodSources, "\n  UNION "), storageCTE, timerCTE)
	if err := tx.WithContext(ctx).Raw(query, args...).Scan(&periods).Error; err != nil {
		return fmt.Errorf("list Collector period keys: %w", err)
	}
	if len(periods) > limit {
		report.PeriodKeysTruncated = true
		periods = periods[:limit]
		report.Complete = false
	}
	for i := range periods {
		periods[i].DatasetID = safeInventoryIdentifier(periods[i].DatasetID, "period_keys.dataset_id", report)
		periods[i].Frequency = safeInventoryIdentifier(periods[i].Frequency, "period_keys.frequency", report)
		periods[i].PeriodTime = safeInventoryTimestamp(periods[i].PeriodTime, "period_keys.period_time", report)
		periods[i].StorageDeadlineAt = safeInventoryOptionalTimestamp(periods[i].StorageDeadlineAt, "period_keys.storage_deadline_at", report)
		periods[i].StorageConfirmedAt = safeInventoryOptionalTimestamp(periods[i].StorageConfirmedAt, "period_keys.storage_confirmed_at", report)
		periods[i].TimerDeadlineMin = safeInventoryOptionalTimestamp(periods[i].TimerDeadlineMin, "period_keys.timer_deadline_min", report)
		periods[i].TimerDeadlineMax = safeInventoryOptionalTimestamp(periods[i].TimerDeadlineMax, "period_keys.timer_deadline_max", report)
		periods[i].ReadinessDeadlineAt = safeInventoryOptionalTimestamp(periods[i].ReadinessDeadlineAt, "period_keys.readiness_deadline_at", report)
		workType := periods[i].WorkType
		if workType == "" {
			workType = "collection"
		}
		if !safeInventoryState(workType, "collection", "resample") {
			report.UnknownStateRows["period_keys.work_type"]++
			workType = "unknown"
		}
		periods[i].WorkType = workType
		if !supportedPeriodInventoryFrequency(periods[i].Frequency, workType) {
			report.UnknownStateRows["period_keys.frequency"]++
			periods[i].Frequency = "invalid"
		}
		periods[i].SnapshotRequired = workType != "resample" || periods[i].StorageStatus != "" || periods[i].TimerBatchCount > 0
		periods[i].StorageStateValid = true
		if periods[i].StorageStatePresent {
			deadline, deadlineErr := parseInventoryTimestamp(periods[i].StorageDeadlineAt)
			confirmedAt, confirmedErr := parseInventoryTimestamp(periods[i].StorageConfirmedAt)
			periods[i].StorageStateValid = safeInventoryState(periods[i].StorageStatus, "waiting", "complete", "degraded") &&
				deadlineErr == nil && !deadline.IsZero() && confirmedErr == nil && !confirmedAt.IsZero()
		}
		periods[i].SnapshotValid = !periods[i].SnapshotRequired || (periods[i].SnapshotSeriesRows > 0 &&
			periods[i].SnapshotExpectedCountMin > 0 &&
			periods[i].SnapshotExpectedCountMin == periods[i].SnapshotExpectedCountMax &&
			periods[i].SnapshotSeriesRows == periods[i].SnapshotExpectedCountMin &&
			periods[i].SnapshotHashVariants == 1 && periods[i].SnapshotSeriesHash != "")
		if periods[i].SnapshotRequired && periods[i].StorageStatus != "" &&
			(periods[i].StorageExpectedCount != periods[i].SnapshotSeriesRows || periods[i].StorageSeriesHash != periods[i].SnapshotSeriesHash) {
			periods[i].SnapshotValid = false
		}
		if !periods[i].StorageStateValid {
			periods[i].SnapshotValid = false
		}
		periods[i].TimerManifestValid = periods[i].TimerBatchCount == 0
		if periods[i].TimerBatchCount > 0 {
			periods[i].TimerManifestValid = periods[i].SnapshotRequired && periods[i].SnapshotValid &&
				periods[i].TimerExpectedCountMin == periods[i].SnapshotSeriesRows &&
				periods[i].TimerExpectedCountMin == periods[i].TimerExpectedCountMax && periods[i].TimerHashVariants == 1 &&
				periods[i].TimerSeriesHash == periods[i].SnapshotSeriesHash &&
				periods[i].TimerGroupIDVariants == periods[i].TimerBatchCount && periods[i].TimerGroupCountVariants == 1 &&
				periods[i].TimerShardIndexVariants == periods[i].TimerBatchCount && periods[i].TimerInvalidGroupRows == 0 &&
				periods[i].TimerClaimMetadataIssues == 0
		}
		if !safeInventoryState(periods[i].StorageStatus, "", "waiting", "complete", "degraded") {
			report.UnknownStateRows["period_keys"]++
			report.Complete = false
			periods[i].StorageStatus = "unknown"
		}
		if !safeInventoryState(periods[i].ReadinessStatus, "", "waiting", "complete", "degraded") {
			report.UnknownStateRows["period_keys"]++
			report.Complete = false
			periods[i].ReadinessStatus = "unknown"
		}
		if !safeInventoryState(periods[i].ReadinessReportState, "", "waiting", "pending", "reported") {
			report.UnknownStateRows["period_keys"]++
			report.Complete = false
			periods[i].ReadinessReportState = "unknown"
		}
	}
	if !report.PeriodKeysTruncated {
		snapshotSubjects, err := validateCollectorPeriodInventorySnapshotContents(ctx, tx, spaceID, periods)
		if err != nil {
			return err
		}
		if available["t_collector_timer_period_batches"] {
			if err := validateCollectorPeriodInventoryTimerContents(ctx, tx, spaceID, periods, snapshotSubjects); err != nil {
				return err
			}
		}
	}
	for _, period := range periods {
		if !period.SnapshotValid {
			report.IntegrityIssues["period_snapshot_inconsistent"]++
		}
		if !period.StorageStateValid {
			report.IntegrityIssues["period_storage_state_inconsistent"]++
		}
		if !period.TimerManifestValid {
			report.IntegrityIssues["timer_period_manifest_inconsistent"]++
		}
	}
	report.PeriodKeys = periods
	return nil
}

type collectorPeriodInventorySnapshotSubject struct {
	SubjectID      string
	Provider       string
	SourceID       string
	MarketType     string
	ProviderSymbol string
}

type collectorPeriodInventoryRouteIdentity struct {
	Provider   string
	MarketType string
}

func validateCollectorPeriodInventorySnapshotContents(ctx context.Context, tx *gorm.DB, spaceID string, periods []collectorPeriodInventoryPeriod) (map[string]map[uint32]collectorPeriodInventorySnapshotSubject, error) {
	targets := make(map[string]int, len(periods))
	subjects := make(map[string]map[uint32]collectorPeriodInventorySnapshotSubject)
	timerTargets := make(map[string]struct{})
	for i := range periods {
		period := periods[i]
		if period.SnapshotSeriesRows == 0 || period.DatasetID == "invalid" || period.Frequency == "invalid" || period.RawPeriodTime == "" {
			continue
		}
		key := inventoryPeriodKey(period.DatasetID, period.Frequency, period.RawPeriodTime)
		targets[key] = i
		if period.TimerBatchCount > 0 {
			timerTargets[key] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return subjects, nil
	}
	query := `SELECT c_dataset_id, c_frequency, c_period_time, c_series_index, c_series_key,
  c_provider, c_source_id, c_market_type, c_subject_id, c_provider_symbol, c_series_tag, c_series_hash, c_expected_count
FROM t_collector_task_period_series WHERE c_space_id = ?
ORDER BY c_dataset_id, c_frequency, c_period_time, c_series_index`
	rows, err := tx.WithContext(ctx).Raw(query, spaceID).Rows()
	if err != nil {
		return nil, fmt.Errorf("scan Collector period snapshot integrity: %w", err)
	}
	defer rows.Close()
	var (
		currentKey    string
		rowCount      int64
		previousIndex int64
		previousKey   string
		expectedCount int64
		storedHash    string
		valid         bool
		digest        = sha256.New()
	)
	timerRouteIdentities := make(map[string]collectorPeriodInventoryRouteIdentity)
	finish := func() {
		index, ok := targets[currentKey]
		if !ok {
			return
		}
		period := &periods[index]
		actualHash := hex.EncodeToString(digest.Sum(nil))
		if !valid || rowCount != period.SnapshotSeriesRows || rowCount != expectedCount || actualHash != storedHash || storedHash != period.SnapshotSeriesHash {
			period.SnapshotValid = false
		}
	}
	for rows.Next() {
		var (
			datasetID, frequency, periodTime                                                            string
			seriesIndex, expected                                                                       int64
			seriesKey, provider, sourceID, marketType, subjectID, providerSymbol, seriesTag, seriesHash string
		)
		if err := rows.Scan(&datasetID, &frequency, &periodTime, &seriesIndex, &seriesKey,
			&provider, &sourceID, &marketType, &subjectID, &providerSymbol, &seriesTag, &seriesHash, &expected); err != nil {
			return nil, fmt.Errorf("scan Collector period snapshot integrity row: %w", err)
		}
		key := inventoryPeriodKey(datasetID, frequency, periodTime)
		if _, targeted := timerTargets[key]; targeted && seriesIndex >= 0 && seriesIndex <= int64(^uint32(0)) {
			if identity, exists := timerRouteIdentities[key]; exists {
				if identity.Provider != provider || identity.MarketType != marketType {
					periods[targets[key]].SnapshotValid = false
				}
			} else {
				timerRouteIdentities[key] = collectorPeriodInventoryRouteIdentity{Provider: provider, MarketType: marketType}
			}
			if subjects[key] == nil {
				subjects[key] = make(map[uint32]collectorPeriodInventorySnapshotSubject)
			}
			subjects[key][uint32(seriesIndex)] = collectorPeriodInventorySnapshotSubject{
				SubjectID: subjectID, Provider: provider, SourceID: sourceID, MarketType: marketType, ProviderSymbol: providerSymbol,
			}
		}
		if key != currentKey {
			finish()
			currentKey = key
			rowCount = 0
			previousIndex = -1
			previousKey = ""
			expectedCount = expected
			storedHash = seriesHash
			valid = true
			digest = sha256.New()
		}
		if !supportedInventoryFrequency(frequency) || strings.TrimSpace(provider) == "" || strings.TrimSpace(sourceID) == "" ||
			strings.TrimSpace(marketType) == "" || strings.TrimSpace(subjectID) == "" || strings.TrimSpace(providerSymbol) == "" ||
			expected <= 0 || expected != expectedCount || seriesHash != storedHash || seriesIndex != rowCount || (rowCount > 0 && seriesKey <= previousKey) ||
			seriesKey != canonicalInventorySeriesKey(provider, sourceID, marketType, subjectID, seriesTag) {
			valid = false
		}
		if rowCount > 0 && seriesIndex <= previousIndex {
			valid = false
		}
		_, _ = digest.Write([]byte(seriesKey))
		_, _ = digest.Write([]byte{0})
		rowCount++
		previousIndex = seriesIndex
		previousKey = seriesKey
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Collector period snapshot integrity rows: %w", err)
	}
	finish()
	return subjects, nil
}

func validateCollectorPeriodInventoryTimerContents(ctx context.Context, tx *gorm.DB, spaceID string, periods []collectorPeriodInventoryPeriod, snapshotSubjects map[string]map[uint32]collectorPeriodInventorySnapshotSubject) error {
	const periodChunkSize = 250
	targets := make(map[string]int)
	keys := make([]string, 0)
	for i := range periods {
		period := periods[i]
		if period.TimerBatchCount == 0 || period.DatasetID == "invalid" || period.Frequency == "invalid" || period.RawPeriodTime == "" {
			continue
		}
		key := inventoryPeriodKey(period.DatasetID, period.Frequency, period.RawPeriodTime)
		targets[key] = i
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	covered := make(map[string]map[uint32]int, len(keys))
	manifestRows := make(map[string]int64, len(keys))
	manifestOwners := make(map[string]collectorPeriodInventoryTimerOwner, len(keys))
	for start := 0; start < len(keys); start += periodChunkSize {
		end := start + periodChunkSize
		if end > len(keys) {
			end = len(keys)
		}
		values := make([]string, 0, end-start)
		args := make([]any, 0, (end-start)*3+1)
		for _, key := range keys[start:end] {
			period := periods[targets[key]]
			values = append(values, "(?, ?, ?)")
			args = append(args, period.DatasetID, period.Frequency, period.RawPeriodTime)
		}
		args = append(args, spaceID)
		// GORM may render the same SQLite DATETIME differently when binding it back.
		query := fmt.Sprintf(`WITH target_periods(c_dataset_id, c_frequency, c_period_time) AS (VALUES %s)
SELECT m.c_dataset_id, m.c_frequency, m.c_period_time, m.c_task_id, m.c_first_run_id, m.c_node_id, m.c_region,
  m.c_route_version, m.c_group_id, m.c_group_count,
  m.c_shard_index, m.c_series_hash, m.c_expected_count, m.c_binding_hash, m.c_batch_id,
  m.c_function_name AS manifest_function_name, m.c_claim_request_id AS claim_request_id,
  coalesce(m.c_claimed_at, '') AS claimed_at,
  coalesce(m.c_deadline_at, '') AS manifest_deadline_at,
  coalesce(b.c_schedule_id, '') AS batch_schedule_id, coalesce(b.c_batch_kind, '') AS batch_kind,
  coalesce(b.c_shard_index, -1) AS batch_shard_index, coalesce(b.c_frequency, '') AS batch_frequency,
  coalesce(b.c_planned_count, -1) AS batch_planned_count, coalesce(b.c_attempt, -1) AS batch_attempt,
  coalesce(b.c_deadline_at, '') AS batch_deadline_at, coalesce(b.c_dispatched_at, '') AS batch_dispatched_at,
  coalesce(b.c_parent_batch_id, '') AS batch_parent_batch_id, coalesce(b.c_instance_id, '') AS batch_instance_id,
  coalesce(b.c_write_target_id, '') AS batch_write_target_id, coalesce(b.c_retry_scope, '') AS batch_retry_scope,
  coalesce(b.c_status, '') AS batch_status, coalesce(b.c_node_id, '') AS batch_node_id,
  coalesce(b.c_region, '') AS batch_region, coalesce(b.c_function_name, '') AS batch_function_name,
  coalesce(b.c_request_id, '') AS batch_request_id,
  coalesce(b.c_request_json, '') AS request_json,
  coalesce((SELECT count(*) FROM t_collector_fetch_batch_items bi
    WHERE bi.c_space_id = m.c_space_id AND bi.c_batch_id = m.c_batch_id), 0) AS batch_item_count,
  coalesce((SELECT count(*) FROM json_each(CASE WHEN length(b.c_request_json) <= 262144 AND json_valid(b.c_request_json)
      THEN b.c_request_json ELSE '{"items":[],"targets":[]}' END, '$.items') item
    WHERE NOT EXISTS (SELECT 1 FROM t_collector_fetch_batch_items bi
      WHERE bi.c_space_id = m.c_space_id AND bi.c_batch_id = m.c_batch_id
        AND bi.c_instance_id = json_extract(item.value, '$.instance_id'))), 0) AS missing_request_items,
  coalesce((SELECT count(*) FROM json_each(CASE WHEN length(b.c_request_json) <= 262144 AND json_valid(b.c_request_json)
      THEN b.c_request_json ELSE '{"items":[],"targets":[]}' END, '$.targets') target
    WHERE NOT EXISTS (SELECT 1 FROM t_collector_instance_write_targets wt
      WHERE wt.c_space_id = m.c_space_id
        AND wt.c_write_target_id = json_extract(target.value, '$.write_target_id')
        AND wt.c_instance_id = json_extract(target.value, '$.instance_id')
        AND wt.c_task_id = json_extract(target.value, '$.task_id')
        AND wt.c_dataset_id = json_extract(target.value, '$.dataset_id')
        AND wt.c_series_index = json_extract(target.value, '$.series_index')
        AND wt.c_series_hash = json_extract(target.value, '$.series_hash')
        AND wt.c_expected_count = json_extract(target.value, '$.expected_count')
        AND wt.c_output_fields_json = json_extract(target.value, '$.output_fields_json'))), 0) AS missing_request_targets
FROM target_periods p
JOIN t_collector_timer_period_batches m ON m.c_dataset_id = p.c_dataset_id AND
  m.c_frequency = p.c_frequency AND julianday(m.c_period_time) = julianday(p.c_period_time)
LEFT JOIN t_collector_fetch_batches b ON b.c_space_id = m.c_space_id AND b.c_batch_id = m.c_batch_id
WHERE m.c_space_id = ?
ORDER BY m.c_dataset_id, m.c_frequency, m.c_period_time, m.c_shard_index`, strings.Join(values, ","))
		rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
		if err != nil {
			return fmt.Errorf("scan Collector Timer manifest contents: %w", err)
		}
		for rows.Next() {
			var (
				datasetID, frequency, periodTime, manifestTaskID, manifestFirstRunID, manifestNodeID, manifestRegion, manifestRouteVersion,
				seriesHash, bindingHash, batchID, manifestFunctionName, claimRequestID,
				claimedAt, manifestDeadline, batchScheduleID, batchKind, batchFrequency, batchDeadline, batchDispatchedAt,
				batchParentID, batchInstanceID, batchWriteTargetID, batchRetryScope, batchStatus, batchNodeID, batchRegion,
				batchFunctionName, batchRequestID, requestJSON string
				groupID, groupCount, shardIndex, expectedCount, batchShardIndex, batchPlannedCount,
				batchAttempt, batchItemCount, missingRequestItems, missingRequestTargets int64
			)
			if err := rows.Scan(&datasetID, &frequency, &periodTime, &manifestTaskID, &manifestFirstRunID, &manifestNodeID, &manifestRegion, &manifestRouteVersion,
				&groupID, &groupCount, &shardIndex,
				&seriesHash, &expectedCount, &bindingHash, &batchID, &manifestFunctionName, &claimRequestID,
				&claimedAt, &manifestDeadline, &batchScheduleID, &batchKind, &batchShardIndex, &batchFrequency, &batchPlannedCount,
				&batchAttempt, &batchDeadline, &batchDispatchedAt, &batchParentID, &batchInstanceID, &batchWriteTargetID,
				&batchRetryScope, &batchStatus, &batchNodeID,
				&batchRegion, &batchFunctionName, &batchRequestID, &requestJSON,
				&batchItemCount, &missingRequestItems, &missingRequestTargets); err != nil {
				rows.Close()
				return fmt.Errorf("scan Collector Timer manifest row: %w", err)
			}
			key := inventoryPeriodKey(datasetID, frequency, periodTime)
			periodIndex, ok := targets[key]
			if !ok {
				continue
			}
			period := &periods[periodIndex]
			manifestRows[key]++
			owner := collectorPeriodInventoryTimerOwner{TaskID: manifestTaskID, FirstRunID: manifestFirstRunID, RouteVersion: manifestRouteVersion}
			if previous, exists := manifestOwners[key]; exists && previous != owner {
				period.TimerManifestValid = false
			} else {
				manifestOwners[key] = owner
			}
			valid := period.TimerManifestValid && expectedCount > 0 && expectedCount <= int64(^uint32(0)) &&
				expectedCount == period.SnapshotSeriesRows &&
				seriesHash != "" && seriesHash == period.SnapshotSeriesHash && bindingHash != "" && batchID != "" &&
				strings.TrimSpace(manifestTaskID) != "" && strings.TrimSpace(manifestFirstRunID) != "" &&
				strings.TrimSpace(manifestRouteVersion) != ""
			request, requestErr := decodeCollectorPeriodInventoryTimerRequest([]byte(requestJSON))
			periodInstant, periodErr := parseInventoryTimestamp(periodTime)
			manifestDeadlineAt, manifestDeadlineErr := parseInventoryTimestamp(manifestDeadline)
			batchDeadlineAt, batchDeadlineErr := parseInventoryTimestamp(batchDeadline)
			storageDeadlineAt, storageDeadlineErr := parseInventoryTimestamp(period.StorageDeadlineAt)
			claimedAtTime, claimedAtErr := parseInventoryTimestamp(claimedAt)
			batchDispatchedAtTime, batchDispatchedAtErr := parseInventoryTimestamp(batchDispatchedAt)
			shard := strconv.FormatInt(shardIndex, 10)
			expectedBatchID := ""
			expectedSyncPointID := ""
			expectedScheduleID := ""
			if periodErr == nil && !periodInstant.IsZero() {
				periodIDTime := periodInstant.UTC().Format(time.RFC3339Nano)
				expectedBatchID = collectorPeriodInventoryStableID(spaceID, datasetID, frequency, periodIDTime, shard, "timer-initial")
				expectedSyncPointID = collectorPeriodInventoryStableID(spaceID, datasetID, frequency, periodIDTime, shard, "timer-sync-point")
				expectedScheduleID = "timer:" + collectorPeriodInventoryStableID(spaceID, datasetID, frequency, periodIDTime, shard)
			}
			claimMetadataValid := false
			if claimRequestID == "" {
				claimMetadataValid = claimedAt == "" && request.RequestID == "" && batchRequestID == "" && batchDispatchedAt == "" &&
					(batchStatus == "planned" || batchStatus == "failed" || batchStatus == "timed_out")
			} else {
				claimMetadataValid = claimRequestID == strings.TrimSpace(claimRequestID) && claimedAtErr == nil && !claimedAtTime.IsZero() &&
					batchDispatchedAtErr == nil && !batchDispatchedAtTime.IsZero() && claimedAtTime.Equal(batchDispatchedAtTime) &&
					manifestDeadlineErr == nil && claimedAtTime.Before(manifestDeadlineAt) &&
					batchRequestID == claimRequestID &&
					(batchStatus == "dispatched" || batchStatus == "succeeded" || batchStatus == "partial_failed" || batchStatus == "failed" || batchStatus == "timed_out")
			}
			if requestErr != nil || request.BatchID != batchID ||
				request.SpaceID != spaceID || request.DatasetID != datasetID || request.Frequency != frequency ||
				int64(request.GroupID) != groupID || int64(request.GroupCount) != groupCount || int64(request.ShardIndex) != shardIndex ||
				request.BindingHash != bindingHash || request.RouteVersion != manifestRouteVersion ||
				request.BatchID != expectedBatchID || request.SyncPointID != expectedSyncPointID || request.ScheduleID != expectedScheduleID ||
				request.ScheduleID != batchScheduleID || request.Frequency != batchFrequency || int64(request.ShardIndex) != batchShardIndex ||
				int64(len(request.Items)) != batchPlannedCount || int64(len(request.Items)) != batchItemCount || batchAttempt != 1 ||
				manifestDeadlineErr != nil || manifestDeadlineAt.IsZero() || batchDeadlineErr != nil || batchDeadlineAt.IsZero() ||
				(claimRequestID == "" && !manifestDeadlineAt.Equal(batchDeadlineAt)) ||
				(period.StorageStatePresent && (storageDeadlineErr != nil || !manifestDeadlineAt.Equal(storageDeadlineAt))) ||
				!claimMetadataValid || batchParentID != "" || batchInstanceID != "" || batchWriteTargetID != "" || batchRetryScope != "" ||
				missingRequestItems != 0 || missingRequestTargets != 0 || !request.RequirePeriodCommit || request.BatchKind != "realtime" ||
				batchKind != request.BatchKind || manifestTaskID == "" || manifestNodeID == "" || manifestRegion == "" ||
				request.NodeID != manifestNodeID || request.Region != manifestRegion ||
				batchNodeID != manifestNodeID || batchRegion != manifestRegion ||
				strings.TrimSpace(request.FunctionName) == "" || request.FunctionName != manifestFunctionName || request.FunctionName != batchFunctionName ||
				request.RequestID != claimRequestID || request.RequestID != batchRequestID ||
				groupID < 0 || groupCount <= 0 || groupID >= groupCount || shardIndex < 0 || len(request.Items) == 0 ||
				len(request.Items) > collectorPeriodInventoryTimerMaxItems ||
				len(request.Items) != len(request.Targets) || request.Concurrency < 0 || request.Concurrency > 64 {
				valid = false
			}
			items := make(map[string]collectorPeriodInventoryTimerItem, len(request.Items))
			itemSeries := make(map[uint32]struct{}, len(request.Items))
			routeProvider := ""
			bindingOutputFields := ""
			for itemIndex, item := range request.Items {
				itemTime, itemErr := parseInventoryRequestTimestamp(item.TargetDataTime)
				startTime, startErr := parseInventoryOptionalRequestTimestamp(item.StartTime)
				endTime, endErr := parseInventoryOptionalRequestTimestamp(item.EndTime)
				snapshotEntry, hasSnapshotSubject := snapshotSubjects[key][item.SeriesIndex]
				itemOutputFields := strings.Join(item.OutputFields, ",")
				if itemIndex == 0 {
					routeProvider = snapshotEntry.Provider
					bindingOutputFields = itemOutputFields
				} else if !strings.EqualFold(strings.TrimSpace(routeProvider), strings.TrimSpace(snapshotEntry.Provider)) || itemOutputFields != bindingOutputFields {
					valid = false
				}
				if strings.TrimSpace(item.InstanceID) == "" || item.InstanceID != strings.TrimSpace(item.InstanceID) ||
					strings.TrimSpace(item.SubjectID) == "" || item.DatasetID != datasetID || item.Frequency != frequency ||
					strings.TrimSpace(item.Symbol) == "" || strings.TrimSpace(item.Provider) == "" || strings.TrimSpace(item.SourceID) == "" || strings.TrimSpace(item.MarketType) == "" ||
					!strings.EqualFold(strings.TrimSpace(item.Provider), strings.TrimSpace(request.Provider)) ||
					!strings.EqualFold(strings.TrimSpace(item.SourceID), strings.TrimSpace(request.SourceID)) ||
					!strings.EqualFold(strings.TrimSpace(item.MarketType), strings.TrimSpace(request.MarketType)) ||
					!strings.EqualFold(strings.TrimSpace(item.MarketID), strings.TrimSpace(request.MarketID)) ||
					!strings.EqualFold(strings.TrimSpace(item.InstrumentType), strings.TrimSpace(request.InstrumentType)) ||
					!item.RequirePeriodCommit || item.SeriesHash != seriesHash || int64(item.ExpectedCount) != expectedCount ||
					int64(item.SeriesIndex) >= expectedCount || !hasSnapshotSubject ||
					item.SubjectID != snapshotEntry.SubjectID ||
					!strings.EqualFold(strings.TrimSpace(item.MarketType), strings.TrimSpace(snapshotEntry.MarketType)) ||
					item.Symbol != snapshotEntry.ProviderSymbol ||
					itemErr != nil || periodErr != nil || !itemTime.Equal(periodInstant) || startErr != nil || endErr != nil ||
					(!startTime.IsZero() && !endTime.IsZero() && !endTime.After(startTime)) ||
					item.CandidateIndex != 0 || strings.TrimSpace(item.SourceEventID) != "" || math.IsNaN(item.RateBudgetRatio) || math.IsInf(item.RateBudgetRatio, 0) ||
					item.RateBudgetRatio < 0 || item.RateBudgetRatio > 1 || item.BarLimit > 10 {
					valid = false
				}
				if _, exists := items[item.InstanceID]; exists {
					valid = false
				}
				if _, exists := itemSeries[item.SeriesIndex]; exists {
					valid = false
				}
				if item.InstanceID == "" {
					valid = false
				}
				items[item.InstanceID] = item
				itemSeries[item.SeriesIndex] = struct{}{}
			}
			if len(request.Items) == 0 || collectorPeriodInventoryTimerBindingHash(routeProvider, request.Provider, request.SourceID,
				manifestRouteVersion, request.GroupID, request.GroupCount, request.MarketType, request.MarketID,
				request.InstrumentType, request.DatasetID, request.Frequency, bindingOutputFields,
				request.NodeID, request.FunctionName, request.Region) != bindingHash {
				valid = false
			}
			targetIDs := make(map[string]struct{}, len(request.Targets))
			targetIdentities := make(map[string]struct{}, len(request.Targets))
			for _, target := range request.Targets {
				item, exists := items[target.InstanceID]
				targetTime, targetErr := parseInventoryRequestTimestamp(target.TargetDataTime)
				var targetOutputFields []string
				outputFieldsErr := json.Unmarshal([]byte(target.OutputFields), &targetOutputFields)
				if !exists || strings.TrimSpace(target.WriteTargetID) == "" || target.WriteTargetID != strings.TrimSpace(target.WriteTargetID) ||
					target.TaskID == "" || target.TaskID != strings.TrimSpace(target.TaskID) || target.TaskID != manifestTaskID ||
					target.SpaceID != spaceID || target.DatasetID != datasetID ||
					target.Frequency != frequency || target.SeriesHash != seriesHash || int64(target.ExpectedCount) != expectedCount ||
					int64(target.SeriesIndex) >= expectedCount || !targetTime.Equal(periodInstant) || targetErr != nil ||
					target.SeriesIndex != item.SeriesIndex || outputFieldsErr != nil ||
					strings.Join(targetOutputFields, ",") != strings.Join(item.OutputFields, ",") {
					valid = false
				}
				if _, exists := targetIDs[target.WriteTargetID]; exists {
					valid = false
				}
				targetIDs[target.WriteTargetID] = struct{}{}
				targetIdentity := target.InstanceID + "\x00" + target.TaskID + "\x00" + target.DatasetID
				if _, exists := targetIdentities[targetIdentity]; exists {
					valid = false
				}
				targetIdentities[targetIdentity] = struct{}{}
				if covered[key] == nil {
					covered[key] = make(map[uint32]int)
				}
				covered[key][target.SeriesIndex]++
			}
			if !valid {
				period.TimerManifestValid = false
			}
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("iterate Collector Timer manifest rows: %w", rowsErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Collector Timer manifest rows: %w", closeErr)
		}
	}
	for key, index := range targets {
		period := &periods[index]
		series := covered[key]
		if manifestRows[key] != period.TimerBatchCount || period.SnapshotExpectedCountMin <= 0 ||
			period.SnapshotExpectedCountMin > int64(^uint32(0)) ||
			int64(len(series)) != period.SnapshotExpectedCountMin {
			period.TimerManifestValid = false
			continue
		}
		for seriesIndex := uint32(0); seriesIndex < uint32(period.SnapshotExpectedCountMin); seriesIndex++ {
			if series[seriesIndex] != 1 {
				period.TimerManifestValid = false
				break
			}
		}
	}
	return nil
}

func decodeCollectorPeriodInventoryTimerRequest(raw []byte) (collectorPeriodInventoryTimerRequest, error) {
	if len(raw) == 0 || len(raw) > collectorPeriodInventoryMaxTimerRequestBytes {
		return collectorPeriodInventoryTimerRequest{}, fmt.Errorf("Timer request is empty or exceeds %d bytes", collectorPeriodInventoryMaxTimerRequestBytes)
	}
	var request collectorPeriodInventoryTimerRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return collectorPeriodInventoryTimerRequest{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return collectorPeriodInventoryTimerRequest{}, fmt.Errorf("Timer request has trailing JSON")
		}
		return collectorPeriodInventoryTimerRequest{}, err
	}
	return request, nil
}

func parseInventoryRequestTimestamp(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
}

func parseInventoryOptionalRequestTimestamp(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return parseInventoryRequestTimestamp(value)
}

func inventoryPeriodKey(datasetID, frequency, periodTime string) string {
	return datasetID + "\x00" + frequency + "\x00" + periodTime
}

func collectorPeriodInventoryStableID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(digest[:])[:32]
}

func collectorPeriodInventoryTimerBindingHash(routeProvider, provider, sourceID, routeVersion string, groupID, groupCount int,
	marketType, marketID, instrumentType, datasetID, frequency, outputFields, nodeID, functionName, region string) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(routeProvider)),
		strings.ToLower(strings.TrimSpace(firstNonEmpty(provider, routeProvider))),
		strings.ToLower(strings.TrimSpace(sourceID)), strings.TrimSpace(routeVersion), strconv.Itoa(groupID), strconv.Itoa(groupCount),
		strings.ToLower(strings.TrimSpace(marketType)), strings.ToLower(strings.TrimSpace(marketID)),
		strings.ToLower(strings.TrimSpace(instrumentType)), strings.TrimSpace(datasetID), strings.TrimSpace(frequency), outputFields,
		strings.TrimSpace(nodeID), strings.TrimSpace(functionName), strings.TrimSpace(region),
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])[:16]
}

func canonicalInventorySeriesKey(provider, sourceID, marketType, subjectID, seriesTag string) string {
	parts := []string{provider, sourceID, marketType, subjectID, seriesTag}
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, "\x00")
}

func supportedInventoryFrequency(value string) bool {
	_, ok := normalizedInventoryMarketFrequency(value)
	return ok
}

func normalizedInventoryMarketFrequency(value string) (string, bool) {
	raw := strings.TrimSpace(value)
	if raw == "1M" {
		return "1M", true
	}
	switch strings.ToLower(raw) {
	case "1m", "5m", "15m", "30m", "1h", "1d", "1w":
		return strings.ToLower(raw), true
	case "60m":
		return "1h", true
	default:
		return "", false
	}
}

func supportedPeriodInventoryFrequency(value, workType string) bool {
	switch workType {
	case "resample":
		return supportedResampleStorageFrequency(value)
	case "collection":
		return supportedInventoryFrequency(value)
	default:
		return false
	}
}

func supportedResampleStorageFrequency(value string) bool {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return false
	}
	unit := raw[len(raw)-1]
	countText := raw[:len(raw)-1]
	count, err := strconv.ParseInt(countText, 10, 64)
	if err != nil || count <= 0 || strconv.FormatInt(count, 10) != countText {
		return false
	}
	switch unit {
	case 'm':
		minutes := count
		return minutes <= 30*24*60 && minutes%60 != 0
	case 'H':
		return count <= 30*24 && count%24 != 0
	case 'D':
		return count <= 30
	default:
		return false
	}
}

func safeInventoryState(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
