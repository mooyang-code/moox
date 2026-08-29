package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultPageSize = 50
	maxPageSize     = 1000
)

// TaskInstanceFilter describes task instance list filters.
type TaskInstanceFilter struct {
	SpaceID        string
	TaskID         string
	RuleID         string
	Provider       string
	MarketType     string
	DataType       string
	DatasetID      string
	SubjectID      string
	Frequency      string
	FunctionName   string
	LastExecNode   string
	LastExecStatus *int
	IncludeDeleted bool
	Page           int
	PageSize       int
}

// TaskInstanceRepository persists executable task instances.
type TaskInstanceRepository struct {
	db *gorm.DB
}

// StorageWriteObservation is a successful time-series write observed from a
// Storage change event. FunctionName is the current SCF assignment, while At
// is the event envelope time rather than the market bar's data time.
type StorageWriteObservation struct {
	SpaceID      string
	DatasetID    string
	SubjectID    string
	Frequency    string
	FunctionName string
	At           time.Time
}

// MarketFetchAssignment is one atomic SCF-to-subject binding update.
type MarketFetchAssignment struct {
	Provider     string
	MarketType   string
	DatasetID    string
	Frequency    string
	FunctionName string
	Subjects     []string
}

// NewTaskInstanceRepository creates a repository.
func NewTaskInstanceRepository(db *gorm.DB) *TaskInstanceRepository {
	return &TaskInstanceRepository{db: db}
}

// Get returns the current task instance by its stable identity.
func (r *TaskInstanceRepository) Get(ctx context.Context, spaceID, taskID string) (domain.TaskInstance, error) {
	var instance domain.TaskInstance
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_id = ?", spaceID, taskID).
		First(&instance).Error
	return instance, err
}

// List returns task instances matching filters.
func (r *TaskInstanceRepository) List(ctx context.Context, filter TaskInstanceFilter) ([]domain.TaskInstance, int64, error) {
	q := r.applyFilter(r.db.WithContext(ctx).Model(&domain.TaskInstance{}), filter)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	page, size := normalizePage(filter.Page, filter.PageSize)
	var instances []domain.TaskInstance
	err := q.Order("c_id DESC").Limit(size).Offset((page - 1) * size).Find(&instances).Error
	return instances, total, err
}

// UpsertMany creates or updates stable business instances. A periodic batch
// never changes the instance identity or resets its freshness state.
func (r *TaskInstanceRepository) UpsertMany(ctx context.Context, instances []domain.TaskInstance) error {
	if len(instances) == 0 {
		return nil
	}
	spaceID := instances[0].SpaceID
	taskIDs := make([]string, 0, len(instances))
	for _, instance := range instances {
		if instance.SpaceID != spaceID {
			return fmt.Errorf("task instances must belong to one space")
		}
		taskIDs = append(taskIDs, instance.TaskID)
	}
	var existingRows []domain.TaskInstance
	if err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_task_id IN ?", spaceID, taskIDs).
		Find(&existingRows).Error; err != nil {
		return err
	}
	existing := make(map[string]domain.TaskInstance, len(existingRows))
	for _, instance := range existingRows {
		existing[instance.TaskID] = instance
	}
	changed := make([]domain.TaskInstance, 0, len(instances))
	for _, instance := range instances {
		current, found := existing[instance.TaskID]
		if !found || taskInstanceDefinitionChanged(current, instance) {
			changed = append(changed, instance)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	now := time.Now().UTC()
	for i := range changed {
		if changed[i].CreateTime.IsZero() {
			changed[i].CreateTime = now
		}
		if changed[i].LastExecStatus == 0 {
			changed[i].LastExecStatus = domain.InstanceStatusPending
		}
		if strings.EqualFold(changed[i].DataType, "kline_resample") && (strings.TrimSpace(changed[i].Result) == "" || strings.TrimSpace(changed[i].Result) == "{}") {
			initial := domain.NewResampleTaskResult(time.Time{})
			encoded, err := initial.Marshal()
			if err != nil {
				return err
			}
			changed[i].Result = encoded
		}
		changed[i].ModifyTime = now
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_task_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"c_rule_id":     clause.Expr{SQL: "excluded.c_rule_id"},
			"c_provider":    clause.Expr{SQL: "excluded.c_provider"},
			"c_market_type": clause.Expr{SQL: "excluded.c_market_type"},
			"c_data_type":   clause.Expr{SQL: "excluded.c_data_type"},
			"c_dataset_id":  clause.Expr{SQL: "excluded.c_dataset_id"},
			"c_subject_id":  clause.Expr{SQL: "excluded.c_subject_id"},
			"c_frequency":   clause.Expr{SQL: "excluded.c_frequency"},
			"c_task_params": clause.Expr{SQL: "excluded.c_task_params"},
			"c_is_deleted":  clause.Expr{SQL: "excluded.c_is_deleted"},
			"c_mtime":       clause.Expr{SQL: "excluded.c_mtime"},
		}),
	}).Create(&changed).Error
}

