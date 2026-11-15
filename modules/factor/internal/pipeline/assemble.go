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
	// params 是这一行各可变成员实际用的参数（成员键 → 参数名 → 取值），最后编码成 __params 列写出；固定成员的参数就是它自己的
	// c_params，不记。
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
	// appliedSeries 记每个 (成员, 标的, 周期) 上哪些期望序列取得了有效值：第 i 位是 tagsOf[标的] 里第 i 个 tag。
	// 逐项结果只从它推出；按项而不按（项、tag）建表，大补算块里这张表有上千万项，键越小越省内存。
	tagsOf := tagsBySubject(plan.ExpectedSeries)
	appliedSeries := make(map[ItemKey]uint64)
	markApplied := func(member domain.MemberDef, subject, tag string, at time.Time) {
		if bit := tagBit(tagsOf[subject], tag); bit != 0 {
			appliedSeries[ItemKey{Member: member.Key, Subject: subject, Period: at.UTC()}] |= bit
		}
	}
	failedItems := make(map[ItemKey]bool) // 有序列取得无效值的项：不能因为另一序列成功就确认
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
			// 再写入本次计算的可变成员实际用的参数：只填充时随取得有效值的成员一起写，其余情况预置全部可变成员。
			rowParams := cloneRowParams(plan.BaseParams[RowKey{Subject: subject, At: at.UTC(), Tag: tagValue}])
			if !plan.FillOnly {
				for _, column := range factorColumns {
					fields[column] = nil
				}
				presetParams(plan, rowParams, subject)
			}
			if _, exists := rows[key]; exists {
				return nil, AssembleReport{}, fmt.Errorf("duplicate source row for subject %q at %s tag %q", subject, at, tagValue)
			}
			rows[key] = &assembledRow{key: key, fields: fields, params: rowParams}
		}
	}

	failedFactorSeries := make(map[string]map[seriesset.Series]struct{})
	// failSubject 记录成员在整个标的上失败：按这个标的的各条可用序列分别记失败。
	failSubject := func(member domain.MemberDef, subject string) {
		for _, item := range loaded.AvailableSeries {
			if item.SubjectID == subject {
				markFactorSeriesFailure(computation, failedFactorSeries, member, item)
			}
		}
	}
	// failApply 记录一行结果值无效：只记这条序列，不连累同一标的的另一个市场。
	failApply := func(member domain.MemberDef, subject, tag string) {
		markFactorSeriesFailure(computation, failedFactorSeries, member, seriesset.Series{SubjectID: subject, SeriesTag: tag})
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
		// 只跳过一部分 series_tag（统一数据集才会出现，成员状态 degraded）：只去掉这些 tag 的行的参数，输出列同样保持预置的 NULL。
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
				if base, ok := plan.BaseParams[RowKey{Subject: key.subject, At: key.at, Tag: key.tag}][member.Key]; ok && member.IsVar() {
					row.params[member.Key] = base
				}
			}
		}
		// 上游判为失败的序列（事件的 failed_series）：本地即使已有这一期的源行，这条序列的结果也写 NULL、不记参数
		// （一个 tag 失败，只把这条序列的行写成 NULL）。计算时它们已不进面板；这里只记失败范围，
		// 不改成员状态（失败序列已在 Compute 里记进成员状态，截面因子可能因此是 skipped，不能被改回 degraded）。
		if !plan.FillOnly && len(plan.UpstreamFailedSeries) > 0 {
			if failedFactorSeries[member.Key] == nil {
				failedFactorSeries[member.Key] = make(map[seriesset.Series]struct{})
			}
			for _, item := range plan.UpstreamFailedSeries {
				failedFactorSeries[member.Key][item] = struct{}{}
			}
		}
		failedSeries := failedFactorSeries[member.Key]
		if len(failedSeries) == 0 || plan.FillOnly {
			continue
		}
		for key, row := range rows {
			if _, failed := failedSeries[seriesset.Series{SubjectID: key.subject, SeriesTag: key.tag}]; !failed {
				continue
			}
			for _, column := range member.OutputColumns() {
				row.fields[column] = nil
			}
			// 失败的成员这一行写 NULL，没有取得值，也就没有“实际用的参数”：从 __params 里去掉它，其余成员的记录不动。
			delete(row.params, member.Key)
		}
	}

	// __params 只记可变成员：本次计算没有可变成员时不写这一列（只补固定成员时，Storage 只更新请求带的字段，可变成员的原值不动）。
	writeParams := hasVarMember(plan.Members)
	out := make([]storageio.ResultRow, 0, len(rows))
	for _, row := range rows {
		if len(row.fields) == 0 {
			continue // 只填充时没有取得任何有效值的行不写
		}
		// 非只填充时可变成员的参数全被去掉也要写出 {}：Storage 只更新请求带的字段，不写就会留下已有行的旧 __params。
		if writeParams && (len(row.params) > 0 || !plan.FillOnly) {
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
	report, reportErr := r.assembleReport(plan, loaded, computation, tagsOf, appliedSeries, failedItems)
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
			presetParams(plan, row.params, key.subject)
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
	if member.IsVar() {
		row.params[member.Key] = MemberParamValues(plan, member, key.subject)
	}
	return true
}

// presetParams 预置一行里本次计算的各可变成员实际用的参数（固定成员不记）。
func presetParams(plan Plan, params map[string]map[string]float64, subject string) {
	for _, member := range plan.Members {
		if member.IsVar() {
			params[member.Key] = MemberParamValues(plan, member, subject)
		}
	}
}

// hasVarMember 报告成员里有没有可变成员。
func hasVarMember(members []domain.MemberDef) bool {
	for _, member := range members {
		if member.IsVar() {
			return true
		}
	}
	return false
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

// markFactorSeriesFailure 记录成员在一条序列上失败：只把这条序列的行写成 NULL，同一标的另一个 tag 的行不动。
func markFactorSeriesFailure(computation Computation, failed map[string]map[seriesset.Series]struct{}, member domain.MemberDef, series seriesset.Series) {
	if failed[member.Key] == nil {
		failed[member.Key] = make(map[seriesset.Series]struct{})
	}
	failed[member.Key][series] = struct{}{}
	state := computation.FactorStates[member.Key]
	state.MemberKey, state.MemberKind, state.FactorID = member.Key, member.Kind, member.Factor.FactorID
	state.Status = "degraded"
	state.SourceHash = member.Factor.SpecHash
	state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, series.SubjectID))
	state.FailedSeries = uniqueSeriesSorted(append(state.FailedSeries, series))
	computation.FactorStates[member.Key] = state
}

