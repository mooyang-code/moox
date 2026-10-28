package bootstrap

import (
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
