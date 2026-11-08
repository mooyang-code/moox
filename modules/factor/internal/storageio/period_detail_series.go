package storageio

import (
	"fmt"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// validateStateSeries 校验一个成员的按序列字段：失败序列在序列全集里，跳过的 tag 在序列全集里出现；
// 没有序列全集（单序列数据集）时这些字段必须为空；complete 的成员不能有这些名单。
func validateStateSeries(state *factorpb.FactorPeriodState, universe map[seriesset.Series]struct{}, tags map[string]struct{}) error {
	label := state.GetFactorId()
	if len(universe) == 0 && (len(state.GetFailedSeries()) != 0 || len(state.GetSkippedSeriesTags()) != 0) {
		return fmt.Errorf("factor period detail: factor %q has series lists but the period has no universe_series", label)
	}
	failed := make(map[seriesset.Series]struct{}, len(state.GetFailedSeries()))
	for i, ref := range state.GetFailedSeries() {
		item, err := seriesOf(ref, fmt.Sprintf("factor %q failed_series item %d", label, i))
		if err != nil {
			return err
		}
		if _, dup := failed[item]; dup {
			return fmt.Errorf("factor period detail: factor %q failed_series item %q is duplicated", label, item.SubjectID+"/"+item.SeriesTag)
		}
		if _, ok := universe[item]; !ok {
			return fmt.Errorf("factor period detail: factor %q failed_series %q is not in universe_series", label, item.SubjectID+"/"+item.SeriesTag)
		}
		failed[item] = struct{}{}
	}
	skipped, err := uniqueTokens(state.GetSkippedSeriesTags(), fmt.Sprintf("factor %q skipped_series_tags", label))
	if err != nil {
		return err
	}
	for tag := range skipped {
		if _, ok := tags[tag]; !ok {
			return fmt.Errorf("factor period detail: factor %q skipped series tag %q is not in universe_series", label, tag)
		}
	}
	if state.GetStatus() == "complete" && (len(failed) != 0 || len(skipped) != 0) {
		return fmt.Errorf("factor period detail: complete factor %q has failed series or skipped series tags", label)
	}
	return nil
}

func seriesOf(ref *factorpb.SeriesRef, label string) (seriesset.Series, error) {
	if ref == nil || !validToken(ref.GetSubjectId()) || !validToken(ref.GetSeriesTag()) {
		return seriesset.Series{}, fmt.Errorf("factor period detail: %s is invalid", label)
	}
	return seriesset.Series{SubjectID: ref.GetSubjectId(), SeriesTag: ref.GetSeriesTag()}, nil
}
