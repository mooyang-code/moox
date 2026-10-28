package tagattr

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestAccessorsNormalizeAndDefault(t *testing.T) {
	tag := &storagepb.Tag{Attributes: map[string]string{KeyMarketType: " SPOT ", KeySyncSource: "Binance", KeySyncCron: "13 * * * *"}}
	require.Equal(t, "spot", MarketType(tag))
	require.Equal(t, "binance", Source(tag))
	require.Equal(t, "13 * * * *", Cron(tag))
	require.Equal(t, DefaultTimezone, Timezone(tag), "未配置时区取缺省值")

	empty := &storagepb.Tag{}
	require.Empty(t, Source(empty))
	require.Empty(t, MarketType(empty))
	require.Equal(t, DefaultCron, Cron(empty))
	require.Empty(t, Source(nil), "nil 标签不 panic")
}
