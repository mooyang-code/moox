package resample

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/frequency"
)

// FixedFrequency is one canonical frequency that can be resampled: a fixed,
// whole-minute bar from 1m to 1d.
type FixedFrequency struct {
	Storage  string
	Duration time.Duration
}

// ParseFixedFrequency parses a canonical frequency that can be resampled.
func ParseFixedFrequency(raw string) (FixedFrequency, error) {
	duration, err := domain.ResampleFrequencyDuration(raw)
	if err != nil {
		return FixedFrequency{}, err
	}
	parsed, _ := frequency.Parse(raw)
	return FixedFrequency{Storage: string(parsed), Duration: duration}, nil
}

// ValidateResamplePair verifies that a target bucket expands to a bounded,
// integral number of source bars.
func ValidateResamplePair(source, target FixedFrequency) error {
	if err := validateFixedFrequency(source); err != nil {
		return fmt.Errorf("source frequency: %w", err)
	}
	if err := validateFixedFrequency(target); err != nil {
		return fmt.Errorf("target frequency: %w", err)
	}
	if target.Duration <= source.Duration {
		return fmt.Errorf("target frequency must be greater than source frequency")
	}
	// 目标周期必须是源周期的整数倍（例如 30m 不能合成 45m）；整除一天由频率语法保证。
	if target.Duration%source.Duration != 0 {
		return fmt.Errorf("target frequency %s must be a whole multiple of source frequency %s", target.Storage, source.Storage)
	}
	return nil
}

// ValidateTaskParamsBasic 在尚未加载源数据集元数据时校验合成任务的静态结构契约：
// 源/目标数据集 ID 不同且合法、序列标签非空、目标周期是源周期的整数倍、输出字段属于标准可归约列。
func ValidateTaskParamsBasic(params *domain.CollectParams) error {
	if params == nil {
		return fmt.Errorf("resample collect params are required")
	}
	if strings.TrimSpace(params.SourceDatasetID) == "" {
		return fmt.Errorf("source_dataset_id is required")
	}
	if strings.TrimSpace(params.TargetDatasetID) == "" {
		return fmt.Errorf("target_dataset_id is required")
	}
	if strings.TrimSpace(params.SourceDatasetID) == strings.TrimSpace(params.TargetDatasetID) {
		return fmt.Errorf("source and target Dataset IDs must differ")
	}
	if strings.TrimSpace(params.SourceSeriesTag) == "" {
		return fmt.Errorf("source_series_tag is required")
	}
	source, err := ParseFixedFrequency(params.SourceFrequency)
	if err != nil {
		return fmt.Errorf("source_frequency: %w", err)
	}
	target, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return fmt.Errorf("target_frequency: %w", err)
	}
	if err := ValidateResamplePair(source, target); err != nil {
		return err
	}
	if err := ValidateOutputFieldsBasic(params.OutputFields); err != nil {
		return err
	}
	return ValidateTargetDatasetID(params.TargetDatasetID)
}

// ValidateTaskParamsForSource 是合成任务参数的完整校验入口，直接依据源数据集的 alignment、calendar 与活跃列校验：
//   - 静态契约（ID、序列标签、源/目标周期整倍数）；
//   - 源数据集分桶时钟（由 source.Alignment + source.Attributes["calendar"] 派生，与 space_id 解耦）；
//   - 交易时段数据集的日内源/目标周期必须整除每个交易时段；
//   - 输出字段必须包含在“标准字段归约规则表 ∩ 源数据集活跃列”中。
func ValidateTaskParamsForSource(sourceInfo storagesource.DatasetInfo, params *domain.CollectParams) error {
	if err := ValidateTaskParamsBasic(params); err != nil {
		return err
	}
	source, _ := ParseFixedFrequency(params.SourceFrequency)
	target, _ := ParseFixedFrequency(params.TargetFrequency)
	market, err := barclock.MarketForDataset(sourceInfo.Alignment, sourceInfo.Attributes["calendar"])
	if err != nil {
		return err
	}
	if _, err := ResolveTaskRules(sourceInfo.ColumnTypes, params.OutputFields); err != nil {
		return err
	}
	clock, err := market.Clock(target.Duration)
	if err == nil {
		err = clock.ValidateSource(source.Duration)
	}
	if err != nil {
		if errors.Is(err, barclock.ErrPeriodNotDivisible) {
			label := market.CalendarID
			if label == "" {
				label = market.Alignment
			}
			return fmt.Errorf("%w；日历 %s 可选的日内周期：%s，1d 按交易日一天一根", err, label, strings.Join(market.AllowedIntraday(), "、"))
		}
		return err
	}
	return nil
}

func validateFixedFrequency(frequency FixedFrequency) error {
	parsed, err := ParseFixedFrequency(frequency.Storage)
	if err != nil {
		return err
	}
	if parsed != frequency {
		return fmt.Errorf("frequency must use the canonical Storage value")
	}
	return nil
}