// tagBit 返回 tag 在标的期望 tag 列表里的位；不是期望的 tag（不进确认）或超出 64 个时为 0。
func tagBit(tags []string, tag string) uint64 {
	for i, candidate := range tags {
		if candidate == tag && i < 64 {
			return 1 << uint(i)
		}
	}
	return 0
}

func tagIn(tags []string, tag string) bool {
	for _, item := range tags {
		if item == tag {
			return true
		}
	}
	return false
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

// assembleReport 汇总每个 (因子, 标的, 周期) 的结果。这个标的的每条期望序列都可用（本期有数据、所在 tag 没被跳过）、
// 在这一周期都取得有效值（appliedSeries 里这些位都在），并且没有序列取得无效值（不在 failedItems 里），才是 written；
// 缺一条（序列整段缺数、所在 tag 被跳过、这一周期那条序列没有行或没取得值）就是 failed——
// 统一数据集上不能因为另一个市场的序列成功就确认（补算与缺口恢复据此保留待办）。
func (r *Runner) assembleReport(plan Plan, loaded LoadResult, computation Computation, tagsOf map[string][]string,
	appliedSeries map[ItemKey]uint64, failedItems map[ItemKey]bool) (AssembleReport, error) {
	targets, err := r.itemTargets(plan)
	if err != nil {
		return AssembleReport{}, err
	}
	report := AssembleReport{Items: make(map[ItemKey]ItemStatus)}
	subjects := intersectLists(plan.Expected, loaded.Available)
	missing := seriesSet(differenceSeries(plan.ExpectedSeries, loaded.AvailableSeries))
	for _, member := range plan.Members {
		skipped := computation.FactorStates[member.Key].SkippedSeriesTags
		for _, subject := range subjects {
			// 有期望序列整段不可用的标的，这个成员在所有周期都没有写成；期望 tag 超过 64 个的标的确认不了（不会出现）。
			tags := tagsOf[subject]
			usable := len(tags) > 0 && len(tags) <= 64
			for _, tag := range tags {
				if _, gone := missing[seriesset.Series{SubjectID: subject, SeriesTag: tag}]; gone || tagIn(skipped, tag) {
					usable = false
					break
				}
			}
			for _, target := range targets {
				key := ItemKey{Member: member.Key, Subject: subject, Period: target.UTC()}
				all := uint64(1)<<uint(len(tags)) - 1 // len(tags) == 64 时移位得 0，减 1 正好是全 1
				if written := usable && !failedItems[key] && appliedSeries[key]&all == all; written {
					report.Items[key] = ItemWritten
				} else {
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
