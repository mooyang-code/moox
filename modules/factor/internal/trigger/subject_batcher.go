package trigger

import (
	"fmt"
	"time"
)

type SubjectBatchConfig struct {
	Window           time.Duration `yaml:"window"`
	MaxBatch         int           `yaml:"max_batch"`
	QueueCapacity    int           `yaml:"queue_capacity"`
	ExecutionTimeout time.Duration `yaml:"execution_timeout"`
}

func DefaultSubjectBatchConfig() SubjectBatchConfig {
	return SubjectBatchConfig{Window: 200 * time.Millisecond, MaxBatch: 64, QueueCapacity: 256, ExecutionTimeout: 2 * time.Minute}
}

func (cfg SubjectBatchConfig) Validate() error {
	if cfg.Window <= 0 || cfg.MaxBatch <= 0 || cfg.QueueCapacity <= 0 || cfg.ExecutionTimeout <= 0 {
		return fmt.Errorf("subject batch limits and timeouts must be positive")
	}
	return nil
}
