package checks

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestMarketCanaryTagChecks(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	attrs := map[string]string{"sync.cron": "0 * * * *", "sync.timezone": "UTC"}
	tag := func(active uint32, attributes map[string]string) *storagepb.Tag {
		return &storagepb.Tag{TagId: "binance_spot", ActiveCount: active, Attributes: attributes}
	}
	run := func(at, status string) *collectorpb.TagSyncStatus {
		parsed, err := time.Parse(time.RFC3339, at)
		require.NoError(t, err)
		return &collectorpb.TagSyncStatus{TagId: "binance_spot", LastRunAt: parsed.UnixMilli(), LastStatus: status}
	}
	cases := []struct {
		name string
		tag  *storagepb.Tag
		run  *collectorpb.TagSyncStatus
		want []string
	}{
		{"healthy", tag(400, attrs), run("2026-09-25T09:00:00Z", "success"), nil},
		{"healthy with default cron", tag(400, nil), run("2026-09-25T09:00:00Z", "success"), nil},
		{"stale", tag(400, attrs), run("2026-09-25T05:00:00Z", "success"), []string{"stale"}},
		{"failed", tag(400, attrs), run("2026-09-25T09:00:00Z", "failed"), []string{"failed"}},
		{"empty", tag(0, attrs), run("2026-09-25T09:00:00Z", "success"), []string{"no_active_members"}},
		{"never run counts as stale", tag(400, attrs), nil, []string{"stale"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, checkTag(tc.tag, tc.run, now))
		})
	}
}

type fakeTagLister struct{ tags []*storagepb.Tag }

func (f fakeTagLister) ListTags(_ context.Context, spaceID string) ([]*storagepb.Tag, error) {
	if spaceID != "" {
		return nil, fmt.Errorf("巡检应一次读所有空间，收到 space %q", spaceID)
	}
	return f.tags, nil
}

type fakeTagSyncStatus struct{ items []*collectorpb.TagSyncStatus }

func (f fakeTagSyncStatus) ListTagSyncStatus(_ context.Context, spaceID string) ([]*collectorpb.TagSyncStatus, error) {
	if spaceID != "" {
		return nil, fmt.Errorf("巡检应一次读所有空间，收到 space %q", spaceID)
	}
	return f.items, nil
}

// 标签巡检覆盖所有空间的内置标签，不限于行情探针所在空间；非内置标签不巡检；同名标签按空间区分同步记录。
func TestSubjectTagFactsCoverBuiltinTagsInAllSpaces(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	attrs := map[string]string{"sync.cron": "0 * * * *", "sync.timezone": "UTC"}
	tags := fakeTagLister{tags: []*storagepb.Tag{
		{SpaceId: "stockcn", TagId: "cn_hs300", TagName: "沪深300", Builtin: true, ActiveCount: 300, Attributes: attrs},
		{SpaceId: "stockhk", TagId: "hk_hsi", TagName: "恒生指数", Builtin: true, ActiveCount: 0, Attributes: attrs},
		{SpaceId: "stockcn", TagId: "my_pick", TagName: "自选", ActiveCount: 5, Attributes: attrs},
	}}
	runs := fakeTagSyncStatus{items: []*collectorpb.TagSyncStatus{
		{SpaceId: "stockcn", TagId: "cn_hs300", LastRunAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).UnixMilli(), LastStatus: "success"},
		// 另一空间的同名标签失败，不能串到 stockcn
		{SpaceId: "crypto", TagId: "cn_hs300", LastRunAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).UnixMilli(), LastStatus: "failed"},
		{SpaceId: "stockhk", TagId: "hk_hsi", LastRunAt: time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC).UnixMilli(), LastStatus: "success"},
	}}
	facts, available, err := SubjectTagFacts(t.Context(), tags, runs, now)
	require.NoError(t, err)
	require.True(t, available)
	byKey := map[string]Fact{}
	for _, fact := range facts {
		byKey[fact.SpaceID+"/"+fact.CheckID] = fact
	}
	require.Len(t, byKey, 2)
	hs300 := byKey["stockcn/"+SubjectTagCheckID("stockcn", "cn_hs300")]
	require.True(t, hs300.Passed(), hs300.Reason)
	require.Equal(t, "采集对象标签 · 沪深300", hs300.Name)
	require.False(t, byKey["stockhk/"+SubjectTagCheckID("stockhk", "hk_hsi")].Passed())
	require.Equal(t, DomainCanary, DomainOf(hs300.CheckID))
}

type failingTagSyncStatus struct{}

func (failingTagSyncStatus) ListTagSyncStatus(context.Context, string) ([]*collectorpb.TagSyncStatus, error) {
	return nil, errors.New("collector unavailable")
}

// Collector 的标签同步状态读不到时不报错、不产出事实，并告诉调用方标签检查本轮无法判断（整类冻结）。
func TestSubjectTagFactsUnavailableWhenCollectorStatusFails(t *testing.T) {
	tags := fakeTagLister{tags: []*storagepb.Tag{{SpaceId: "stockcn", TagId: "cn_hs300", Builtin: true}}}
	facts, available, err := SubjectTagFacts(t.Context(), tags, failingTagSyncStatus{}, time.Now())
	require.NoError(t, err)
	require.False(t, available)
	require.Empty(t, facts)
}
