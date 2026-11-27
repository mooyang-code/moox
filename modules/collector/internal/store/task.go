// Package store contains Collector persistence adapters.
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

type taskRow struct {
	ID              int                               `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID         string                            `gorm:"column:c_space_id"`
	TaskID          string                            `gorm:"column:c_task_id"`
	TaskName        string                            `gorm:"column:c_task_name"`
	Description     string                            `gorm:"column:c_description"`
	DataType        string                            `gorm:"column:c_data_type"`
	Ingest          string                            `gorm:"column:c_ingest"`
	DefinitionHash  string                            `gorm:"column:c_definition_hash"`
	CollectParams   string                            `gorm:"column:c_collect_params"`
	Enabled         bool                              `gorm:"column:c_enabled"`
	Creator         string                            `gorm:"column:c_creator"`
	PrepareState    domain.CollectionTaskPrepareState `gorm:"column:c_prepare_state"`
	LastError       string                            `gorm:"column:c_last_error"`
	ResultDatasetID string                            `gorm:"column:c_result_dataset_id"`
	CreateTime      int64                             `gorm:"column:c_ctime"`
	ModifyTime      int64                             `gorm:"column:c_mtime"`
}

func (taskRow) TableName() string { return "t_collector_tasks" }

func (row taskRow) domain() domain.CollectionTask {
	return domain.CollectionTask{
		ID:              row.ID,
		SpaceID:         row.SpaceID,
		TaskID:          row.TaskID,
		TaskName:        row.TaskName,
		Description:     row.Description,
		DataType:        row.DataType,
		Ingest:          row.Ingest,
		DefinitionHash:  row.DefinitionHash,
		CollectParams:   row.CollectParams,
		Enabled:         row.Enabled,
		Creator:         row.Creator,
		PrepareState:    row.PrepareState,
		LastError:       row.LastError,
		ResultDatasetID: row.ResultDatasetID,
		CreateTime:      fromUnixMilli(row.CreateTime),
		ModifyTime:      fromUnixMilli(row.ModifyTime),
	}
}

func newTaskRow(task domain.CollectionTask, ctime, mtime int64) taskRow {
	return taskRow{
		ID:              task.ID,
		SpaceID:         task.SpaceID,
		TaskID:          task.TaskID,
		TaskName:        task.TaskName,
		Description:     task.Description,
		DataType:        task.DataType,
		Ingest:          task.Ingest,
		DefinitionHash:  task.DefinitionHash,
		CollectParams:   task.CollectParams,
		Enabled:         task.Enabled,
		Creator:         task.Creator,
		PrepareState:    task.PrepareState,
		LastError:       task.LastError,
		ResultDatasetID: task.ResultDatasetID,
		CreateTime:      ctime,
		ModifyTime:      mtime,
	}
}

func tasksFromRows(rows []taskRow) []domain.CollectionTask {
	out := make([]domain.CollectionTask, len(rows))
	for i, row := range rows {
		out[i] = row.domain()
	}
	return out
}

const MaxEnabledTasks = 1000

const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

func normalizePage(page int, size int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	return page, size
}

// TaskFilter describes collection task list filters.
type TaskFilter struct {
	SpaceID  string
	DataType string
	Enabled  *bool
	TaskID   string
	Keyword  string
	Page     int
	PageSize int
}

// TaskRepository persists collection tasks.
type TaskRepository struct {
	db *gorm.DB
	// defaultIngest 按空间返回 K 线任务的默认采集方式，见 Options.DefaultIngest。
	defaultIngest func(spaceID string) string
}

// NewTaskRepository creates a repository. defaultIngest 可以为 nil。
func NewTaskRepository(db *gorm.DB, defaultIngest func(spaceID string) string) *TaskRepository {
	return &TaskRepository{db: db, defaultIngest: defaultIngest}
}

// ResolveIngest 定下任务入库的采集方式，是所有建任务路径（RPC、内置任务种子）共用的唯一入口：
// K 线任务没写时取空间的市场默认值，取值必须是三种之一；其他任务没有采集方式。
func (r *TaskRepository) ResolveIngest(task domain.CollectionTask) (string, error) {
	ingest := strings.ToLower(strings.TrimSpace(task.Ingest))
	if task.DataType != domain.DataTypeKline {
		if ingest != "" {
			return "", fmt.Errorf("只有 K 线采集任务有采集方式，%s 任务不能指定 ingest", task.DataType)
		}
		return "", nil
	}
	if ingest == "" && r.defaultIngest != nil {
		ingest = r.defaultIngest(task.SpaceID)
	}
	if ingest == "" {
		return "", fmt.Errorf("空间 %s 没有登记默认采集方式，K 线任务要写明 ingest（stream、scf 或 local）", task.SpaceID)
	}
	if err := domain.ValidateIngest(ingest); err != nil {
		return "", err
	}
	return ingest, nil
}

// List returns tasks matching filters.
func (r *TaskRepository) List(ctx context.Context, filter TaskFilter) ([]domain.CollectionTask, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&taskRow{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var rows []taskRow
	if err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// ResultDatasetNames 返回空间内每个任务结果数据集的展示名（即任务名），键为结果数据集 ID。
func (r *TaskRepository) ResultDatasetNames(ctx context.Context, spaceID string) (map[string]string, error) {
	type row struct {
		DatasetID string `gorm:"column:c_result_dataset_id"`
		Name      string `gorm:"column:c_task_name"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).Model(&taskRow{}).Select("c_result_dataset_id, c_task_name").
		Where("c_space_id = ? AND c_result_dataset_id <> ''", spaceID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	names := make(map[string]string, len(rows))
	for _, item := range rows {
		names[item.DatasetID] = item.Name
	}
	return names, nil
}

