package input

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/packages/events"
)

// DefaultCalendar 是没有声明日历的数据集使用的日历。
const DefaultCalendar = "crypto_24x7"

// datasetRoleFactorResult 是 Factor 写入的结果数据集角色：它的周期完成由 factor_period.computed 宣布。
// 其余带 source_dataset_id 的数据集（例如 Collector 的重采样 K 线）仍由 collector.period.completed 宣布。
const datasetRoleFactorResult = "factor_result"

// Resolve 在启用时解析绑定：View → 数据集 → 源数据集市场类型 → 列与因子指纹，并编译策略。
func Resolve(ctx context.Context, client Client, spaceID, viewID string, strategy dsl.Strategy) (Resolved, *dsl.Program, error) {
	if client == nil {
		return Resolved{}, nil, errors.New("Storage 客户端未配置")
	}
	if strings.TrimSpace(viewID) == "" {
		return Resolved{}, nil, errors.New("view_id 不能为空")
	}
	if err := dsl.Validate(&strategy); err != nil {
		return Resolved{}, nil, err
	}
	view, err := client.GetView(ctx, spaceID, viewID)
	if err != nil {
		return Resolved{}, nil, err
	}
	if view.DatasetID == "" || view.Frequency == "" {
		return Resolved{}, nil, fmt.Errorf("View %s 没有数据集或频率", viewID)
	}
	if view.Status != "" && view.Status != "active" {
		return Resolved{}, nil, fmt.Errorf("View %s 的状态是 %s，只能绑定 active 的 View", viewID, view.Status)
	}
	if strategy.Bar != "" && !strings.EqualFold(strategy.Bar, view.Frequency) {
		return Resolved{}, nil, fmt.Errorf("DSL 的 bar 断言为 %s，但 View %s 的周期是 %s", strategy.Bar, viewID, view.Frequency)
	}
	dataset, err := client.GetDataset(ctx, spaceID, view.DatasetID)
	if err != nil {
		return Resolved{}, nil, err
	}
	resolved := Resolved{ViewID: viewID, DatasetID: view.DatasetID, Bar: strings.ToLower(view.Frequency), Columns: map[string]ColumnBinding{}, Factors: map[string]string{}, MinAgeBars: strategy.Universe.MinAgeBars}
	resolved.Calendar = strings.ToLower(strings.TrimSpace(dataset.Attributes["calendar"]))
	if resolved.Calendar == "" {
		resolved.Calendar = DefaultCalendar
	}
	source := dataset
	if sourceID := strings.TrimSpace(dataset.Attributes["source_dataset_id"]); sourceID != "" && sourceID != dataset.DatasetID {
		source, err = client.GetDataset(ctx, spaceID, sourceID)
		if err != nil {
			return Resolved{}, nil, err
		}
		resolved.SourceDatasetID = sourceID
		if calendar := strings.ToLower(strings.TrimSpace(source.Attributes["calendar"])); calendar != "" {
			resolved.Calendar = calendar
		}
	}
	marketType, err := sourceMarketType(ctx, client, spaceID, source)
	if err != nil {
		return Resolved{}, nil, err
	}
	resolved.MarketType = marketType
	resolved.Spot = marketType == "spot"
	if _, err := FromBarEnd(resolved.Calendar, resolved.Bar, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		return Resolved{}, nil, fmt.Errorf("日历 %s 与周期 %s 的组合不受支持：%w", resolved.Calendar, resolved.Bar, err)
	}
	if resolved.Spot {
		if strategy.Portfolio.Leverage.Cmp(quant.One()) > 0 {
			return Resolved{}, nil, fmt.Errorf("现货 View 不允许 portfolio.leverage 大于 1（当前 %s）", strategy.Portfolio.Leverage.String())
		}
		for _, rule := range strategy.Rules {
			if rule.Side == dsl.SideShort {
				return Resolved{}, nil, fmt.Errorf("现货 View 不允许规则 %s 做空", rule.ID)
			}
		}
	}
	if bars, ok := retentionBars(dataset.Retention, resolved.Bar); ok {
		if strategy.Universe.MinAgeBars > bars {
			return Resolved{}, nil, fmt.Errorf("universe.min_age_bars=%d 超过 View 的保留根数 %d", strategy.Universe.MinAgeBars, bars)
		}
	}
	columns := make(map[string]ViewColumn, len(view.Columns))
	for _, column := range view.Columns {
		columns[column.Name] = column
		resolved.ViewColumns = append(resolved.ViewColumns, column.Name)
	}
	sortStrings(resolved.ViewColumns)
	if strategy.Universe.MinAgeBars > 0 {
		if _, ok := columns[ageProbeColumn]; !ok {
			return Resolved{}, nil, fmt.Errorf("universe.min_age_bars 通过 %s 列判断上市时间，但 View %s 没有该列", ageProbeColumn, viewID)
		}
		if err := checkAgeCoverage(view, resolved, strategy.Universe.MinAgeBars); err != nil {
			return Resolved{}, nil, err
		}
	}
	if err := checkReferences(ctx, client, spaceID, view, strategy); err != nil {
		return Resolved{}, nil, err
	}
	referenced, err := dsl.ReferencedColumns(strategy)
	if err != nil {
		return Resolved{}, nil, err
	}
	missing := make([]string, 0)
	for _, name := range referenced {
		column, ok := columns[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		binding := ColumnBinding{Source: SourceDataset}
		if factorID := strings.TrimSpace(column.Attributes["origin_factor_id"]); factorID != "" {
			binding = ColumnBinding{Source: SourceFactor, FactorID: factorID, FactorOutput: strings.TrimSpace(column.Attributes["factor_output"])}
			hash, ok := resolved.Factors[factorID]
			if !ok {
				factor, err := client.GetFactor(ctx, factorID)
				if err != nil {
					return Resolved{}, nil, err
				}
				if factor.DefinitionHash == "" {
					return Resolved{}, nil, fmt.Errorf("因子 %s 没有 definition_hash，请升级 Factor 管理端", factorID)
				}
				hash = factor.DefinitionHash
				resolved.Factors[factorID] = hash
			}
			binding.DefinitionHash = hash
		}
		resolved.Columns[name] = binding
	}
	if len(missing) > 0 {
		return Resolved{}, nil, fmt.Errorf("列 %s 不存在于 View %s；可用列：%s", strings.Join(missing, "、"), viewID, strings.Join(resolved.ViewColumns, "、"))
	}
	program, err := dsl.Compile(strategy, resolved.ViewColumns)
	if err != nil {
		return Resolved{}, nil, err
	}
	resolved.UsesPreviousBar = program.UsesPreviousBar
	resolved.CompletionKind = events.CollectorPeriodCompleted.Name()
	if strings.TrimSpace(dataset.Attributes["dataset_role"]) == datasetRoleFactorResult {
		resolved.CompletionKind = events.FactorPeriodComputed.Name()
	}
	return resolved, program, nil
}

// checkReferences 校验 DSL 引用的标签存在、固定写出的标的（pool 列表、universe 的 include 与 exclude）
// 在 View 的数据集中有绑定。ID 区分大小写；拼写错误会让池悄悄变空或排除失效，必须在启用时报出。
func checkReferences(ctx context.Context, client Client, spaceID string, view ViewInfo, strategy dsl.Strategy) error {
	tags := make([]string, 0)
	instruments := make([]string, 0)
	tags = append(tags, strategy.Universe.Tags...)
	tags = append(tags, strategy.Universe.ExcludeTags...)
	instruments = append(instruments, strategy.Universe.Include...)
	instruments = append(instruments, strategy.Universe.Exclude...)
	for _, rule := range strategy.Rules {
		tags = append(tags, rule.Pool.Tags...)
		instruments = append(instruments, rule.Pool.Fixed...)
	}
	for _, tag := range uniqueSorted(tags) {
		if _, err := client.GetTag(ctx, spaceID, tag); err != nil {
			if errors.Is(err, ErrTagNotFound) {
				return fmt.Errorf("DSL 引用的标签 %s 不存在（ID 区分大小写）", tag)
			}
			return err
		}
	}
	if len(instruments) == 0 {
		return nil
	}
	subjects, err := client.ListDatasetSubjects(ctx, spaceID, view.DatasetID)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		known[subject.SubjectID] = struct{}{}
	}
	var unknown []string
	for _, id := range uniqueSorted(instruments) {
		if _, ok := known[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("DSL 写出的标的 %s 不在 View %s 的数据集中（ID 区分大小写）", strings.Join(unknown, "、"), view.ViewID)
	}
	return nil
}

// checkAgeCoverage 校验 View 能追溯 min_age_bars：N 不超过每个序列保留的根数，且以最新一根为 T，
// T − (N−1) 根不早于活跃序列的覆盖起点。覆盖范围未知（索引还没有任何行）时无法确认，拒绝启用。
func checkAgeCoverage(view ViewInfo, resolved Resolved, minAgeBars int) error {
	if view.SeriesBars > 0 && minAgeBars > view.SeriesBars {
		return fmt.Errorf("universe.min_age_bars=%d 超过 View %s 每个序列保留的 %d 根，永远无法满足", minAgeBars, resolved.ViewID, view.SeriesBars)
	}
	start := CoverageStart(view, resolved)
	if start.IsZero() {
		return fmt.Errorf("View %s 还没有数据（覆盖范围未知），无法确认 universe.min_age_bars=%d 所需的历史，请等数据写入后再启用", resolved.ViewID, minAgeBars)
	}
	target, err := HistoryStart(resolved.Calendar, resolved.Bar, view.IndexedTo, minAgeBars)
	if err != nil {
		return err
	}
	if target.Before(start) {
		return fmt.Errorf("universe.min_age_bars=%d 需要追溯到 %s，但 View %s 的活跃序列当前只覆盖 %s 至 %s；请减小 min_age_bars 或等待 View 积累更多历史",
			minAgeBars, target.Format(time.RFC3339), resolved.ViewID, start.Format(time.RFC3339), view.IndexedTo.Format(time.RFC3339))
	}
	return nil
}

// sourceMarketType 判定源数据集的市场类型：优先读数据集属性 market_type，否则由其标的范围标签的 market_type 决定；
// 标签缺失、不一致或取值未知都拒绝启用，避免把合约当作现货（或相反）。
func sourceMarketType(ctx context.Context, client Client, spaceID string, source DatasetInfo) (string, error) {
	if value := strings.ToLower(strings.TrimSpace(source.Attributes["market_type"])); value != "" {
		if value != "spot" && value != "swap" {
			return "", fmt.Errorf("源数据集 %s 的 market_type=%s 不受支持", source.DatasetID, value)
		}
		return value, nil
	}
	if len(source.SubjectTags) == 0 {
		return "", fmt.Errorf("源数据集 %s 既没有 market_type 属性也没有标的范围标签，无法确定市场类型", source.DatasetID)
	}
	marketType := ""
	for _, tagID := range source.SubjectTags {
		tag, err := client.GetTag(ctx, spaceID, tagID)
		if err != nil {
			return "", err
		}
		if tag.MarketType != "spot" && tag.MarketType != "swap" {
			return "", fmt.Errorf("标签 %s 的市场类型 %q 不受支持", tagID, tag.MarketType)
		}
		if marketType != "" && marketType != tag.MarketType {
			return "", fmt.Errorf("源数据集 %s 的标签同时包含 %s 与 %s，无法确定市场类型", source.DatasetID, marketType, tag.MarketType)
		}
		marketType = tag.MarketType
	}
	return marketType, nil
}

// retentionBars 把数据集保留期（"<n>h" 或 "forever"）换算为根数；forever 或无法解析返回 false。
func retentionBars(retention, bar string) (int, bool) {
	retention = strings.TrimSpace(strings.ToLower(retention))
	if retention == "" || retention == "forever" || !strings.HasSuffix(retention, "h") {
		return 0, false
	}
	hours, err := strconv.Atoi(strings.TrimSuffix(retention, "h"))
	if err != nil || hours <= 0 {
		return 0, false
	}
	duration, err := parseBarDuration(bar)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return int((time.Duration(hours) * time.Hour) / duration), true
}

// Compile 用固化的解析结果重新编译 DSL（重启或恢复会话时使用）。
func Compile(resolved Resolved, dslYaml string) (*dsl.Program, error) {
	strategy, err := dsl.Parse([]byte(dslYaml))
	if err != nil {
		return nil, err
	}
	return dsl.Compile(strategy, resolved.ViewColumns)
}

// ParseResolved 解析保存的 resolved_json。
func ParseResolved(raw []byte) (Resolved, error) {
	var resolved Resolved
	if err := json.Unmarshal(raw, &resolved); err != nil {
		return Resolved{}, fmt.Errorf("解析 resolved_json：%w", err)
	}
	if resolved.ViewID == "" || resolved.Bar == "" || resolved.Calendar == "" {
		return Resolved{}, errors.New("resolved_json 缺少 view_id、bar 或 calendar")
	}
	if resolved.Columns == nil {
		resolved.Columns = map[string]ColumnBinding{}
	}
	if resolved.Factors == nil {
		resolved.Factors = map[string]string{}
	}
	return resolved, nil
}

// ReadinessBinding 把解析结果转为就绪判定需要的部分。
func (r Resolved) ReadinessBinding() readiness.Binding {
	factors := make(map[string]string, len(r.Factors))
	for id, hash := range r.Factors {
		factors[id] = hash
	}
	return readiness.Binding{Factors: factors, UsesPreviousBar: r.UsesPreviousBar}
}
