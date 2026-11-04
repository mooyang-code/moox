package subjectsync

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

var ErrEmptySnapshot = errors.New("subject list is empty")

type Lister interface {
	List(context.Context) ([]marketdata.Instrument, error)
}

type ListerKey struct {
	Source         string
	InstrumentType string
}

type Listers map[ListerKey]Lister

// IndexListerKey 定位一个数据源上某个指数的成分股列表。
type IndexListerKey struct {
	Source string
	Index  string
}

// IndexListers 是指数成分股的 lister：与 Listers 分开，Listers 只表示数据源的全部标的。
type IndexListers map[IndexListerKey]Lister

// FetchIndexSnapshot 取指数的成分股快照；为空时返回 ErrEmptySnapshot。
func FetchIndexSnapshot(ctx context.Context, listers IndexListers, source, index string) ([]marketdata.Instrument, error) {
	lister, ok := listers[IndexListerKey{Source: source, Index: index}]
	if !ok {
		return nil, fmt.Errorf("数据源 %s 不支持指数 %s 的成分股", source, index)
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

func FetchSnapshot(ctx context.Context, listers Listers, sources []string, instrumentType string) ([]marketdata.Instrument, error) {
	merged := make(map[string]marketdata.Instrument)
	for _, source := range sources {
		lister, ok := listers[ListerKey{Source: source, InstrumentType: instrumentType}]
		if !ok {
			return nil, fmt.Errorf("source %s does not support %s subject listing", source, instrumentType)
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
		out[key.Source] = append(out[key.Source], key.InstrumentType)
	}
	for source := range out {
		sort.Strings(out[source])
	}
	return out
}
