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
	require.Empty(t, Index(tag), "未配置指数时同步数据源的全部标的")
	require.Equal(t, "000300", Index(&storagepb.Tag{Attributes: map[string]string{KeySyncIndex: " 000300 "}}))

	// 指数成分来源：不写 sync.index_source 时等于 sync.source；写了用它，sync.source 仍是采集用的行情源。
	require.Equal(t, "binance", IndexSource(tag))
	hsi := &storagepb.Tag{Attributes: map[string]string{KeySyncSource: "eastmoney", KeySyncIndexSource: " Tencent ", KeySyncIndex: "HSI"}}
	require.Equal(t, "tencent", IndexSource(hsi))
	require.Equal(t, "eastmoney", Source(hsi))
	require.Empty(t, IndexSource(&storagepb.Tag{}))

	empty := &storagepb.Tag{}
	require.Empty(t, Source(empty))
	require.Empty(t, MarketType(empty))
	require.Equal(t, DefaultCron, Cron(empty))
	require.Empty(t, Source(nil), "nil 标签不 panic")
}
