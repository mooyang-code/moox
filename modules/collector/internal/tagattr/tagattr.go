// Package tagattr 读取 Collector 写在标签 attributes 里的业务含义：Storage 只把标签当成一组标的，
// 市场类型与标的同步配置由 Collector 定义并解释。
package tagattr

import (
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 标签属性键。
const (
	// KeyMarketType 是标签覆盖的市场类型（spot、swap、equity 等）。
	KeyMarketType = "market_type"
	// KeySyncSource 是标的同步使用的数据来源，也是该标签采集时的行情源。
	KeySyncSource = "sync.source"
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

// Source 返回标签的同步来源（小写）；未配置时为空。
func Source(tag *storagepb.Tag) string { return lower(tag, KeySyncSource) }

// MarketType 返回标签的市场类型（小写）；未配置时为空。
func MarketType(tag *storagepb.Tag) string { return lower(tag, KeyMarketType) }

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
