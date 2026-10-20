package metrics

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func seriesLimitStore(t *testing.T, limits SeriesLimits) *MetricMessageStore {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	r.SetSeriesLimits(limits)
	return r
}

func samplesFor(service string, names ...string) []Sample {
	out := make([]Sample, 0, len(names))
	for _, name := range names {
		out = append(out, Sample{
			SeriesID: "series-" + name, ServiceName: service, InstanceID: service + "@node", MetricName: name,
			MetricType: "gauge", Value: 1, ObservedAt: time.Now().UTC(), MessageID: "m",
		})
	}
	return out
}

func commit(t *testing.T, r *MetricMessageStore, id, service string, samples []Sample) {
	t.Helper()
	report := &metricspb.MetricReport{ServiceName: service, InstanceId: service + "@node", BootId: "boot"}
	_, err := r.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: id}, report, samples)
	require.NoError(t, err)
}

func seriesNames(t *testing.T, r *MetricMessageStore, service string) []string {
	t.Helper()
	var names []string
	require.NoError(t, r.db.Model(&MetricSeries{}).Where("c_service_name = ?", service).Order("c_metric_name").Pluck("c_metric_name", &names).Error)
	return names
}

func TestSeriesLimitDropsOnlyNewSeriesBeyondTheHourlyLimit(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 3})
	before := testutil.ToFloat64(seriesDropped.WithLabelValues("svc-a"))

	commit(t, r, "m1", "svc-a", samplesFor("svc-a", "m_1", "m_2"))
	require.Equal(t, []string{"m_1", "m_2"}, seriesNames(t, r, "svc-a"))

	// 已有的 m_1、m_2 照常更新；新序列只能再放行一个（共 3 个），m_4、m_5 被丢弃。
	commit(t, r, "m2", "svc-a", samplesFor("svc-a", "m_1", "m_2", "m_3", "m_4", "m_5"))
	require.Equal(t, []string{"m_1", "m_2", "m_3"}, seriesNames(t, r, "svc-a"))
	require.Equal(t, before+2, testutil.ToFloat64(seriesDropped.WithLabelValues("svc-a")))

	status, err := r.SeriesLimitStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.True(t, status.Enabled)
	require.EqualValues(t, 3, status.HourCurrent)
	require.EqualValues(t, 3, status.HourMax)
	require.True(t, status.HasDropping)
	require.True(t, status.NearLimit)
	require.False(t, status.Healthy())
	require.Len(t, status.Drops, 1)
	require.Equal(t, "svc-a", status.Drops[0].Service)
	require.EqualValues(t, 2, status.Drops[0].Total)
	require.Equal(t, 3.0, testutil.ToFloat64(seriesLimitCurrent.WithLabelValues("hour")))

	// 已有序列在达到限额后仍然更新最新值。
	updated := samplesFor("svc-a", "m_1")
	updated[0].Value = 42
	updated[0].ObservedAt = time.Now().UTC().Add(time.Second)
	commit(t, r, "m3", "svc-a", updated)
	latest, err := r.GetLatest(context.Background(), "series-m_1")
	require.NoError(t, err)
	require.EqualValues(t, 42, latest.Value)
}

func TestSeriesLimitDailyLimitAndWindowReset(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 100, MaxNewPerDay: 2})
	commit(t, r, "m1", "svc", samplesFor("svc", "a", "b", "c"))
	require.Equal(t, []string{"a", "b"}, seriesNames(t, r, "svc"), "日限额 2 个")

	// 把已建序列的创建时间挪到 2 小时前：小时窗口清零，日窗口仍占满。
	require.NoError(t, r.db.Model(&MetricSeries{}).Where("1 = 1").Update("c_ctime", time.Now().UTC().Add(-2*time.Hour)).Error)
	status, err := r.SeriesLimitStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 0, status.HourCurrent)
	require.EqualValues(t, 2, status.DayCurrent)
	commit(t, r, "m2", "svc", samplesFor("svc", "c"))
	require.Equal(t, []string{"a", "b"}, seriesNames(t, r, "svc"), "日窗口还没过，仍然拒绝")

	// 再挪到 25 小时前：日窗口也清零，新序列可以进来。
	require.NoError(t, r.db.Model(&MetricSeries{}).Where("1 = 1").Update("c_ctime", time.Now().UTC().Add(-25*time.Hour)).Error)
	commit(t, r, "m3", "svc", samplesFor("svc", "c"))
	require.Equal(t, []string{"a", "b", "c"}, seriesNames(t, r, "svc"))
}

func TestSeriesLimitRedeliveryDoesNotCountTwice(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 1})
	before := testutil.ToFloat64(seriesDropped.WithLabelValues("dup"))
	samples := samplesFor("dup", "x", "y")
	commit(t, r, "same-id", "dup", samples)
	commit(t, r, "same-id", "dup", samples) // 重复投递：事务内识别为重复，不再计数
	require.Equal(t, []string{"x"}, seriesNames(t, r, "dup"))
	require.Equal(t, before+1, testutil.ToFloat64(seriesDropped.WithLabelValues("dup")))
}

