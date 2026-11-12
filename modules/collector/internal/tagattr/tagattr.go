// Package tagattr 读取 Collector 写在标签 attributes 里的业务含义：Storage 只把标签当成一组标的，
// 市场类型与标的同步配置由 Collector 定义并解释。
package tagattr

import (
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/seriesset"
)

// 标签属性键。
const (
	// KeyMarketType 是标签覆盖的市场类型（spot、swap、equity 等）。
	KeyMarketType = "market_type"
	// KeySyncSource 是该标签采集时的行情源，也是标的同步使用的数据来源（指数成分另有来源时见 KeySyncIndexSource）。
	KeySyncSource = "sync.source"
	// KeySyncIndex 是指数代码：有它时标签同步该指数的成分股，而不是数据源的全部标的。
	KeySyncIndex = "sync.index"
	// KeySyncIndexSource 是指数成分的来源，可选：成分名单与行情不是同一家时才写（港股恒指的行情源是 eastmoney、
	// 成分来源是 tencent）；不写时等于 KeySyncSource。只在配置了 KeySyncIndex 时有意义。
	KeySyncIndexSource = "sync.index_source"
	// KeySyncCron 是标的同步的 cron 表达式；KeySyncTimezone 是它的时区。
	KeySyncCron     = "sync.cron"
	KeySyncTimezone = "sync.timezone"
)

// 同步配置缺省值。
const (
	DefaultCron     = "0 * * * *"
	DefaultTimezone = "UTC"
)

func lower(tag *storagepb.Tag, key string) string {
	return strings.ToLower(strings.TrimSpace(tag.GetAttributes()[key]))
}

// Source 返回标签采集用的行情源，也是全部标的的同步来源（小写）；未配置时为空。
func Source(tag *storagepb.Tag) string { return lower(tag, KeySyncSource) }

// IndexSource 返回标签的指数成分来源（小写）：配置了 sync.index_source 用它，否则等于 Source。
func IndexSource(tag *storagepb.Tag) string {
	if value := lower(tag, KeySyncIndexSource); value != "" {
		return value
	}
	return Source(tag)
}

// Index 返回标签要同步成分股的指数代码；未配置时为空（同步数据源的全部标的）。
func Index(tag *storagepb.Tag) string { return strings.TrimSpace(tag.GetAttributes()[KeySyncIndex]) }

// MarketType 返回标签的市场类型（小写）；未配置时为空。
func MarketType(tag *storagepb.Tag) string { return lower(tag, KeyMarketType) }

// IsSpotSwap 判断一组市场类型是否正好是现货加永续：统一数据集（同一数据集里同时放两个市场的序列）只允许这个组合。
func IsSpotSwap(markets map[string]struct{}) bool {
	_, spot := markets[seriesset.MarketSpot]
	_, swap := markets[seriesset.MarketSwap]
	return len(markets) == 2 && spot && swap
}

// Cron 返回标签的同步 cron，未配置时取缺省值。
func Cron(tag *storagepb.Tag) string {
	if value := strings.TrimSpace(tag.GetAttributes()[KeySyncCron]); value != "" {
		return value
	}
	return DefaultCron
}

// Timezone 返回标签同步 cron 使用的时区，未配置时取缺省值。
func Timezone(tag *storagepb.Tag) string {
	if value := strings.TrimSpace(tag.GetAttributes()[KeySyncTimezone]); value != "" {
		return value
	}
	return DefaultTimezone
}
