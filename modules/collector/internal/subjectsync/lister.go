package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

var ErrEmptySnapshot = errors.New("subject list is empty")

type Lister interface {
	List(context.Context) ([]marketdata.Instrument, error)
}

// ListerKey 定位一个市场里某个数据源某个品种的全部标的列表。同一个数据源可以服务多个市场，所以键带市场。
type ListerKey struct {
	MarketID       string
	Source         string
	InstrumentType string
}

type Listers map[ListerKey]Lister

// IndexListerKey 定位一个市场里某个数据源上某个指数的成分股列表。
type IndexListerKey struct {
	MarketID string
	Source   string
	Index    string
}

// IndexListers 是指数成分股的 lister：与 Listers 分开，互不依赖（数据源可以只提供其中一种）。
type IndexListers map[IndexListerKey]Lister

// FetchIndexSnapshot 取指数的成分股快照；为空时返回 ErrEmptySnapshot。
func FetchIndexSnapshot(ctx context.Context, listers IndexListers, marketID, source, index string) ([]marketdata.Instrument, error) {
	lister, ok := listers[IndexListerKey{MarketID: marketID, Source: source, Index: index}]
	if !ok {
		return nil, fmt.Errorf("市场 %s 的数据源 %s 不支持指数 %s 的成分股", marketID, source, index)
	}
	items, err := lister.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("取 %s 指数 %s 的成分股：%w", source, index, err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("取 %s 指数 %s 的成分股：%w", source, index, ErrEmptySnapshot)
	}
	out := append([]marketdata.Instrument(nil), items...)
	sort.Slice(out, func(i, j int) bool { return out[i].SubjectID < out[j].SubjectID })
	return out, nil
}

func FetchSnapshot(ctx context.Context, listers Listers, marketID string, sources []string, instrumentType string) ([]marketdata.Instrument, error) {
	merged := make(map[string]marketdata.Instrument)
	for _, source := range sources {
		lister, ok := listers[ListerKey{MarketID: marketID, Source: source, InstrumentType: instrumentType}]
		if !ok {
			return nil, fmt.Errorf("source %s does not support %s %s subject listing", source, marketID, instrumentType)
		}
		items, err := lister.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("list %s %s: %w", source, instrumentType, err)
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("list %s %s: %w", source, instrumentType, ErrEmptySnapshot)
		}
		for _, item := range items {
			if item.SubjectID != "" {
				if _, exists := merged[item.SubjectID]; !exists {
					merged[item.SubjectID] = item
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, ErrEmptySnapshot
	}
	out := make([]marketdata.Instrument, 0, len(merged))
	for _, item := range merged {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SubjectID < out[j].SubjectID })
	return out, nil
}

func (l Listers) Supported() map[string][]string {
	out := map[string][]string{}
	for key := range l {
		if !slices.Contains(out[key.Source], key.InstrumentType) {
			out[key.Source] = append(out[key.Source], key.InstrumentType)
		}
	}
	for source := range out {
		sort.Strings(out[source])
	}
	return out
}
