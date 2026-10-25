package preview

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

type fakeSource struct {
	subjects []string
	reads    int
}

func (*fakeSource) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (f *fakeSource) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	f.reads++
	frames := map[string]*storageio.Frame{}
	for i, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
		for at := req.Start; at.Before(req.End); at = at.Add(time.Minute) {
			frame.Rows = append(frame.Rows, []any{at, "binance", float64(i + 1)})
		}
		frames[subject] = frame
	}
	return frames, nil
}

func (*fakeSource) WriteRows(context.Context, string, string, string, []storageio.ResultRow) error {
	return fmt.Errorf("试算不能写入")
}

func (*fakeSource) ReportComputed(context.Context, storageio.PeriodMarker) error {
	return fmt.Errorf("试算不能上报")
}

func (*fakeSource) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

func (f *fakeSource) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return f.subjects, nil
}

func newService(source *fakeSource) *Service {
	s := New(source, 1)
	s.now = func() time.Time { return time.Date(2026, 10, 17, 8, 30, 30, 0, time.UTC) }
	return s
}

var set = domain.FactorSet{SetID: "fset_a", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m"}

func TestPreviewComputesRecentPeriodsForShownSubjects(t *testing.T) {
	source := &fakeSource{subjects: []string{"ETH", "BTC", "SOL"}}
	def := domain.MustDefinition("draft", "draft", "outputs:\n  m: ts_mean(close, 3)\n  r: cs_rank(close)\n")
	result, err := newService(source).Preview(context.Background(), Request{Set: set, Factor: def, Subjects: []string{"BTC"}, Periods: 4})
	// 一个定义混用时序与截面算子：编译为截面因子，计算用全部标的，只展示 BTC。
	require.NoError(t, err)
	require.Equal(t, []string{"m", "r"}, result.Columns)
	require.Len(t, result.Rows, 4)
	for _, row := range result.Rows {
		require.Equal(t, "BTC", row.Subject)
		require.NotNil(t, row.Values[0])
		require.InDelta(t, 1.0/3, *row.Values[1], 1e-12, "全集按名称排序后 BTC 的值最小，排名 1/3")
	}
	require.True(t, result.Rows[3].DataTime.Equal(time.Date(2026, 10, 17, 8, 29, 0, 0, time.UTC)), "最近一期是当前周期的前一期")
}

func TestPreviewRejectsOversizedRequestsBeforeReading(t *testing.T) {
	source := &fakeSource{subjects: []string{"BTC"}}
	def := domain.MustDefinition("draft", "draft", "outputs:\n  m: ts_mean(close, 3)\n")
	_, err := newService(source).Preview(context.Background(), Request{Set: set, Factor: def, Periods: MaxPeriods + 1})
	require.ErrorContains(t, err, "periods")

	many := make([]string, MaxUniverse)
	for i := range many {
		many[i] = fmt.Sprintf("S%04d", i)
	}
	source = &fakeSource{subjects: many}
	heavy := domain.MustDefinition("draft", "draft", "outputs:\n  r: cs_rank(ts_mean(close, 5000))\n")
	_, err = newService(source).Preview(context.Background(), Request{Set: set, Factor: heavy, Periods: MaxPeriods})
	require.ErrorIs(t, err, pipeline.ErrMemoryBudget)
	require.Zero(t, source.reads, "超限时不读取")
}

// 显式指定的标的必须属于源数据集：任务的标的就是源数据集当前的标的。
func TestPreviewRejectsSubjectsOutsideTheSourceDataset(t *testing.T) {
	source := &fakeSource{subjects: []string{"BTC", "ETH"}}
	def := domain.MustDefinition("mean", "mean", "outputs:\n  m: ts_mean(close, 2)\n")
	_, err := newService(source).Preview(context.Background(), Request{Set: set, Factor: def, Subjects: []string{"BTC", "DOGE"}, Periods: 4})
	require.ErrorContains(t, err, "DOGE")
	require.ErrorContains(t, err, "不在源数据集")
	require.Zero(t, source.reads, "校验在读取源数据之前")
}
