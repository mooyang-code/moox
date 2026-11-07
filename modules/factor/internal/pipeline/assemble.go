package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/seriesset"
)

type resultKey struct {
	subject string
	at      time.Time
	tag     string
}

type assembledRow struct {
	key    resultKey
	fields map[string]any
	// params 是这一行各成员实际用的参数（成员键 → 参数名 → 取值），最后编码成 __params 列写出。
	params map[string]map[string]float64
}

// Assemble 把计算结果组装成要写入的行；逐项结果见 AssembleWithReport。
func (r *Runner) Assemble(plan Plan, loaded LoadResult, computation Computation) ([]storageio.ResultRow, error) {
	rows, _, err := r.AssembleWithReport(plan, loaded, computation)
	return rows, err
}

// AssembleWithReport 在 Assemble 之外返回逐 (因子, 标的, 周期) 的结果，补算与填充据此确认哪些项已取得有效值。
func (r *Runner) AssembleWithReport(plan Plan, loaded LoadResult, computation Computation) ([]storageio.ResultRow, AssembleReport, error) {
	if r == nil || r.clock == nil {
		return nil, AssembleReport{}, fmt.Errorf("factor period clock is required")
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return nil, AssembleReport{}, err
	}
	applied := make(map[ItemKey]bool)
	appliedSeries := make(map[seriesItem]bool) // 统一数据集：每条序列各自取得有效值的项；单序列计划不记
	multi := plan.multi()
	markApplied := func(member domain.MemberDef, subject, tag string, at time.Time) {
		applied[ItemKey{Member: member.Key, Subject: subject, Period: at.UTC()}] = true
		if multi {
			appliedSeries[seriesItem{ItemKey: ItemKey{Member: member.Key, Subject: subject, Period: at.UTC()}, tag: tag}] = true
		}
	}
	failedItems := make(map[ItemKey]bool) // 任一序列失败的项：不能因为另一序列成功就确认
	rows := make(map[resultKey]*assembledRow)
	factorColumns := make([]string, 0)
	columnSet := make(map[string]struct{})
	for _, member := range plan.Members {
		for _, column := range member.OutputColumns() {
			if _, exists := columnSet[column]; exists {
				return nil, AssembleReport{}, fmt.Errorf("duplicate factor result column %q", column)
			}
			columnSet[column] = struct{}{}
		}
		factorColumns = append(factorColumns, member.OutputColumns()...)
	}
	carryColumns := append([]string(nil), plan.CarryColumns...)
	if !plan.WriteCarry {
		carryColumns = nil
	}
	for subject, frame := range loaded.Frames {
		if frame == nil {
			continue
		}
		positions, positionErr := framePositions(frame, carryColumns)
		if positionErr != nil {
			return nil, AssembleReport{}, fmt.Errorf("source frame for %q: %w", subject, positionErr)
		}
		for rowNo, values := range frame.Rows {
			if len(values) != len(frame.Columns) {
				return nil, AssembleReport{}, fmt.Errorf("source frame for %q row %d has %d values for %d columns", subject, rowNo, len(values), len(frame.Columns))
			}
			at, timeErr := parseDataTime(values[positions["data_time"]])
			if timeErr != nil {
				return nil, AssembleReport{}, fmt.Errorf("source frame for %q row %d data_time: %w", subject, rowNo, timeErr)
			}
			tagValue, tagOK := values[positions["series_tag"]].(string)
			if !inTargetRange(plan, target, at) {
				continue
			}
			if !tagOK {
				return nil, AssembleReport{}, fmt.Errorf("source frame for %q row %d has invalid series_tag", subject, rowNo)
			}
			key := resultKey{subject: subject, at: at.UTC(), tag: tagValue}
			fields := make(map[string]any, len(carryColumns)+len(factorColumns))
			for _, column := range carryColumns {
				fields[column] = values[positions[column]]
			}
			// 只填充时不预置因子列：没有取得有效值的因子列不写，不会把已有的值覆盖成 NULL。__params 先取已有行的记录，
			// 再写入本次计算的成员实际用的参数：只填充时随取得有效值的成员一起写，其余情况预置全部成员。
			rowParams := cloneRowParams(plan.BaseParams[RowKey{Subject: subject, At: at.UTC(), Tag: tagValue}])
			if !plan.FillOnly {
				for _, column := range factorColumns {
					fields[column] = nil
				}
				for _, member := range plan.Members {
					rowParams[member.Key] = MemberParamValues(plan, member, subject)
				}
			}
			if _, exists := rows[key]; exists {
				return nil, AssembleReport{}, fmt.Errorf("duplicate source row for subject %q at %s tag %q", subject, at, tagValue)
			}
			rows[key] = &assembledRow{key: key, fields: fields, params: rowParams}
		}
	}

	failedFactorSubjects := make(map[string]map[failTarget]struct{})
	// failSubject 记录成员在整个标的上失败：单序列数据集标的的全部行写 NULL；统一数据集按这个标的的各条可用序列分别记失败。
	failSubject := func(member domain.MemberDef, subject string) {
		if !plan.multi() {
			markFactorSubjectFailure(computation, failedFactorSubjects, member, subject)
			return
		}
		for _, item := range loaded.AvailableSeries {
			if item.SubjectID == subject {
				markFactorSeriesFailure(computation, failedFactorSubjects, member, item)
			}
		}
	}
	// failApply 记录一行结果值无效：单序列数据集标的整体失败（原有行为）；统一数据集只记这条序列，不连累另一个市场。
	failApply := func(member domain.MemberDef, subject, tag string) {
		if plan.multi() {
			markFactorSeriesFailure(computation, failedFactorSubjects, member, seriesset.Series{SubjectID: subject, SeriesTag: tag})
			return
		}
		markFactorSubjectFailure(computation, failedFactorSubjects, member, subject)
	}
	for _, member := range plan.Members {
		bySubject := computation.Results[member.Key]
		if member.Factor.FactorType == domain.FactorTypeCrossSection {
			if item, ok := bySubject[""]; ok {
				if item.Err != nil {
					for _, subject := range plan.Available {
						failSubject(member, subject)
					}
					continue
				}
				if err := validateResultColumns(item, member, true); err != nil {
					for _, subject := range plan.Available {
						failSubject(member, subject)
					}
					continue
				}
				for rowNo, result := range item.Rows {
					at, tag, subject, outputs, decodeErr := decodeResultRow(result, true)
					if decodeErr != nil {
						return nil, AssembleReport{}, fmt.Errorf("member %q cross-section result row %d: %w", member.Key, rowNo, decodeErr)
					}
					if !subjectIn(plan.Available, subject) {
						return nil, AssembleReport{}, fmt.Errorf("member %q returned unexpected subject %q", member.Key, subject)
					}
					if !inTargetRange(plan, target, at) {
						continue
					}
					if applyFactorValues(plan, rows, resultKey{subject: subject, at: at.UTC(), tag: tag}, member, outputs, factorColumns) {
						markApplied(member, subject, tag, at)
					} else {
						failedItems[ItemKey{Member: member.Key, Subject: subject, Period: at.UTC()}] = true
						failApply(member, subject, tag)
					}
				}
			}
			continue
		}

		for subject, item := range bySubject {
			if item.Err != nil {
				failSubject(member, subject)
				continue
			}
			if err := validateResultColumns(item, member, false); err != nil {
				failSubject(member, subject)
				continue
			}
			for rowNo, result := range item.Rows {
				at, tag, _, outputs, decodeErr := decodeResultRow(result, false)
				if decodeErr != nil {
					return nil, AssembleReport{}, fmt.Errorf("member %q subject %q result row %d: %w", member.Key, subject, rowNo, decodeErr)
				}
				if !inTargetRange(plan, target, at) {
					continue
				}
				if applyFactorValues(plan, rows, resultKey{subject: subject, at: at.UTC(), tag: tag}, member, outputs, factorColumns) {
					markApplied(member, subject, tag, at)
				} else {
					failedItems[ItemKey{Member: member.Key, Subject: subject, Period: at.UTC()}] = true
					failApply(member, subject, tag)
				}
			}
		}
	}

	for _, member := range plan.Members {
		// 整因子跳过的成员（严格全集的截面因子缺标的）没有任何结果项，输出列保持预置的 NULL：它在每一行都没有取得值，也就没有实际用的参数。
		if !plan.FillOnly && computation.FactorStates[member.Key].Status == "skipped" {
			for _, row := range rows {
				delete(row.params, member.Key)
			}
		}
		// 统一数据集里只跳过一部分 series_tag（成员状态 degraded）：只去掉这些 tag 的行的参数，输出列同样保持预置的 NULL。
		if skipped := computation.FactorStates[member.Key].SkippedSeriesTags; len(skipped) > 0 && !plan.FillOnly {
			for key, row := range rows {
				if tagIn(skipped, key.tag) {
					delete(row.params, member.Key)
				}
			}
		}
		// 期望序列是数据层推测的（补算、失效重算）：被跳过的 tag 不能确定真的缺数（可能只是晚到或刚下架），
		// 不把这些行写成 NULL，输出列与 __params 保持已有的值，不覆盖实时路径算好的结果。
		if plan.ExpectedSeriesDerived && !plan.FillOnly {
			state := computation.FactorStates[member.Key]
			for key, row := range rows {
				if state.Status != "skipped" && !tagIn(state.SkippedSeriesTags, key.tag) {
					continue
				}
				for _, column := range member.OutputColumns() {
					delete(row.fields, column)
				}
				if base, ok := plan.BaseParams[RowKey{Subject: key.subject, At: key.at, Tag: key.tag}][member.Key]; ok {
					row.params[member.Key] = base
				}
			}
		}
		failedSubjects := failedFactorSubjects[member.Key]
		if len(failedSubjects) == 0 || plan.FillOnly {
			continue
		}
		for key, row := range rows {
			_, wholeSubject := failedSubjects[failTarget{subject: key.subject}]
			_, thisSeries := failedSubjects[failTarget{subject: key.subject, tag: key.tag}]
			if !wholeSubject && !thisSeries {
				continue
			}
			for _, column := range member.OutputColumns() {
				row.fields[column] = nil
			}
			// 失败的成员这一行写 NULL，没有取得值，也就没有“实际用的参数”：从 __params 里去掉它，其余成员的记录不动。
			delete(row.params, member.Key)
		}
	}

	out := make([]storageio.ResultRow, 0, len(rows))
	for _, row := range rows {
		if len(row.fields) == 0 {
			continue // 只填充时没有取得任何有效值的行不写
		}
		// 非只填充时成员的参数全被去掉也要写出 {}：Storage 只更新请求带的字段，不写就会留下已有行的旧 __params。
		if len(row.params) > 0 || (!plan.FillOnly && len(plan.Members) > 0) {
			encoded, encodeErr := factorname.CanonicalParams(row.params)
			if encodeErr != nil {
				return nil, AssembleReport{}, fmt.Errorf("encode %s of subject %q at %s: %w", factorname.ParamsColumn, row.key.subject, row.key.at, encodeErr)
			}
			row.fields[factorname.ParamsColumn] = storageio.JSONText(encoded)
		}
		out = append(out, storageio.ResultRow{SubjectID: row.key.subject, DataTime: row.key.at, SeriesTag: row.key.tag, Fields: row.fields})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectID != out[j].SubjectID {
			return out[i].SubjectID < out[j].SubjectID
		}
		if !out[i].DataTime.Equal(out[j].DataTime) {
			return out[i].DataTime.Before(out[j].DataTime)
		}
		return out[i].SeriesTag < out[j].SeriesTag
	})
	if plan.multi() {
		r.failUnavailableSeriesItems(plan, loaded, computation, appliedSeries, failedItems)
	}
	for key := range failedItems {
		delete(applied, key)
	}
	report, reportErr := r.assembleReport(plan, loaded, computation, applied)
	if reportErr != nil {
		return nil, AssembleReport{}, reportErr
	}
	return out, report, nil
}

