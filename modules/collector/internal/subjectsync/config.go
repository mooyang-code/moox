package subjectsync

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron"
)

// DefaultFetchTimeout 是拉取一次标的列表的默认超时。
const DefaultFetchTimeout = 2 * time.Minute

// AttributeJob 是一项属性同步任务：按带时区的 cron 计划，从数据源刷新一个空间的标的属性。
type AttributeJob struct {
	SpaceID        string   `yaml:"space_id"`
	Sources        []string `yaml:"sources"`
	InstrumentType string   `yaml:"instrument_type"`
	Cron           string   `yaml:"cron"`
	Timezone       string   `yaml:"timezone"`
}

// NormalizeAttributeJobs 校验属性同步任务并补上默认值：时区默认 UTC，cron 默认每天 00:00。
func NormalizeAttributeJobs(jobs []AttributeJob) error {
	for i := range jobs {
		job := &jobs[i]
		job.SpaceID = strings.TrimSpace(job.SpaceID)
		if job.Timezone == "" {
			job.Timezone = "UTC"
		}
		if job.Cron == "" {
			job.Cron = "0 0 * * *"
		}
		if job.SpaceID == "" || len(job.Sources) == 0 {
			return fmt.Errorf("attributes[%d]: space_id 和 sources 不能为空", i)
		}
		if _, err := cron.ParseStandard(job.Cron); err != nil {
			return fmt.Errorf("attributes[%d].cron 无效: %w", i, err)
		}
		if _, err := time.LoadLocation(job.Timezone); err != nil {
			return fmt.Errorf("attributes[%d].timezone 无效: %w", i, err)
		}
	}
	return nil
}