// ClearMarketFetchAssignments removes the current SCF assignment before a
// fresh deterministic assignment is persisted. Execution history is kept.
func (r *TaskInstanceRepository) ClearMarketFetchAssignments(ctx context.Context, spaceID string, functionNames []string) error {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	query := r.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_is_deleted = ? AND c_function_name <> ''", spaceID, false)
	if len(functionNames) > 0 {
		query = query.Where("c_function_name IN ?", functionNames)
	}
	return query.Updates(map[string]any{"c_function_name": "", "c_mtime": time.Now().UTC()}).Error
}

// ReplaceMarketFetchAssignments atomically replaces the bindings owned by the
// listed functions. The completion consumer never observes the transient empty
// state between clearing old bindings and assigning the new snapshot.
func (r *TaskInstanceRepository) ReplaceMarketFetchAssignments(ctx context.Context, spaceID string, functionNames []string, assignments []MarketFetchAssignment) error {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return fmt.Errorf("space_id is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Model(&domain.TaskInstance{}).
			Where("c_space_id = ? AND c_is_deleted = ? AND c_function_name <> ''", spaceID, false)
		if len(functionNames) > 0 {
			query = query.Where("c_function_name IN ?", functionNames)
		}
		if err := query.Updates(map[string]any{"c_function_name": "", "c_mtime": time.Now().UTC()}).Error; err != nil {
			return err
		}
		for _, assignment := range assignments {
			functionName := strings.TrimSpace(assignment.FunctionName)
			if strings.TrimSpace(assignment.DatasetID) == "" || strings.TrimSpace(assignment.Frequency) == "" || functionName == "" {
				return fmt.Errorf("dataset_id, frequency and function_name are required")
			}
			if len(assignment.Subjects) == 0 {
				continue
			}
			if err := tx.Model(&domain.TaskInstance{}).
				Where("c_space_id = ? AND c_provider = ? AND c_market_type = ? AND c_data_type = ? AND c_dataset_id = ? AND c_frequency IN ? AND c_subject_id IN ? AND c_is_deleted = ?", spaceID, strings.TrimSpace(assignment.Provider), strings.TrimSpace(assignment.MarketType), "kline", strings.TrimSpace(assignment.DatasetID), frequencyVariants(assignment.Frequency), assignment.Subjects, false).
				Where("c_function_name <> ?", functionName).
				Updates(map[string]any{"c_function_name": functionName, "c_mtime": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// AssignMarketFetchFunction binds all subjects in one timer assignment to the
// current SCF function. The stable business task identity is unchanged when a
// subject moves to another function.
func (r *TaskInstanceRepository) AssignMarketFetchFunction(ctx context.Context, spaceID, provider, marketType, datasetID, frequency, functionName string, subjects []string) error {
	spaceID = strings.TrimSpace(spaceID)
	functionName = strings.TrimSpace(functionName)
	if spaceID == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(frequency) == "" || functionName == "" {
		return fmt.Errorf("space_id, dataset_id, frequency and function_name are required")
	}
	if len(subjects) == 0 {
		return nil
	}
	frequencyValues := frequencyVariants(frequency)
	return r.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_provider = ? AND c_market_type = ? AND c_data_type = ? AND c_dataset_id = ? AND c_frequency IN ? AND c_subject_id IN ? AND c_is_deleted = ?", spaceID, strings.TrimSpace(provider), strings.TrimSpace(marketType), "kline", strings.TrimSpace(datasetID), frequencyValues, subjects, false).
		Where("c_function_name <> ?", functionName).
		Updates(map[string]any{"c_function_name": functionName, "c_mtime": time.Now().UTC()}).Error
}

// MarkStorageWrites updates task freshness after Storage has committed the
// corresponding rows. The function match prevents a late old SCF write from
// updating a task already reassigned to another function.
func (r *TaskInstanceRepository) MarkStorageWrites(ctx context.Context, observations []StorageWriteObservation) (int64, error) {
	if len(observations) == 0 {
		return 0, nil
	}
	updated := int64(0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		seen := make(map[string]struct{}, len(observations))
		for _, observation := range observations {
			if strings.TrimSpace(observation.SpaceID) == "" || strings.TrimSpace(observation.DatasetID) == "" || strings.TrimSpace(observation.SubjectID) == "" || strings.TrimSpace(observation.Frequency) == "" || strings.TrimSpace(observation.FunctionName) == "" || observation.At.IsZero() {
				continue
			}
			key := strings.Join([]string{observation.SpaceID, observation.DatasetID, observation.SubjectID, observation.Frequency, observation.FunctionName}, "\x00")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			frequencyValues := frequencyVariants(observation.Frequency)
			result := tx.Model(&domain.TaskInstance{}).
				Where("c_space_id = ? AND c_dataset_id = ? AND c_subject_id = ? AND c_frequency IN ? AND c_function_name = ? AND c_is_deleted = ?", observation.SpaceID, observation.DatasetID, observation.SubjectID, frequencyValues, observation.FunctionName, false).
				Where("c_last_exec_time IS NULL OR c_last_exec_time < ?", observation.At.UTC()).
				Updates(map[string]any{"c_last_exec_status": domain.InstanceStatusSuccess, "c_last_exec_time": observation.At.UTC(), "c_mtime": time.Now().UTC()})
			if result.Error != nil {
				return result.Error
			}
			updated += result.RowsAffected
		}
		return nil
	})
	return updated, err
}

func frequencyVariants(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	values := []string{value}
	seen := map[string]struct{}{value: {}}
	add := func(candidate string) {
		if candidate == "" {
			return
		}
		if _, exists := seen[candidate]; exists {
			return
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	canonical, err := report.NormalizeDatasetFrequency(value)
	if err != nil {
		return values
	}
	add(canonical)
	// Older task rows may retain lowercase hour/day spellings while Storage
	// events use its canonical uppercase identity. Minutes and months are
	// intentionally not case-folded because M and m have different meanings.
	if len(canonical) > 1 {
		switch canonical[len(canonical)-1] {
		case 'H', 'D', 'W', 'Y':
			add(strings.ToLower(canonical))
		}
	}
	return values
}

func taskInstanceDefinitionChanged(current, desired domain.TaskInstance) bool {
	return current.RuleID != desired.RuleID ||
		current.Provider != desired.Provider ||
		current.MarketType != desired.MarketType ||
		current.DataType != desired.DataType ||
		current.DatasetID != desired.DatasetID ||
		current.SubjectID != desired.SubjectID ||
		current.Frequency != desired.Frequency ||
		current.TaskParams != desired.TaskParams ||
		current.IsDeleted != desired.IsDeleted
}

// DeactivateMissingMarketFetchRuleInstances prevents the gap auditor from
// reviving symbols or frequencies removed from an enabled market rule.
func (r *TaskInstanceRepository) DeactivateMissingMarketFetchRuleInstances(ctx context.Context, spaceID, ruleID string, activeTaskIDs []string) error {
	query := r.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_is_deleted = ?", spaceID, ruleID, false)
	if len(activeTaskIDs) > 0 {
		query = query.Where("c_task_id NOT IN ?", activeTaskIDs)
	}
	// Do not call Count on this statement before Updates. GORM's SQLite
	// dialect retains the count source table and emits UPDATE ... FROM the
	// same table, making unqualified c_* predicates ambiguous.
	return query.Updates(map[string]any{"c_is_deleted": true, "c_mtime": time.Now().UTC()}).Error
}

// DeactivateMissingResampleRuleInstances keeps resample reconciliation from
// touching market-fetch instances that happen to share a rule identifier.
func (r *TaskInstanceRepository) DeactivateMissingResampleRuleInstances(ctx context.Context, spaceID, ruleID string, activeTaskIDs []string) error {
	query := r.db.WithContext(ctx).Model(&domain.TaskInstance{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_data_type = ? AND c_is_deleted = ?", spaceID, ruleID, "kline_resample", false)
	if len(activeTaskIDs) > 0 {
		query = query.Where("c_task_id NOT IN ?", activeTaskIDs)
	}
	return query.Updates(map[string]any{"c_is_deleted": true, "c_mtime": time.Now().UTC()}).Error
}

func (r *TaskInstanceRepository) ListStale(ctx context.Context, spaceID string, before time.Time, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 {
		limit = 100
	}
	var instances []domain.TaskInstance
	query := r.db.WithContext(ctx).Where("c_is_deleted = ? AND (c_last_exec_time IS NULL OR c_last_exec_time < ?)", false, before.UTC())
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	err := query.Order("c_last_exec_time ASC").Limit(limit).Find(&instances).Error
	return instances, err
}

// ListAll returns the enabled stable instances in a bounded deterministic
// order. Gap auditing must inspect the Storage watermark even when the last
// invocation was recent, so a last_exec_time cutoff would starve slower
// frequencies (for example 1h) behind the same few stale rows.
func (r *TaskInstanceRepository) ListAll(ctx context.Context, spaceID string, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	query := r.db.WithContext(ctx).Where("c_is_deleted = ?", false)
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var instances []domain.TaskInstance
	err := query.Order("c_id ASC").Limit(limit).Find(&instances).Error
	return instances, err
}

// ListAfterID returns a bounded page after the audit cursor. The cursor keeps
// gap auditing fair when the task table grows beyond one page.
func (r *TaskInstanceRepository) ListAfterID(ctx context.Context, spaceID string, afterID, limit int) ([]domain.TaskInstance, error) {
	if limit <= 0 || limit > maxPageSize {
		limit = maxPageSize
	}
	query := r.db.WithContext(ctx).Where("c_is_deleted = ?", false)
	if strings.TrimSpace(spaceID) != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	if afterID > 0 {
		query = query.Where("c_id > ?", afterID)
	}
	var instances []domain.TaskInstance
	err := query.Order("c_id ASC").Limit(limit).Find(&instances).Error
	return instances, err
}

func (r *TaskInstanceRepository) applyFilter(q *gorm.DB, filter TaskInstanceFilter) *gorm.DB {
	if filter.SpaceID != "" {
		q = q.Where("c_space_id = ?", filter.SpaceID)
	}
	if filter.TaskID != "" {
		q = q.Where("c_task_id LIKE ?", "%"+filter.TaskID+"%")
	}
	if filter.RuleID != "" {
		q = q.Where("c_rule_id LIKE ?", "%"+filter.RuleID+"%")
	}
	if filter.Provider != "" {
		q = q.Where("c_provider = ?", filter.Provider)
	}
	if filter.MarketType != "" {
		q = q.Where("c_market_type = ?", filter.MarketType)
	}
	if filter.DataType != "" {
		q = q.Where("c_data_type = ?", filter.DataType)
	}
	if filter.DatasetID != "" {
		q = q.Where("c_dataset_id = ?", filter.DatasetID)
	}
	if filter.SubjectID != "" {
		q = q.Where("c_subject_id = ?", filter.SubjectID)
	}
	if filter.Frequency != "" {
		q = q.Where("c_frequency = ?", filter.Frequency)
	}
	if filter.FunctionName != "" {
		q = q.Where("c_function_name LIKE ?", "%"+filter.FunctionName+"%")
	}
	if filter.LastExecNode != "" {
		q = q.Where("c_last_exec_node = ?", filter.LastExecNode)
	}
	if filter.LastExecStatus != nil {
		q = q.Where("c_last_exec_status = ?", *filter.LastExecStatus)
	}
	if !filter.IncludeDeleted {
		q = q.Where("c_is_deleted = ?", false)
	}
	return q
}

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

func normalizeJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}