func framePositions(frame *storageio.Frame, carryColumns []string) (map[string]int, error) {
	if len(frame.Columns) < 2 || frame.Columns[0] != "data_time" || frame.Columns[1] != "series_tag" {
		return nil, fmt.Errorf("columns must start with data_time, series_tag")
	}
	positions := make(map[string]int, len(frame.Columns))
	for index, name := range frame.Columns {
		if _, duplicate := positions[name]; duplicate {
			return nil, fmt.Errorf("duplicate column %q", name)
		}
		positions[name] = index
	}
	for _, name := range carryColumns {
		if _, ok := positions[name]; !ok {
			return nil, fmt.Errorf("carry column %q is missing", name)
		}
	}
	return positions, nil
}

func inTargetRange(plan Plan, liveTarget, at time.Time) bool {
	if plan.Mode == ModeLive {
		return at.Equal(liveTarget)
	}
	return !at.Before(plan.TargetStart) && at.Before(plan.TargetEnd)
}

func parseDataTime(value any) (time.Time, error) {
	switch item := value.(type) {
	case time.Time:
		if item.IsZero() {
			return time.Time{}, fmt.Errorf("time is zero")
		}
		return item.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, item)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.UTC(), nil
	case int64:
		return time.Unix(item, 0).UTC(), nil
	case int:
		return time.Unix(int64(item), 0).UTC(), nil
	case float64:
		if math.IsNaN(item) || math.IsInf(item, 0) {
			return time.Time{}, fmt.Errorf("invalid epoch time")
		}
		seconds, fraction := math.Modf(item)
		return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unexpected time type %T", value)
	}
}

