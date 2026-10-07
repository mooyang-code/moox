package command

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// canonicalCollectionTaskID matches task IDs whose Collector result slug is the
// ID itself, so the result Dataset is exactly "dataset_<task_id>".
var canonicalCollectionTaskID = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

type setupCollectionTaskSeed struct {
	Tasks []struct {
		SpaceID       string   `yaml:"space_id"`
		TaskID        string   `yaml:"task_id"`
		DataType      string   `yaml:"data_type"`
		TagIDs        []string `yaml:"tag_ids"`
		CollectParams struct {
			Frequency string `yaml:"frequency"`
		} `yaml:"collect_params"`
	} `yaml:"tasks"`
}

// skillKlineDatasets maps each frequency of the kline collection tasks bound to
// tagID to that task's result Dataset. Every collection task owns its result,
// so the Skill reads exactly what the task collects.
type skillKlineDatasets func(spaceID, tagID string) (map[string]string, error)

func loadSkillKlineDatasets(path string) (skillKlineDatasets, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read collection tasks: %w", err)
	}
	var seed setupCollectionTaskSeed
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return nil, fmt.Errorf("decode collection tasks: %w", err)
	}
	return func(spaceID, tagID string) (map[string]string, error) {
		datasets := make(map[string]string)
		for _, task := range seed.Tasks {
			if strings.TrimSpace(task.SpaceID) != spaceID || !strings.EqualFold(strings.TrimSpace(task.DataType), "kline") || !containsTag(task.TagIDs, tagID) {
				continue
			}
			frequency := strings.TrimSpace(task.CollectParams.Frequency)
			taskID := strings.TrimSpace(task.TaskID)
			if frequency == "" || !canonicalCollectionTaskID.MatchString(taskID) || strings.Contains(taskID, "_symbols_") {
				return nil, fmt.Errorf("collection task %s/%s has no frequency or a non-canonical task_id", spaceID, taskID)
			}
			if !isNormalizedCatalogKey(frequency) {
				// Skill intervals are lowercase keys queried verbatim as the
				// frequency, so case-significant frequencies such as 1H cannot
				// be offered without a separate frequency field.
				continue
			}
			if previous, exists := datasets[frequency]; exists {
				return nil, fmt.Errorf("tag %s has several %s kline tasks (%s, %s)", tagID, frequency, previous, "dataset_"+taskID)
			}
			datasets[frequency] = "dataset_" + taskID
		}
		return datasets, nil
	}, nil
}

func containsTag(tagIDs []string, tagID string) bool {
	for _, candidate := range tagIDs {
		if strings.TrimSpace(candidate) == tagID {
			return true
		}
	}
	return false
}
