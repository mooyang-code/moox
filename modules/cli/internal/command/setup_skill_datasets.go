package command

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"

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
		Start         string   `yaml:"start"`
		CollectParams struct {
			Frequency    string   `yaml:"frequency"`
			OutputFields []string `yaml:"output_fields"`
		} `yaml:"collect_params"`
	} `yaml:"tasks"`
}

func readCollectionTaskSeed(path string) (setupCollectionTaskSeed, error) {
	var seed setupCollectionTaskSeed
	raw, err := os.ReadFile(path)
	if err != nil {
		return seed, fmt.Errorf("read collection tasks: %w", err)
	}
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return seed, fmt.Errorf("decode collection tasks: %w", err)
	}
	return seed, nil
}

// manualStartTasks 返回种子里标了 start: manual 的任务 ID：setup start 不启用它们，由用户显式启用。
func manualStartTasks(path, spaceID string) (map[string]bool, error) {
	seed, err := readCollectionTaskSeed(path)
	if err != nil {
		return nil, err
	}
	manual := map[string]bool{}
	for _, task := range seed.Tasks {
		if strings.TrimSpace(task.SpaceID) == spaceID && strings.TrimSpace(task.Start) == "manual" {
			manual[strings.TrimSpace(task.TaskID)] = true
		}
	}
	return manual, nil
}

// skillKlineDatasets maps each frequency of the kline collection tasks bound to
// tagID to that task's result Dataset. Every collection task owns its result,
// so the Skill reads exactly what the task collects.
// 同一标签同一频率有两条任务时，取没有指定 output_fields（默认输出）的那条；分不出来才报错。
type skillKlineDatasets func(spaceID, tagID string) (map[string]string, error)

func loadSkillKlineDatasets(path string) (skillKlineDatasets, error) {
	seed, err := readCollectionTaskSeed(path)
	if err != nil {
		return nil, err
	}
	return func(spaceID, tagID string) (map[string]string, error) {
		datasets := make(map[string]string)
		defaultOutput := make(map[string]bool)
		for _, task := range seed.Tasks {
			if strings.TrimSpace(task.SpaceID) != spaceID || !strings.EqualFold(strings.TrimSpace(task.DataType), "kline") || !containsTag(task.TagIDs, tagID) {
				continue
			}
			frequency := strings.TrimSpace(task.CollectParams.Frequency)
			taskID := strings.TrimSpace(task.TaskID)
			if frequency == "" || !canonicalCollectionTaskID.MatchString(taskID) || strings.Contains(taskID, "_symbols_") {
				return nil, fmt.Errorf("collection task %s/%s has no frequency or a non-canonical task_id", spaceID, taskID)
			}
			if !frequencypkg.IsCanonical(frequency) {
				return nil, fmt.Errorf("collection task %s/%s frequency %q is not canonical", spaceID, taskID, frequency)
			}
			plain := len(task.CollectParams.OutputFields) == 0
			if previous, exists := datasets[frequency]; exists {
				if plain == defaultOutput[frequency] {
					return nil, fmt.Errorf("tag %s has several %s kline tasks (%s, %s)", tagID, frequency, previous, "dataset_"+taskID)
				}
				if !plain {
					continue
				}
			}
			datasets[frequency], defaultOutput[frequency] = "dataset_"+taskID, plain
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