func validateResultColumns(item ItemResult, member domain.MemberDef, cross bool) error {
	want := []string{"data_time", "series_tag"}
	if cross {
		want = append(want, "subject_id")
	}
	want = append(want, member.OutputColumns()...)
	if len(item.Columns) != len(want) {
		return fmt.Errorf("got %d columns, want %d", len(item.Columns), len(want))
	}
	for i, name := range want {
		if item.Columns[i] != name {
			return fmt.Errorf("column %d is %q, want %q", i, item.Columns[i], name)
		}
	}
	for rowNo, values := range item.Rows {
		if len(values) != len(want) {
			return fmt.Errorf("row %d has %d values, want %d", rowNo, len(values), len(want))
		}
	}
	return nil
}

func decodeResultRow(values []any, cross bool) (time.Time, string, string, []any, error) {
	at, err := parseDataTime(values[0])
	if err != nil {
		return time.Time{}, "", "", nil, err
	}
	tag, ok := values[1].(string)
	if !ok {
		return time.Time{}, "", "", nil, fmt.Errorf("series_tag has type %T", values[1])
	}
	index := 2
	subject := ""
	if cross {
		var valid bool
		subject, valid = values[index].(string)
		if !valid || subject == "" {
			return time.Time{}, "", "", nil, fmt.Errorf("subject_id is invalid")
		}
		index++
	}
	return at, tag, subject, values[index:], nil
}

