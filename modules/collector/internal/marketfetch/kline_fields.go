package marketfetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// errFieldUnsupported 表示写入目标选了一个附加字段，但这条管线的（市场, 品种）下没有来源（任务校验本应拦下，属于配置错误）。
var errFieldUnsupported = errors.New("这个市场与品种下没有提供该附加字段的来源")

// fieldAwaitReserve 是实时批次取附加字段时，在批次截止前留出的时间：取不到只判选了这些字段的目标失败，
// 其他目标仍有时间照常写入。
const fieldAwaitReserve = 2 * time.Second

// fieldFetch 是一个来源在这一项上要取的字段。
type fieldFetch struct {
	source marketdata.FieldSource
	fields []string
}

// fieldOutcome 是一项的附加字段取数结果：values 按 K 线开始时刻（Unix 纳秒）给出各字段的值；
// failed 是没取到的字段及原因（来源报错，或没有来源）。
type fieldOutcome struct {
	values map[int64]map[string]marketdata.FieldValue
	failed map[string]error
}

// selectedOutputFields 返回这一项显式选中的输出字段：有写入目标时是各目标的并集，否则是取数项自己的。
func selectedOutputFields(req Request, item domain.CollectionItem) map[string]struct{} {
	selected := map[string]struct{}{}
	add := func(fields []string) {
		for _, field := range fields {
			if field = strings.ToLower(strings.TrimSpace(field)); field != "" {
				selected[field] = struct{}{}
			}
		}
	}
	if len(req.Targets) == 0 {
		add(item.OutputFields)
		return selected
	}
	for _, target := range itemTargets(req, item) {
		add(targetOutputFields(item, target))
	}
	return selected
}

// wantedFields 把这一项选中的附加字段分到各自的来源；选了目录里登记的附加字段而这条管线没有来源的，记进 failed（配置错误）。
// 没选任何附加字段时不取数。
func (p *KlinePipeline) wantedFields(req Request, item domain.CollectionItem) (fetches []fieldFetch, failed map[string]error) {
	failed = map[string]error{}
	if len(p.Fields) == 0 && len(p.ExtraFieldIDs) == 0 {
		return nil, failed
	}
	selected := selectedOutputFields(req, item)
	for _, source := range p.Fields {
		var fields []string
		for _, spec := range source.FieldSpecs() {
			id := strings.ToLower(strings.TrimSpace(spec.ID))
			if _, ok := selected[id]; ok {
				fields = append(fields, id)
				delete(selected, id)
			}
		}
		if len(fields) > 0 {
			fetches = append(fetches, fieldFetch{source: source, fields: fields})
		}
	}
	for _, id := range p.ExtraFieldIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if _, ok := selected[id]; ok {
			failed[id] = errFieldUnsupported
		}
	}
	return fetches, failed
}

// fetchedFieldIDs 返回这一项取了数的附加字段：用来在行里认出这些列，没有显式选中它们的写入目标不写。
func fetchedFieldIDs(fetches []fieldFetch) map[string]marketdata.FieldValue {
	ids := map[string]marketdata.FieldValue{}
	for _, fetch := range fetches {
		for _, id := range fetch.fields {
			ids[id] = marketdata.FieldValue{}
		}
	}
	return ids
}

// startFields 异步向各来源取 [req.Start, req.End) 内的附加字段，返回的函数等待并合并结果。
// 一个来源报错只影响它提供的字段。
func startFields(ctx context.Context, req marketdata.FieldRequest, fetches []fieldFetch) func() fieldOutcome {
	type result struct {
		values map[time.Time]map[string]marketdata.FieldValue
		err    error
	}
	done := make([]chan result, len(fetches))
	for index, fetch := range fetches {
		done[index] = make(chan result, 1)
		request := req
		request.Fields = fetch.fields
		go func(fetch fieldFetch, out chan<- result) {
			fetchCtx, cancel := fieldFetchContext(ctx, request.Realtime)
			defer cancel()
			values, err := fetch.source.FetchFields(fetchCtx, request)
			out <- result{values: values, err: err}
		}(fetch, done[index])
	}
	return func() fieldOutcome {
		outcome := fieldOutcome{values: map[int64]map[string]marketdata.FieldValue{}, failed: map[string]error{}}
		for index, fetch := range fetches {
			got := <-done[index]
			if got.err != nil {
				for _, id := range fetch.fields {
					outcome.failed[id] = got.err
				}
				continue
			}
			for barStart, values := range got.values {
				key := barStart.UnixNano()
				for _, id := range fetch.fields {
					value, ok := values[id]
					if !ok {
						continue
					}
					if outcome.values[key] == nil {
						outcome.values[key] = map[string]marketdata.FieldValue{}
					}
					outcome.values[key][id] = value
				}
			}
		}
		return outcome
	}
}

