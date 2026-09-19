// Package ruleseed loads and safely applies the built-in Collector task bundle.
package ruleseed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

type taskSeed struct {
	Tasks []taskSeedItem `yaml:"tasks"`
}

type taskSeedItem struct {
	SpaceID       string         `yaml:"space_id"`
	TaskID        string         `yaml:"task_id"`
	TaskName      string         `yaml:"task_name"`
	Description   string         `yaml:"description"`
	DataType      string         `yaml:"data_type"`
	Provider      string         `yaml:"provider"`
	MarketType    string         `yaml:"market_type"`
	Enabled       bool           `yaml:"enabled"`
	Creator       string         `yaml:"creator"`
	CollectParams map[string]any `yaml:"collect_params"`
}

// SeedSummary reports missing-only task application results.
type SeedSummary struct {
	Created   int
	Unchanged int
}

// Load reads and validates a Collector task bundle.
func Load(r io.Reader) ([]domain.CollectionTask, error) {
	return loadTaskSeed(r)
}

// LoadFile reads and validates a Collector task bundle from disk.
func LoadFile(path string) ([]domain.CollectionTask, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("seed file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open seed file: %w", err)
	}
	defer file.Close()
	return Load(file)
}

func loadTaskSeed(r io.Reader) ([]domain.CollectionTask, error) {
	if r == nil {
		return nil, fmt.Errorf("seed reader is required")
	}
	decoder := yaml.NewDecoder(r)
	decoder.KnownFields(true)
	var seed taskSeed
	if err := decoder.Decode(&seed); err != nil {
		return nil, fmt.Errorf("decode task seed: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode task seed: multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("decode task seed: %w", err)
	}
	if len(seed.Tasks) == 0 {
		return nil, fmt.Errorf("task seed must contain at least one task")
	}

	tasks := make([]domain.CollectionTask, 0, len(seed.Tasks))
	seen := make(map[string]struct{}, len(seed.Tasks))
	for index, item := range seed.Tasks {
		task, err := validateTaskSeedItem(item)
		if err != nil {
			return nil, fmt.Errorf("tasks[%d]: %w", index, err)
		}
		key := task.SpaceID + "\x00" + task.TaskID
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("tasks[%d]: duplicate task %s/%s", index, task.SpaceID, task.TaskID)
		}
		seen[key] = struct{}{}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func validateTaskSeedItem(item taskSeedItem) (domain.CollectionTask, error) {
	spaceID := strings.TrimSpace(item.SpaceID)
	ruleID := strings.TrimSpace(item.TaskID)
	dataType := strings.ToLower(strings.TrimSpace(item.DataType))
	provider := strings.ToLower(strings.TrimSpace(item.Provider))
	marketType := strings.ToLower(strings.TrimSpace(item.MarketType))
	if spaceID == "" || ruleID == "" || dataType == "" || provider == "" || marketType == "" {
		return domain.CollectionTask{}, fmt.Errorf("space_id, task_id, data_type, provider and market_type are required")
	}
	if item.CollectParams == nil {
		return domain.CollectionTask{}, fmt.Errorf("collect_params is required")
	}
	rawParams, err := json.Marshal(item.CollectParams)
	if err != nil {
		return domain.CollectionTask{}, fmt.Errorf("marshal collect_params: %w", err)
	}
	params, err := domain.ParseCollectParams(string(rawParams), provider, marketType, dataType)
	if err != nil {
		return domain.CollectionTask{}, fmt.Errorf("collect_params: %w", err)
	}
	if err := params.Validate(); err != nil {
		return domain.CollectionTask{}, fmt.Errorf("collect_params: %w", err)
	}
	if params.Provider != provider || params.MarketType != marketType || params.Collector.DataType != dataType {
		return domain.CollectionTask{}, fmt.Errorf("collect_params provider, market_type and data_type must match task fields")
	}
	canonical, err := json.Marshal(params)
	if err != nil {
		return domain.CollectionTask{}, fmt.Errorf("marshal canonical collect_params: %w", err)
	}
	return domain.CollectionTask{
		SpaceID:       spaceID,
		TaskID:        ruleID,
		TaskName:      strings.TrimSpace(item.TaskName),
		Description:   strings.TrimSpace(item.Description),
		DataType:      dataType,
		Provider:      provider,
		MarketType:    marketType,
		CollectParams: string(canonical),
		Enabled:       item.Enabled,
		Creator:       strings.TrimSpace(item.Creator),
	}, nil
}

// SeedMissing inserts only absent (space_id, task_id) pairs. Existing rows are
// deliberately left untouched so a user disable or edit survives redeploy.
func SeedMissing(ctx context.Context, repo *store.CollectionTaskRepository, rules []domain.CollectionTask) (SeedSummary, error) {
	if repo == nil {
		return SeedSummary{}, fmt.Errorf("task repository is required")
	}
	var summary SeedSummary
	for _, rule := range rules {
		_, err := repo.GetByTaskID(ctx, rule.SpaceID, rule.TaskID)
		switch {
		case err == nil:
			summary.Unchanged++
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := repo.Create(ctx, rule); err != nil {
				return summary, fmt.Errorf("create task %s/%s: %w", rule.SpaceID, rule.TaskID, err)
			}
			summary.Created++
		default:
			return summary, fmt.Errorf("check task %s/%s: %w", rule.SpaceID, rule.TaskID, err)
		}
	}
	return summary, nil
}