func applyFactorValues(plan Plan, rows map[resultKey]*assembledRow, key resultKey, member domain.MemberDef, values []any,
	allFactorColumns []string) bool {
	valid := true
	for _, value := range values {
		if _, ok := numericOrNull(value); !ok {
			valid = false
			break
		}
	}
	if !valid && plan.FillOnly {
		return false // 只填充：无效值不写，更不能把已有的值覆盖成 NULL
	}
	row, exists := rows[key]
	if !exists {
		row = &assembledRow{key: key, fields: make(map[string]any), params: cloneRowParams(plan.BaseParams[RowKey{Subject: key.subject, At: key.at, Tag: key.tag}])}
		if !plan.FillOnly {
			for _, column := range allFactorColumns {
				row.fields[column] = nil
			}
			for _, planned := range plan.Members {
				row.params[planned.Key] = MemberParamValues(plan, planned, key.subject)
			}
		}
		rows[key] = row
	}
	columns := member.OutputColumns()
	if !valid {
		for _, column := range columns {
			row.fields[column] = nil
		}
		return false
	}
	for i, column := range columns {
		row.fields[column] = normalizeNumeric(values[i])
	}
	row.params[member.Key] = MemberParamValues(plan, member, key.subject)
	return true
}

// MemberParamValues 返回成员在标的 subject 上本次计算实际用的参数（参数名 → 取值）；没有参数的成员是空对象。
func MemberParamValues(plan Plan, member domain.MemberDef, subject string) map[string]float64 {
	names := member.ParamNames()
	values := make(map[string]float64, len(names))
	if len(names) == 0 {
		return values
	}
	snapshot := snapshotOf(plan, member)
	for _, name := range names {
		values[name] = snapshot.Value(name, subject)
	}
	return values
}

func cloneRowParams(base map[string]map[string]float64) map[string]map[string]float64 {
	out := make(map[string]map[string]float64, len(base))
	for key, values := range base {
		out[key] = values
	}
	return out
}

func numericOrNull(value any) (float64, bool) {
	switch item := value.(type) {
	case nil:
		return 0, true
	case int:
		return float64(item), true
	case int8:
		return float64(item), true
	case int16:
		return float64(item), true
	case int32:
		return float64(item), true
	case int64:
		return float64(item), true
	case uint:
		return float64(item), true
	case uint8:
		return float64(item), true
	case uint16:
		return float64(item), true
	case uint32:
		return float64(item), true
	case uint64:
		return float64(item), true
	case float32:
		return float64(item), true
	case float64:
		return item, true
	case json.Number:
		parsed, err := strconv.ParseFloat(string(item), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeNumeric(value any) any {
	parsed, ok := numericOrNull(value)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil
	}
	if _, isFloat := value.(float32); isFloat {
		return parsed
	}
	if _, isFloat := value.(float64); isFloat {
		return parsed
	}
	if _, isNumber := value.(json.Number); isNumber {
		return parsed
	}
	return value
}

// failTarget 是成员失败的范围：tag 为空表示这个标的的全部序列（单序列数据集的原有口径），否则只是这一条序列。
type failTarget struct{ subject, tag string }

// markFactorSeriesFailure 记录成员在统一数据集的一条序列上失败：只把这条序列的行写成 NULL，另一个 tag 的行不动。
func markFactorSeriesFailure(computation Computation, failed map[string]map[failTarget]struct{}, member domain.MemberDef, series seriesset.Series) {
	if failed[member.Key] == nil {
		failed[member.Key] = make(map[failTarget]struct{})
	}
	failed[member.Key][failTarget{subject: series.SubjectID, tag: series.SeriesTag}] = struct{}{}
	state := computation.FactorStates[member.Key]
	state.MemberKey, state.MemberKind, state.FactorID = member.Key, member.Kind, member.Factor.FactorID
	state.Status = "degraded"
	state.SourceHash = member.Factor.SpecHash
	state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, series.SubjectID))
	state.FailedSeries = uniqueSeriesSorted(append(state.FailedSeries, series))
	computation.FactorStates[member.Key] = state
}

