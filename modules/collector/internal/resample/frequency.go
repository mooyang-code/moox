package resample

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/frequency"
)

const maxSourceBars = 10_080

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

// ValidateTaskParams 是合成任务参数的唯一校验入口（创建、更新、种子导入、启动清单都调用它），在提交时就拒绝不可能完成的合成：
//   - 目标周期必须是源周期的整数倍；
//   - 市场必须登记了分桶方式（crypto 按 epoch_utc；A 股按交易日历 session），没有交易日历的市场不能合成；
//   - 交易时段市场的日内源周期、目标周期必须整除每个交易时段，除不尽的（如 A 股 45m）直接拒绝并列出可选周期。
//
// 结果 Dataset 身份在 space_id 与 task_id 确定后才分配，所以调用方要先分配好 target_dataset_id。
func ValidateTaskParams(spaceID string, params *domain.CollectParams) error {
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
	market, err := barclock.MarketFor(spaceID)
	if err != nil {
		return err
	}
	if alignment := strings.TrimSpace(params.Alignment); alignment != "" && alignment != market.Alignment {
		return fmt.Errorf("alignment must be %s for market %s", market.Alignment, market.ID)
	}
	clock, err := market.Clock(target.Duration)
	if err == nil {
		err = clock.ValidateSource(source.Duration)
	}
	if err != nil {
		if errors.Is(err, barclock.ErrPeriodNotDivisible) {
			return fmt.Errorf("%w；市场 %s 可选的日内周期：%s，1d 按交易日一天一根", err, market.ID, strings.Join(market.AllowedIntraday(), "、"))
		}
		return err
	}
	return ValidateTargetDatasetID(params.TargetDatasetID)
}

// BucketAt returns the latest fully closed target bucket at effectiveNow on
// the fixed grid anchored at origin.
func BucketAt(effectiveNow, origin time.Time, target FixedFrequency) (start, end time.Time) {
	if validateFixedFrequency(target) != nil || effectiveNow.IsZero() || origin.IsZero() {
		return time.Time{}, time.Time{}
	}
	elapsed := effectiveNow.Sub(origin)
	periods := elapsed / target.Duration
	if elapsed%target.Duration < 0 {
		periods--
	}
	end = origin.Add(periods * target.Duration).UTC()
	return end.Add(-target.Duration), end
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