// ListEnabled returns enabled tasks in one space.
func (r *TaskRepository) ListEnabled(ctx context.Context, spaceID string) ([]domain.CollectionTask, error) {
	var rows []taskRow
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_enabled = ?", strings.TrimSpace(spaceID), true).
		Order("c_id ASC").
		Limit(MaxEnabledTasks + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", MaxEnabledTasks)
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListEnabledResample 返回全部空间中已启用且已就绪的 K 线重采样任务（按空间与 ID 升序）。
func (r *TaskRepository) ListEnabledResample(ctx context.Context) ([]domain.CollectionTask, error) {
	var rows []taskRow
	err := r.db.WithContext(ctx).
		Where("c_data_type = ? AND c_enabled = ? AND c_prepare_state = ?", "kline_resample", true, domain.PrepareStateReady).
		Order("c_space_id ASC, c_id ASC").
		Limit(MaxEnabledTasks + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled resample task count exceeds limit %d", MaxEnabledTasks)
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// EnabledSpaces 返回有启用任务的空间。
func (r *TaskRepository) EnabledSpaces(ctx context.Context) ([]string, error) {
	var spaces []string
	err := r.db.WithContext(ctx).Model(&taskRow{}).
		Where("c_enabled = ?", true).
		Distinct("c_space_id").
		Pluck("c_space_id", &spaces).Error
	return spaces, err
}

// ListEnabledAll returns the complete enabled task inventory. It fails rather
// than returning a truncated snapshot because observability reconciliation must
// never publish a partial expected set.
func (r *TaskRepository) ListEnabledAll(ctx context.Context, limit int) ([]domain.CollectionTask, error) {
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("enabled task limit must be between 1 and %d", MaxEnabledTasks)
	}
	var rows []taskRow
	if err := r.db.WithContext(ctx).
		Where("c_enabled = ?", true).
		Order("c_id ASC").
		Limit(limit + 1).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, fmt.Errorf("enabled task count exceeds limit %d", limit)
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// ListKlineResultTasks returns the bounded, authoritative task-result
// inventory source. Disabled tasks are included so consumers can resolve
// freshness checks; deleted tasks are represented by absence from the next
// complete inventory snapshot.
func (r *TaskRepository) ListKlineResultTasks(ctx context.Context, spaceID string, limit int) ([]domain.CollectionTask, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return nil, fmt.Errorf("kline task inventory space_id is required")
	}
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("kline task inventory limit must be between 1 and %d", MaxEnabledTasks)
	}
	var rows []taskRow
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_data_type IN ?", spaceID, []string{"kline", "kline_resample"}).
		Order("c_id ASC").
		Limit(limit + 1).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, fmt.Errorf("kline task inventory exceeds limit %d", limit)
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetByTaskID returns a task by its business id within a space.
func (r *TaskRepository) GetByTaskID(ctx context.Context, spaceID string, taskID string) (*domain.CollectionTask, error) {
	var row taskRow
	q := r.db.WithContext(ctx).Where("c_task_id = ?", taskID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	if err := q.First(&row).Error; err != nil {
		return nil, err
	}
	tasks := []domain.CollectionTask{row.domain()}
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return &tasks[0], nil
}

// GetByTaskName returns a task by its display name within a space.
func (r *TaskRepository) GetByTaskName(ctx context.Context, spaceID string, taskName string) (*domain.CollectionTask, error) {
	var row taskRow
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_name = ?", strings.TrimSpace(spaceID), domain.NormalizeCollectionTaskName(taskName)).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	tasks := []domain.CollectionTask{row.domain()}
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return &tasks[0], nil
}

// GetByDefinitionHash returns the task that has the same normalized
// collection definition in a space. It is used before provisioning result
// metadata so duplicate requests cannot leave orphan datasets or views.
func (r *TaskRepository) GetByDefinitionHash(ctx context.Context, spaceID, definitionHash string) (*domain.CollectionTask, error) {
	var row taskRow
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_definition_hash = ?", strings.TrimSpace(spaceID), strings.TrimSpace(definitionHash)).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	tasks := []domain.CollectionTask{row.domain()}
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return &tasks[0], nil
}

// Create inserts a new collection task.
func (r *TaskRepository) Create(ctx context.Context, task domain.CollectionTask) error {
	now := nowUnixMilli()
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if task.TaskName != "" {
		if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
			return err
		}
	}
	if task.TaskName == "" {
		task.TaskName = task.TaskID
	}
	if task.PrepareState == "" {
		task.PrepareState = domain.PrepareStateReady
	}
	ingest, err := r.ResolveIngest(task)
	if err != nil {
		return err
	}
	task.Ingest = ingest
	tags := normalizeTaskTagIDs(task.TagIDs)
	row := newTaskRow(task, now, now)
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateTaskResultIdentityConflict(ctx, tx, task, ""); err != nil {
			return err
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return insertTaskTags(tx, task.SpaceID, task.TaskID, tags)
	})
}

// ListKline 返回空间的全部 K 线采集任务（含停用的），按任务 ID 排序；datasetID 非空时只返回写这个结果数据集的任务。
func (r *TaskRepository) ListKline(ctx context.Context, spaceID, datasetID string) ([]domain.CollectionTask, error) {
	query := r.db.WithContext(ctx).Where("c_space_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), domain.DataTypeKline)
	if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
		query = query.Where("c_result_dataset_id = ?", datasetID)
	}
	var rows []taskRow
	if err := query.Order("c_task_id ASC").Limit(MaxEnabledTasks).Find(&rows).Error; err != nil {
		return nil, err
	}
	tasks := tasksFromRows(rows)
	// 标签不在任务表里（gorm:"-"），补采路由要靠它推出来源与市场。
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// SetIngest 切换 K 线任务的采集方式：datasetID 为空时改空间的全部 K 线任务，否则改写这个结果数据集的任务。
// 返回改到的任务数。采集方式不进定义哈希，切换不改变任务身份。
func (r *TaskRepository) SetIngest(ctx context.Context, spaceID, datasetID, ingest string) (int64, error) {
	if err := domain.ValidateIngest(ingest); err != nil {
		return 0, err
	}
	query := r.db.WithContext(ctx).Model(&taskRow{}).
		Where("c_space_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), domain.DataTypeKline)
	if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
		query = query.Where("c_result_dataset_id = ?", datasetID)
	}
	result := query.Updates(map[string]any{"c_ingest": ingest, "c_mtime": nowUnixMilli()})
	return result.RowsAffected, result.Error
}

// ListResampleByPrepareStates returns bounded preparation work in stable order.
func (r *TaskRepository) ListResampleByPrepareStates(ctx context.Context, states []domain.CollectionTaskPrepareState, limit int) ([]domain.CollectionTask, error) {
	if limit <= 0 || limit > MaxEnabledTasks {
		return nil, fmt.Errorf("resample prepare task limit must be between 1 and %d", MaxEnabledTasks)
	}
	values := make([]string, 0, len(states))
	for _, state := range states {
		if !state.Valid() {
			return nil, fmt.Errorf("invalid task prepare state: %s", state)
		}
		values = append(values, string(state))
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one task prepare state is required")
	}
	var rows []taskRow
	err := r.db.WithContext(ctx).
		Where("c_data_type = ? AND c_enabled = ? AND c_prepare_state IN ?", "kline_resample", true, values).
		// Always service tasks that are not ready before refreshing already-ready
		// catalogs. Otherwise a stable prefix of ready tasks can starve new or
		// failed tasks once the inventory exceeds the per-tick bound.
		Order("CASE WHEN c_prepare_state = 'ready' THEN 1 ELSE 0 END ASC, c_mtime ASC, c_id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	tasks := tasksFromRows(rows)
	if err := r.attachTaskTags(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// SetPrepareState advances asynchronous target preparation without changing
// the immutable task definition.
func (r *TaskRepository) SetPrepareState(ctx context.Context, spaceID, taskID string, state domain.CollectionTaskPrepareState, lastError string) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(taskID) == "" {
		return fmt.Errorf("space_id and task_id are required")
	}
	if !state.Valid() {
		return fmt.Errorf("invalid collection task prepare state: %s", state)
	}
	result := r.db.WithContext(ctx).Model(&taskRow{}).
		Where("c_space_id = ? AND c_task_id = ? AND c_data_type = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID), "kline_resample").
		Updates(map[string]any{"c_prepare_state": state, "c_last_error": strings.TrimSpace(lastError), "c_mtime": nowUnixMilli()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

type taskTagRow struct {
	SpaceID string `gorm:"column:c_space_id"`
	TaskID  string `gorm:"column:c_task_id"`
	TagID   string `gorm:"column:c_tag_id"`
}

func normalizeTaskTagIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func insertTaskTags(tx *gorm.DB, spaceID, taskID string, tagIDs []string) error {
	for _, tagID := range normalizeTaskTagIDs(tagIDs) {
		if err := tx.Table("t_collector_task_tags").Create(map[string]any{
			"c_space_id": strings.TrimSpace(spaceID),
			"c_task_id":  strings.TrimSpace(taskID),
			"c_tag_id":   tagID,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *TaskRepository) attachTaskTags(ctx context.Context, tasks []domain.CollectionTask) error {
	if len(tasks) == 0 {
		return nil
	}
	taskIDs := make([]string, 0, len(tasks))
	seen := make(map[string]struct{}, len(tasks))
	for i := range tasks {
		tasks[i].TagIDs = nil
		if _, ok := seen[tasks[i].TaskID]; ok {
			continue
		}
		seen[tasks[i].TaskID] = struct{}{}
		taskIDs = append(taskIDs, tasks[i].TaskID)
	}
	var rows []taskTagRow
	if err := r.db.WithContext(ctx).Table("t_collector_task_tags").
		Select("c_space_id, c_task_id, c_tag_id").
		Where("c_task_id IN ?", taskIDs).
		Order("c_space_id, c_task_id, c_tag_id").
		Find(&rows).Error; err != nil {
		return err
	}
	byTask := make(map[string][]string, len(tasks))
	for _, row := range rows {
		key := row.SpaceID + "\x00" + row.TaskID
		byTask[key] = append(byTask[key], row.TagID)
	}
	for i := range tasks {
		key := tasks[i].SpaceID + "\x00" + tasks[i].TaskID
		tasks[i].TagIDs = append([]string(nil), byTask[key]...)
	}
	return nil
}

// UpdateByTaskID updates an existing collection task.
func (r *TaskRepository) UpdateByTaskID(ctx context.Context, spaceID string, taskID string, task domain.CollectionTask) (*domain.CollectionTask, error) {
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if task.TaskName != "" {
		if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(task.ResultDatasetID) == "" || strings.TrimSpace(task.TaskName) == "" {
		existing, err := r.GetByTaskID(ctx, spaceID, taskID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(task.ResultDatasetID) == "" {
			task.ResultDatasetID = existing.ResultDatasetID
		}
		if strings.TrimSpace(task.TaskName) == "" {
			task.TaskName = existing.TaskName
		}
	}
	now := nowUnixMilli()
	updates := map[string]any{
		"c_space_id":          task.SpaceID,
		"c_task_name":         task.TaskName,
		"c_description":       task.Description,
		"c_data_type":         task.DataType,
		"c_collect_params":    task.CollectParams,
		"c_enabled":           task.Enabled,
		"c_creator":           task.Creator,
		"c_result_dataset_id": task.ResultDatasetID,
		"c_mtime":             now,
	}
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateTaskResultIdentityConflict(ctx, tx, task, taskID); err != nil {
			return err
		}
		q := tx.Model(&taskRow{}).Where("c_task_id = ?", taskID)
		if strings.TrimSpace(spaceID) != "" {
			q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
		}
		return q.Updates(updates).Error
	}); err != nil {
		return nil, err
	}
	return r.GetByTaskID(ctx, spaceID, taskID)
}

// validateTaskResultIdentityConflict enforces that a collection task owns its
// result exclusively: no other task may write the same Dataset.
func validateTaskResultIdentityConflict(ctx context.Context, tx *gorm.DB, task domain.CollectionTask, excludeTaskID string) error {
	datasetID := strings.TrimSpace(task.ResultDatasetID)
	if datasetID == "" {
		return nil
	}
	query := tx.WithContext(ctx).Where("c_space_id = ?", strings.TrimSpace(task.SpaceID))
	if strings.TrimSpace(excludeTaskID) != "" {
		query = query.Where("c_task_id <> ?", strings.TrimSpace(excludeTaskID))
	}
	var conflict taskRow
	err := query.Where("c_result_dataset_id = ?", datasetID).Limit(1).Find(&conflict).Error
	if err != nil {
		return fmt.Errorf("check task result identity: %w", err)
	}
	if conflict.TaskID != "" {
		return fmt.Errorf("task result dataset %s conflicts with task %s/%s", datasetID, conflict.SpaceID, conflict.TaskID)
	}
	return nil
}

// UpdateMutableByTaskID updates only public mutable task fields. Task identity,
// collection configuration, result identities, and preparer-owned runtime
// state are intentionally absent from the update set.
func (r *TaskRepository) UpdateMutableByTaskID(ctx context.Context, spaceID, taskID string, task domain.CollectionTask) (*domain.CollectionTask, error) {
	return r.updateMutableByTaskID(ctx, spaceID, taskID, task, false)
}

// UpdateValidatedTaskByTaskID is used after the RPC layer has validated that
// the collection definition is semantically unchanged. It also persists the
// canonical collect_params in the same SQL update.
func (r *TaskRepository) UpdateValidatedTaskByTaskID(ctx context.Context, spaceID, taskID string, task domain.CollectionTask) (*domain.CollectionTask, error) {
	return r.updateMutableByTaskID(ctx, spaceID, taskID, task, true)
}

func (r *TaskRepository) updateMutableByTaskID(ctx context.Context, spaceID, taskID string, task domain.CollectionTask, persistCollectParams bool) (*domain.CollectionTask, error) {
	task.TaskName = domain.NormalizeCollectionTaskName(task.TaskName)
	if err := domain.ValidateCollectionTaskName(task.TaskName); err != nil {
		return nil, err
	}
	if !domain.CollectionTaskPrepareState(task.PrepareState).Valid() {
		return nil, fmt.Errorf("invalid collection task prepare state: %s", task.PrepareState)
	}
	now := nowUnixMilli()
	updates := map[string]any{
		"c_task_name":   task.TaskName,
		"c_description": task.Description,
		"c_enabled":     task.Enabled,
		"c_mtime":       now,
	}
	if persistCollectParams {
		updates["c_collect_params"] = task.CollectParams
		updates["c_definition_hash"] = task.DefinitionHash
	}
	query := r.db.WithContext(ctx).Model(&taskRow{}).
		Where("c_space_id = ? AND c_task_id = ?", strings.TrimSpace(spaceID), strings.TrimSpace(taskID))
	result := query.Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	return r.GetByTaskID(ctx, spaceID, taskID)
}

// SetEnabled changes a task enabled flag.
func (r *TaskRepository) SetEnabled(ctx context.Context, spaceID, taskID string, enabled bool) error {
	updates := map[string]any{"c_enabled": enabled, "c_mtime": nowUnixMilli()}
	q := r.db.WithContext(ctx).Model(&taskRow{}).Where("c_task_id = ?", taskID)
	if strings.TrimSpace(spaceID) != "" {
		q = q.Where("c_space_id = ?", strings.TrimSpace(spaceID))
	}
	return q.Updates(updates).Error
}

func (r *TaskRepository) DeleteByTaskID(ctx context.Context, spaceID, taskID string) error {
	spaceID, taskID = strings.TrimSpace(spaceID), strings.TrimSpace(taskID)
	result := r.db.WithContext(ctx).Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).Delete(&taskRow{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *TaskRepository) applyFilter(q *gorm.DB, filter TaskFilter) *gorm.DB {
	if v := strings.TrimSpace(filter.SpaceID); v != "" {
		q = q.Where("c_space_id = ?", v)
	}
	if v := strings.TrimSpace(filter.DataType); v != "" {
		q = q.Where("c_data_type = ?", v)
	}
	if filter.Enabled != nil {
		q = q.Where("c_enabled = ?", *filter.Enabled)
	}
	if v := strings.TrimSpace(filter.TaskID); v != "" {
		q = q.Where("c_task_id = ?", v)
	}
	if v := strings.ToLower(strings.TrimSpace(filter.Keyword)); v != "" {
		pattern := "%" + escapeLikePattern(v) + "%"
		q = q.Where("(LOWER(c_task_name) LIKE ? ESCAPE '\\' OR LOWER(c_task_id) LIKE ? ESCAPE '\\')", pattern, pattern)
	}
	return q
}

func escapeLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
