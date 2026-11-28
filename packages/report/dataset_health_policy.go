package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

// RealtimeTimeSeriesPolicy 定义 Monitor 判定实时时序数据集新鲜度与停滞的阈值。
type RealtimeTimeSeriesPolicy struct {
	Defaults  RealtimeTimeSeriesDefaults   `yaml:"defaults" json:"defaults"`
	Overrides []RealtimeTimeSeriesOverride `yaml:"overrides" json:"overrides"`
}

type RealtimeTimeSeriesDefaults struct {
	RunMissedIntervals     int `yaml:"run_missed_intervals" json:"run_missed_intervals"`
	SuccessMissedIntervals int `yaml:"success_missed_intervals" json:"success_missed_intervals"`
	// MinuteLateness 与 HourLateness 是应收的最新一根 K 线收线后允许迟到多久：分钟级（短于 1 小时的周期）与
	// 小时级（1h、4h）；日线及以上允许迟到 1 个周期。超过仍没到，数据集判为过期并告警。
	MinuteLateness time.Duration `yaml:"minute_lateness" json:"minute_lateness"`
	HourLateness   time.Duration `yaml:"hour_lateness" json:"hour_lateness"`
}

type RealtimeTimeSeriesOverride struct {
	SpaceID   string `yaml:"space_id" json:"space_id"`
	DatasetID string `yaml:"dataset_id" json:"dataset_id"`
	Freq      string `yaml:"freq" json:"freq"`
	// Lateness 覆盖该数据集最新一根 K 线收线后允许迟到的时间。
	Lateness time.Duration `yaml:"lateness,omitempty" json:"lateness,omitempty"`
}

// DefaultRealtimeTimeSeriesPolicy 返回 Monitor 默认的实时时序数据集健康阈值。
func DefaultRealtimeTimeSeriesPolicy() RealtimeTimeSeriesPolicy {
	return RealtimeTimeSeriesPolicy{
		Defaults: RealtimeTimeSeriesDefaults{
			RunMissedIntervals:     2,
			SuccessMissedIntervals: 3,
			MinuteLateness:         10 * time.Second,
			HourLateness:           20 * time.Second,
		},
	}
}

// Lateness 返回数据集最新一根 K 线收线后允许迟到多久：有按数据集的覆盖就用覆盖，否则分钟级用 MinuteLateness、
// 小时级用 HourLateness，日线及以上允许迟到 1 个周期（月按 30 天计）。周期无法识别时返回 0（不判断迟到）。
func (p RealtimeTimeSeriesPolicy) Lateness(spaceID, datasetID, freq string) time.Duration {
	parsed, err := frequency.Parse(freq)
	if err != nil {
		return 0
	}
	for _, override := range p.Overrides {
		overrideFreq, err := frequency.Parse(override.Freq)
		if err == nil && override.SpaceID == spaceID && override.DatasetID == datasetID && overrideFreq == parsed && override.Lateness > 0 {
			return override.Lateness
		}
	}
	switch period := parsed.NominalDuration(); {
	case period < time.Hour:
		return p.Defaults.MinuteLateness
	case period < 24*time.Hour:
		return p.Defaults.HourLateness
	default:
		return period
	}
}

// Validate 校验数据集健康策略的默认阈值与按数据集覆盖项。
func (p RealtimeTimeSeriesPolicy) Validate() error {
	defaults := p.Defaults
	if defaults.RunMissedIntervals <= 0 ||
		defaults.SuccessMissedIntervals <= 0 ||
		defaults.MinuteLateness <= 0 ||
		defaults.HourLateness <= 0 {
		return fmt.Errorf("数据集健康策略默认阈值必须大于 0")
	}
	seen := make(map[string]bool, len(p.Overrides))
	for index, override := range p.Overrides {
		if strings.TrimSpace(override.SpaceID) == "" ||
			strings.TrimSpace(override.DatasetID) == "" ||
			strings.TrimSpace(override.Freq) == "" {
			return fmt.Errorf("数据集健康策略第 %d 条覆盖项缺少 space_id、dataset_id 或 freq", index)
		}
		freq, err := frequency.Parse(override.Freq)
		if err != nil {
			return fmt.Errorf("数据集健康策略第 %d 条覆盖项周期无效 %q", index, override.Freq)
		}
		key := strings.Join([]string{override.SpaceID, override.DatasetID, string(freq)}, "\x00")
		if seen[key] {
			return fmt.Errorf(
				"数据集健康策略存在重复覆盖项：%s/%s/%s",
				override.SpaceID,
				override.DatasetID,
				override.Freq,
			)
		}
		seen[key] = true
		if override.Lateness < 0 {
			return fmt.Errorf("数据集健康策略第 %d 条覆盖项 lateness 不能为负数", index)
		}
	}
	return nil
}