func TestSeriesLimitDisabledAdmitsEverything(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{})
	names := make([]string, 0, 50)
	for i := 0; i < 50; i++ {
		names = append(names, fmt.Sprintf("m_%02d", i))
	}
	commit(t, r, "m1", "free", samplesFor("free", names...))
	require.Len(t, seriesNames(t, r, "free"), 50)
	status, err := r.SeriesLimitStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.True(t, status.Healthy())
}

func TestSeriesLimitStatusHealthyWhenWellBelowLimit(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 100, MaxNewPerDay: 1000})
	commit(t, r, "m1", "ok", samplesFor("ok", "a", "b"))
	status, err := r.SeriesLimitStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.True(t, status.Healthy())
	require.EqualValues(t, 2, status.HourCurrent)
	require.Empty(t, status.Drops)
}

func TestSeriesLimitDropsExpireFromTheStatusAfterAnHour(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 1})
	commit(t, r, "m1", "old", samplesFor("old", "a", "b"))
	status, err := r.SeriesLimitStatus(context.Background(), time.Now().UTC().Add(2*time.Hour))
	require.NoError(t, err)
	require.False(t, status.HasDropping, "一小时前的丢弃不再让检查报警")
}

func TestRejectReasonClassifiesParserErrors(t *testing.T) {
	good := func(raw string) *metricspb.MetricSnapshot {
		return &metricspb.MetricSnapshot{SchemaVersion: 1, Format: metricspb.ExpositionFormat_EXPOSITION_FORMAT_PROMETHEUS_TEXT, Data: []byte(raw)}
	}
	env := Envelope{ServiceName: "svc", InstanceID: "svc@node", ObservedAt: time.Now().UTC()}

	checksum := good("# TYPE moox_x gauge\nmoox_x 1\n")
	checksum.DataSha256 = make([]byte, 32)
	_, err := ParseSnapshot(checksum, env, DefaultLimits())
	require.Equal(t, RejectChecksum, RejectReason(err))

	_, err = ParseSnapshot(good("# TYPE moox_x gauge\nmoox_x 1\nmoox_y 2\n"), env, Limits{MaxSamples: 1})
	require.Equal(t, RejectLimitExceeded, RejectReason(err))

	_, err = ParseSnapshot(good("this is not prometheus text {{{"), env, DefaultLimits())
	require.Equal(t, RejectParse, RejectReason(err))

	require.Equal(t, RejectParse, RejectReason(errors.New("anything else")))
	require.Equal(t, RejectParse, RejectReason(nil))
}

func TestIngestRejectedKeepsTheLatestReasonPerService(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{})
	before := testutil.ToFloat64(ingestTotal.WithLabelValues("rejected", RejectChecksum))
	r.RecordIngestRejected(nil, "svc-b", RejectParse)
	r.RecordIngestRejected(nil, "svc-b", RejectChecksum)
	require.Equal(t, before+1, testutil.ToFloat64(ingestTotal.WithLabelValues("rejected", RejectChecksum)))

	rejects := r.IngestRejects()
	require.Len(t, rejects, 1)
	require.Equal(t, "svc-b", rejects[0].Service)
	require.Equal(t, RejectChecksum, rejects[0].LastReason)
	require.EqualValues(t, 1, rejects[0].ByReason[RejectParse])
	require.False(t, rejects[0].LastAt.IsZero())
}

func TestRecordSamplesFilteredCountsPerService(t *testing.T) {
	before := testutil.ToFloat64(samplesFiltered.WithLabelValues("filtered-svc"))
	RecordSamplesFiltered("filtered-svc", 0)
	RecordSamplesFiltered("filtered-svc", 7)
	require.Equal(t, before+7, testutil.ToFloat64(samplesFiltered.WithLabelValues("filtered-svc")))
}

func TestIngestDiagnosticsAreBounded(t *testing.T) {
	d := newIngestDiagnostics()
	for i := 0; i < maxDiagServices+50; i++ {
		d.recordDropped(fmt.Sprintf("svc-%d", i), "m", 1, time.Now())
		d.recordRejected(fmt.Sprintf("svc-%d", i), RejectParse, time.Now())
	}
	require.Len(t, d.drops(time.Time{}), maxDiagServices)
	require.Len(t, d.rejects(), maxDiagServices)
}

func TestSeriesLimitGaugeSurvivesAnEmptySnapshot(t *testing.T) {
	r := seriesLimitStore(t, SeriesLimits{MaxNewPerHour: 10, MaxNewPerDay: 100})
	commit(t, r, "m1", "svc", samplesFor("svc", "a", "b", "c"))
	require.Equal(t, 3.0, testutil.ToFloat64(seriesLimitCurrent.WithLabelValues("hour")))

	// 白名单过滤后为空的快照：gauge 仍是目录里的真实新增数，而不是 0。
	commit(t, r, "m2", "svc", nil)
	require.Equal(t, 3.0, testutil.ToFloat64(seriesLimitCurrent.WithLabelValues("hour")))
	require.Equal(t, 3.0, testutil.ToFloat64(seriesLimitCurrent.WithLabelValues("day")))
}
