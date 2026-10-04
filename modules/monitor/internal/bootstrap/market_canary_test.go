package bootstrap

import (
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestMarketCanaryTagChecks(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tag  *storagepb.Tag
		want []string
	}{
		{"healthy", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "success", ActiveCount: 400}, nil},
		{"stale", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 05:00:00", LastStatus: "success", ActiveCount: 400}, []string{"stale"}},
		{"failed", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "failed", LastError: "451", ActiveCount: 400}, []string{"failed"}},
		{"empty", &storagepb.Tag{TagId: "binance_spot", Cron: "0 * * * *", Timezone: "UTC", LastRunAt: "2026-09-25 09:00:00", LastStatus: "success"}, []string{"no_active_members"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, checkTag(tc.tag, now))
		})
	}
}
