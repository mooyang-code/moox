package bootstrap

import (
	"context"
	"fmt"
	"testing"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMarketCanaryTagChecks(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	attrs := map[string]string{"sync.cron": "0 * * * *", "sync.timezone": "UTC"}
	tag := func(active uint32, attributes map[string]string) *storagepb.Tag {
		return &storagepb.Tag{TagId: "binance_spot", ActiveCount: active, Attributes: attributes}
	}
	run := func(at, status string) *collectorpb.TagSyncStatus {
		return &collectorpb.TagSyncStatus{TagId: "binance_spot", LastRunAt: at, LastStatus: status}
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
func TestRunSubjectTagChecksCoversBuiltinTagsInAllSpaces(t *testing.T) {
	repositories := openMonitorTestRepositories(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	attrs := map[string]string{"sync.cron": "0 * * * *", "sync.timezone": "UTC"}
	tags := fakeTagLister{tags: []*storagepb.Tag{
		{SpaceId: "stockcn", TagId: "cn_hs300", TagName: "沪深300", Builtin: true, ActiveCount: 300, Attributes: attrs},
		{SpaceId: "stockhk", TagId: "hk_hsi", TagName: "恒生指数", Builtin: true, ActiveCount: 0, Attributes: attrs},
		{SpaceId: "stockcn", TagId: "my_pick", TagName: "自选", ActiveCount: 5, Attributes: attrs},
	}}
	runs := fakeTagSyncStatus{items: []*collectorpb.TagSyncStatus{
		{SpaceId: "stockcn", TagId: "cn_hs300", LastRunAt: "2026-11-12T09:00:00Z", LastStatus: "success"},
		// 另一空间的同名标签失败，不能串到 stockcn
		{SpaceId: "crypto", TagId: "cn_hs300", LastRunAt: "2026-11-12T09:00:00Z", LastStatus: "failed"},
		{SpaceId: "stockhk", TagId: "hk_hsi", LastRunAt: "2026-11-12T09:00:00Z", LastStatus: "success"},
	}}
	results := map[string]domain.CheckResult{}
	hook := func(_ context.Context, check domain.Check, result domain.CheckResult) {
		results[check.SpaceID+"/"+check.CheckID] = result
	}

	require.NoError(t, runSubjectTagChecks(ctx, repositories, tags, runs, hook, now))
	require.Len(t, results, 2)
	hs300 := results["stockcn/"+subjectTagCheckID("stockcn", "cn_hs300")]
	require.True(t, hs300.Success, hs300.ErrorMessage)
	hsi := results["stockhk/"+subjectTagCheckID("stockhk", "hk_hsi")]
	require.False(t, hsi.Success)
	_, err := repositories.Checks.Get(ctx, "stockcn", subjectTagCheckID("stockcn", "my_pick"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// 第二轮不改检查名：新建与修复用同一个名称
	require.NoError(t, runSubjectTagChecks(ctx, repositories, tags, runs, hook, now.Add(time.Minute)))
	check, err := repositories.Checks.Get(ctx, "stockcn", subjectTagCheckID("stockcn", "cn_hs300"))
	require.NoError(t, err)
	require.Equal(t, "采集对象标签 · 沪深300", check.Name)
}