// fieldFetchContext 给实时批次的附加字段取数设一个早于批次截止的上限：截止前 fieldAwaitReserve，剩余不足时取剩余的一半。
// 补采等批次不设上限。
func fieldFetchContext(ctx context.Context, realtime bool) (context.Context, context.CancelFunc) {
	deadline, ok := ctx.Deadline()
	if !realtime || !ok {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(deadline)
	limit := remaining - fieldAwaitReserve
	if limit < remaining/2 {
		limit = remaining / 2
	}
	return context.WithTimeout(ctx, limit)
}

// fillExtraFields 把取到的值填进每一根；来源没有值的根、字段留空，照常写行。
func fillExtraFields(bars []marketdata.NormalizedKline, values map[int64]map[string]marketdata.FieldValue) {
	for index := range bars {
		if extra := values[bars[index].BarStart.UnixNano()]; len(extra) > 0 {
			bars[index].Extra = extra
		}
	}
}

// klineSpan 返回一组 K 线覆盖的 [最早开盘, 最晚收盘)。
func klineSpan(bars []marketdata.NormalizedKline) (time.Time, time.Time) {
	var start, end time.Time
	for _, bar := range bars {
		if start.IsZero() || bar.BarStart.Before(start) {
			start = bar.BarStart
		}
		if bar.BarEnd.After(end) {
			end = bar.BarEnd
		}
	}
	return start, end
}

// extraFieldValues 把一根 K 线的附加字段转成行字段，按字段名排序（SCF、stream、补采写出的行要逐字节一致）。
func extraFieldValues(extra map[string]marketdata.FieldValue) []*storagepb.FieldValue {
	ids := make([]string, 0, len(extra))
	for id := range extra {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fields := make([]*storagepb.FieldValue, 0, len(ids))
	for _, id := range ids {
		if value := extra[id]; value.Type == marketdata.FieldTypeInt {
			fields = append(fields, intValue(id, value.Int))
		} else {
			fields = append(fields, doubleValue(id, value.Double))
		}
	}
	return fields
}

// failedField 返回 selected 里没取到的第一个附加字段（按字段名排序）及原因；都取到了返回空。
func failedField(selected []string, failed map[string]error) (string, error) {
	if len(failed) == 0 {
		return "", nil
	}
	var ids []string
	for _, field := range selected {
		if field = strings.ToLower(strings.TrimSpace(field)); failed[field] != nil {
			ids = append(ids, field)
		}
	}
	if len(ids) == 0 {
		return "", nil
	}
	sort.Strings(ids)
	return ids[0], failed[ids[0]]
}

// fieldFailure 是附加字段没取到时这一项的结果：可重试的 provider_error（没有来源时是不重试的配置错误）。
// 不能把这几根丢掉照常报成功，否则补采复查会把它们登记成缺失、之后不再补。
func fieldFailure(item domain.CollectionItem, fieldID string, cause error) domain.ItemResult {
	if errors.Is(cause, errFieldUnsupported) {
		return failureResult(item, domain.ItemOutcomeInvalid, fieldID, fmt.Errorf("附加字段 %s：%w", fieldID, cause))
	}
	return failureResult(item, domain.ItemOutcomeProviderError, fieldID, fmt.Errorf("附加字段 %s 取数失败：%w", fieldID, cause))
}

// targetOutputFields 返回写入目标的输出字段：目标自己给出的优先，否则用取数项的。
func targetOutputFields(item domain.CollectionItem, target domain.WriteTarget) []string {
	if target.OutputFields != "" {
		var selected []string
		if json.Unmarshal([]byte(target.OutputFields), &selected) == nil {
			return selected
		}
	}
	return item.OutputFields
}
