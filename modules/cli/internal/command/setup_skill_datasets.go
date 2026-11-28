package command

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type setupCollectionTaskSeed struct {
	Tasks []struct {
		SpaceID string `yaml:"space_id"`
		TaskID  string `yaml:"task_id"`
		Start   string `yaml:"start"`
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
