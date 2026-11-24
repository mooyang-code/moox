package storageio

import (
	"fmt"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// validateStateSeries 校验一个成员的按序列字段：失败序列在序列全集里；complete 的成员不能有失败序列。
func validateStateSeries(state *factorpb.FactorPeriodState, universe map[seriesset.Series]struct{}) error {
	label := state.GetFactorId()
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
	if state.GetStatus() == "complete" && len(failed) != 0 {
		return fmt.Errorf("factor period detail: complete factor %q has failed series", label)
	}
	return nil
}

func seriesOf(ref *factorpb.SeriesRef, label string) (seriesset.Series, error) {
	if ref == nil || !validToken(ref.GetSubjectId()) || !validToken(ref.GetSeriesTag()) {
		return seriesset.Series{}, fmt.Errorf("factor period detail: %s is invalid", label)
	}
	return seriesset.Series{SubjectID: ref.GetSubjectId(), SeriesTag: ref.GetSeriesTag()}, nil
}