// seriesItem 是统一数据集里（成员、标的、周期、series_tag）一条序列上的结果项。
type seriesItem struct {
	ItemKey
	tag string
}

// failUnavailableSeriesItems 统一数据集：这个 (成员, 标的, 周期) 要期望且可用的每条序列在该周期都取得有效值才算写成，
// 缺一条（序列整段缺数、所在 tag 被跳过、或这一周期那条序列没有行/没有取得值）就记为失败——
// 不能因为另一个市场的序列成功就确认（补算与缺口恢复据此保留待办），与单序列数据集“缺这一根就是失败”同一口径。
func (r *Runner) failUnavailableSeriesItems(plan Plan, loaded LoadResult, computation Computation, appliedSeries map[seriesItem]bool, failedItems map[ItemKey]bool) {
	targets, err := r.itemTargets(plan)
	if err != nil {
		return
	}
	missing := seriesSet(differenceSeries(plan.ExpectedSeries, loaded.AvailableSeries))
	for _, member := range plan.Members {
		skipped := computation.FactorStates[member.Key].SkippedSeriesTags
		for _, item := range plan.ExpectedSeries {
			_, isMissing := missing[item]
			unavailable := isMissing || tagIn(skipped, item.SeriesTag)
			for _, target := range targets {
				key := ItemKey{Member: member.Key, Subject: item.SubjectID, Period: target.UTC()}
				if unavailable || !appliedSeries[seriesItem{ItemKey: key, tag: item.SeriesTag}] {
					failedItems[key] = true
				}
			}
		}
	}
}

func tagIn(tags []string, tag string) bool {
	for _, item := range tags {
		if item == tag {
			return true
		}
	}
	return false
}

func markFactorSubjectFailure(computation Computation, failed map[string]map[failTarget]struct{}, member domain.MemberDef, subject string) {
	if failed[member.Key] == nil {
		failed[member.Key] = make(map[failTarget]struct{})
	}
	failed[member.Key][failTarget{subject: subject}] = struct{}{}
	state := computation.FactorStates[member.Key]
	state.MemberKey, state.MemberKind, state.FactorID = member.Key, member.Kind, member.Factor.FactorID
	state.Status = "degraded"
	state.SourceHash = member.Factor.SpecHash
	state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, subject))
	computation.FactorStates[member.Key] = state
}

func subjectIn(subjects []string, wanted string) bool {
	for _, subject := range subjects {
		if subject == wanted {
			return true
		}
	}
	return false
}

// AssembleReport 是补算与填充的逐 (因子, 标的, 周期) 结果。
type AssembleReport struct {
	Items map[ItemKey]ItemStatus
}

// assembleReport 汇总每个 (因子, 标的, 周期) 的结果：取得有效值 written；其余（计算错误、无效值、没有源行）failed。
func (r *Runner) assembleReport(plan Plan, loaded LoadResult, computation Computation, applied map[ItemKey]bool) (AssembleReport, error) {
	targets, err := r.itemTargets(plan)
	if err != nil {
		return AssembleReport{}, err
	}
	report := AssembleReport{Items: make(map[ItemKey]ItemStatus)}
	subjects := intersectLists(plan.Expected, loaded.Available)
	for _, member := range plan.Members {
		for _, target := range targets {
			for _, subject := range subjects {
				key := ItemKey{Member: member.Key, Subject: subject, Period: target.UTC()}
				switch {
				case applied[key]:
					report.Items[key] = ItemWritten
				default:
					report.Items[key] = ItemFailed
				}
			}
		}
	}
	return report, nil
}

// itemTargets 是本次计划要汇报的目标周期。
func (r *Runner) itemTargets(plan Plan) ([]time.Time, error) {
	if plan.Mode == ModeRecalc {
		return periodRange(r.clock, plan.TargetStart, plan.TargetEnd, plan.Set.Freq)
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return nil, err
	}
	return []time.Time{target}, nil
}
