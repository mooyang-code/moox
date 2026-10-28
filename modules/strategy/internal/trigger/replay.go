package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/storagepb"
)

// 逐期回放的策略侧接口（设计 9.2、9.4）：回放服务按周期把完整的回放输入交过来，策略复用实盘的求值引擎（engine.Evaluate）
// 求出本期决策，只返回决策，不发事件。接口无状态：规则状态由回放服务按 replay_id 保存并在下一期带回来，
// 不读写实盘实例的状态。挂在健康检查端口上（只监听本机，沿用健康检查的签名鉴权）。
//
//	POST /v1/replay/bind      固定回放要用的策略版本与因子绑定
//	POST /v1/replay/evaluate  对一期求值

const (
	maxReplayBody   = 8 << 20
	replayMetaTTL   = 10 * time.Minute
	replayBindingTT = time.Hour
)

// ReplayEvaluator 实现逐期回放的求值。
type ReplayEvaluator struct {
	Store  *store.Store
	Client input.Client
	Now    func() time.Time

	mu       sync.Mutex
	bindings map[string]*replayBound
	meta     map[string]*replayMeta
}

type replayBound struct {
	resolved input.Resolved
	program  *dsl.Program
	dslHash  string
	space    string
	at       time.Time
}

type replayMeta struct {
	subjects []input.Subject
	members  map[string][]string
	at       time.Time
}

func (e *ReplayEvaluator) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

// Routes 返回挂到健康检查端口上的路由。
func (e *ReplayEvaluator) Routes() []health.Route {
	return []health.Route{
		{Path: "/v1/replay/bind", Handler: http.HandlerFunc(e.handleBind)},
		{Path: "/v1/replay/evaluate", Handler: http.HandlerFunc(e.handleEvaluate)},
	}
}

// ReplayBindRequest 是 POST /v1/replay/bind 的请求体。
type ReplayBindRequest struct {
	InstanceID string `json:"instance_id"`
}

// ReplayBinding 是回放固定下来的策略版本与因子绑定。
type ReplayBinding struct {
	InstanceID string `json:"instance_id"`
	StrategyID string `json:"strategy_id"`
	SpaceID    string `json:"space_id"`
	ViewID     string `json:"view_id"`
	DSLHash    string `json:"dsl_hash"`
	Bar        string `json:"bar"`
	Calendar   string `json:"calendar"`
	Spot       bool   `json:"spot"`
	MinAgeBars int    `json:"min_age_bars,omitempty"`
	// Factors 是策略引用的因子及其定义指纹（回放期间因子定义变了，回放服务据此拒绝继续）。
	Factors map[string]string `json:"factors"`
	// Columns 是策略引用的列：列名 → 来源（数据集列，或因子的某个输出）。
	Columns map[string]input.ColumnBinding `json:"columns"`
	// SourceColumns 是回放要带回的数据集列（含估值用的 close）。
	SourceColumns []string `json:"source_columns"`
	// PreviousDepth 是策略引用上一根（bars[-1]）的深度：0 或 1。
	PreviousDepth int `json:"previous_depth"`
	// FactorTaskID 是实例关联的因子任务（回放用它计算因子）；Tuned 为真表示策略逐期输出下一期的可变成员参数（闭环回放）。
	FactorTaskID string `json:"factor_task_id"`
	Tuned        bool   `json:"tuned,omitempty"`
}

func (e *ReplayEvaluator) handleBind(rw http.ResponseWriter, r *http.Request) {
	var req ReplayBindRequest
	if !decodeReplayBody(rw, r, &req) {
		return
	}
	binding, err := e.Bind(r.Context(), req.InstanceID)
	if err != nil {
		writeReplayError(rw, http.StatusBadRequest, err)
		return
	}
	writeReplayJSON(rw, http.StatusOK, binding)
}

// Bind 解析实例当前的策略定义并固定下来：与研究回放一样用当前的 DSL 与 View 绑定，不改实例本身的会话。
func (e *ReplayEvaluator) Bind(ctx context.Context, instanceID string) (ReplayBinding, error) {
	if e.Store == nil || e.Client == nil {
		return ReplayBinding{}, errors.New("策略模块没有配置 Storage 与 Factor 依赖，不能回放")
	}
	instance, err := e.Store.GetInstance(ctx, instanceID)
	if err != nil {
		return ReplayBinding{}, fmt.Errorf("读取实例 %s：%w", instanceID, err)
	}
	definition, err := e.Store.GetDefinition(ctx, instance.StrategyID)
	if err != nil {
		return ReplayBinding{}, fmt.Errorf("读取策略 %s：%w", instance.StrategyID, err)
	}
	strategy, err := dsl.Parse([]byte(definition.DSLYaml))
	if err != nil {
		return ReplayBinding{}, err
	}
	if strategy.HasTranche() {
		return ReplayBinding{}, errors.New("tranche 规则只用于合约 View，而闭环回放第一版只支持现货，暂不支持回放 tranche 策略")
	}
	resolved, program, err := e.resolveForReplay(ctx, instance, strategy)
	if err != nil {
		return ReplayBinding{}, err
	}
	if !resolved.Spot {
		return ReplayBinding{}, fmt.Errorf("View %s 的源数据集 market_type=%s；回放第一版只支持现货", resolved.ViewID, resolved.MarketType)
	}
	e.mu.Lock()
	if e.bindings == nil {
		e.bindings = map[string]*replayBound{}
	}
	for key, b := range e.bindings {
		if e.now().Sub(b.at) > replayBindingTT {
			delete(e.bindings, key)
		}
	}
	e.bindings[instanceID+"@"+definition.DSLHash] = &replayBound{resolved: resolved, program: program, dslHash: definition.DSLHash, space: instance.SpaceID, at: e.now()}
	e.mu.Unlock()
	source := map[string]struct{}{"close": {}, "open": {}}
	depth := 0
	if program.UsesPreviousBar {
		depth = 1
	}
	for _, name := range append(append([]string(nil), program.Columns...), program.PreviousColumns...) {
		if binding, ok := resolved.Columns[name]; ok && binding.Source == input.SourceDataset {
			source[name] = struct{}{}
		}
	}
	columns := make([]string, 0, len(source))
	for name := range source {
		columns = append(columns, name)
	}
	sort.Strings(columns)
	return ReplayBinding{
		InstanceID: instanceID, StrategyID: instance.StrategyID, SpaceID: instance.SpaceID, ViewID: resolved.ViewID,
		DSLHash: definition.DSLHash, Bar: resolved.Bar, Calendar: resolved.Calendar, Spot: resolved.Spot, MinAgeBars: resolved.MinAgeBars,
		Factors: resolved.Factors, Columns: resolved.Columns, SourceColumns: columns, PreviousDepth: depth,
		FactorTaskID: resolved.FactorTaskID, Tuned: len(program.Params) > 0,
	}, nil
}

// ReplayRow 是一个标的本期的回放输入（因子引擎 ReplayStep 返回的一个标的）。
type ReplayRow struct {
	Source      map[string]float64            `json:"source"`
	SourcePrev  map[string]float64            `json:"source_prev,omitempty"`
	Factors     map[string]map[string]float64 `json:"factors,omitempty"`
	FactorsPrev map[string]map[string]float64 `json:"factors_prev,omitempty"`
	// Params 是本期各成员（成员键）实际用的参数（参数名 → 值），与实时结果行的 __params 一致，params() 读它。
	Params  map[string]map[string]float64 `json:"params,omitempty"`
	AgeBars int                           `json:"age_bars"`
}

// ReplayFactorState 是一个成员本期的状态。
type ReplayFactorState struct {
	Status         string   `json:"status"`
	DefinitionHash string   `json:"definition_hash"`
	Kind           string   `json:"kind"`
	FailedSubjects []string `json:"failed_subjects,omitempty"`
}

// ReplayEvaluateRequest 是 POST /v1/replay/evaluate 的请求体：回放输入契约（设计 9.4）。
type ReplayEvaluateRequest struct {
	ReplayID   string `json:"replay_id"`
	InstanceID string `json:"instance_id"`
	DSLHash    string `json:"dsl_hash"`
	// Period 是这根 K 线的 data_time（RFC3339）。
	Period string `json:"period"`
	// Rows 是本期有数据的标的及其输入；它的键就是本期的标的名单。
	Rows map[string]ReplayRow `json:"rows"`
	// FactorStates 是各因子本期的状态（complete、degraded、skipped）与失败标的。
	FactorStates map[string]ReplayFactorState `json:"factor_states"`
	// State 是上一期的规则状态（回放服务保存，首期为空）。
	State json.RawMessage `json:"state,omitempty"`
}

// ReplayDecision 是本期决策：目标权重或跳过。
type ReplayDecision struct {
	Status     string          `json:"status"`
	SkipReason string          `json:"skip_reason,omitempty"`
	Detail     string          `json:"detail,omitempty"`
	Targets    []TargetJSON    `json:"targets"`
	State      json.RawMessage `json:"state"`
	Summary    json.RawMessage `json:"summary,omitempty"`
	BarEnd     string          `json:"bar_end"`
	DSLHash    string          `json:"dsl_hash"`
	// NextParams 是本期求出的下一期可变成员参数（成员键 → 参数名 → 取值）：只含求出参数的成员，且只在本期 ok 时给出。
	NextParams map[string]map[string]ReplayParamValue `json:"next_params,omitempty"`
}

// ReplayParamValue 是一个参数一期的取值：统一值加按标的覆盖。
type ReplayParamValue struct {
	Default   float64            `json:"default"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

func (e *ReplayEvaluator) handleEvaluate(rw http.ResponseWriter, r *http.Request) {
	var req ReplayEvaluateRequest
	if !decodeReplayBody(rw, r, &req) {
		return
	}
	decision, err := e.Evaluate(r.Context(), req)
	if err != nil {
		writeReplayError(rw, http.StatusBadRequest, err)
		return
	}
	writeReplayJSON(rw, http.StatusOK, decision)
}

// Evaluate 对一期求值。判定顺序与实时一致：就绪判定 → 集合划分与年龄剔除 → 引擎求值。
func (e *ReplayEvaluator) Evaluate(ctx context.Context, req ReplayEvaluateRequest) (ReplayDecision, error) {
	e.mu.Lock()
	bound := e.bindings[req.InstanceID+"@"+req.DSLHash]
	e.mu.Unlock()
	if bound == nil {
		return ReplayDecision{}, fmt.Errorf("实例 %s 的回放绑定不存在或已过期（dsl_hash=%s）：先调用 bind", req.InstanceID, req.DSLHash)
	}
	barStart, err := time.Parse(time.RFC3339, req.Period)
	if err != nil {
		return ReplayDecision{}, fmt.Errorf("period：%w", err)
	}
	resolved, program := bound.resolved, bound.program
	boundary, err := input.FromStorageStart(resolved.Calendar, resolved.Bar, barStart.UTC())
	if err != nil {
		return ReplayDecision{}, err
	}
	state := engine.State{}
	if len(req.State) > 0 && string(req.State) != "null" {
		if err := json.Unmarshal(req.State, &state); err != nil {
			return ReplayDecision{}, fmt.Errorf("规则状态无法解析：%w", err)
		}
	}
	finish := func(decision engine.Decision, detail string) (ReplayDecision, error) {
		targets, _, err := EncodeTargets(decision.Targets)
		if err != nil {
			return ReplayDecision{}, err
		}
		stateJSON, err := json.Marshal(decision.State)
		if err != nil {
			return ReplayDecision{}, err
		}
		summary, err := json.Marshal(decision.Summary)
		if err != nil {
			return ReplayDecision{}, err
		}
		return ReplayDecision{
			Status: decision.Status, SkipReason: decision.SkipReason, Detail: detail, Targets: targets, State: stateJSON, Summary: summary,
			BarEnd: boundary.BarEnd.Format(time.RFC3339), DSLHash: bound.dslHash,
		}, nil
	}
	skipped := func(reason, detail string) (ReplayDecision, error) {
		return finish(engine.Decision{Status: engine.StatusSkipped, SkipReason: reason, State: state}, detail)
	}

	// 就绪判定：回放固定了因子版本，上一根的版本证据取固定下来的指纹。
	event := &storagepb.ViewDataReady{Status: "complete"}
	for member, factorState := range req.FactorStates {
		event.Factors = append(event.Factors, &storagepb.FactorPeriodState{
			MemberKey: member, MemberKind: factorState.Kind, FactorId: member, Status: factorState.Status, DefinitionHash: factorState.DefinitionHash, FailedSubjects: factorState.FailedSubjects,
		})
	}
	var adjacent *readiness.Record
	if len(resolved.PreviousFactors) > 0 {
		adjacent = &readiness.Record{Factors: resolved.Factors}
	}
	ready := readiness.Check(resolved.ReadinessBinding(), adjacent, event)
	if ready.Reason != "" {
		return skipped(ready.Reason, ready.Detail)
	}

	meta, err := e.metaFor(ctx, bound)
	if err != nil {
		return ReplayDecision{}, err
	}
	universe := make([]string, 0, len(req.Rows))
	for subject := range req.Rows {
		universe = append(universe, subject)
	}
	sort.Strings(universe)
	members := func(ctx context.Context, tagID string) ([]string, error) { return meta.members[tagID], nil }
	sets, err := input.BuildSets(ctx, members, program.Strategy, meta.subjects, universe)
	if err != nil {
		var skip *input.SkipError
		if errors.As(err, &skip) {
			return skipped(skip.Reason, skip.Detail)
		}
		return ReplayDecision{}, err
	}
	probe := func(_ context.Context, instruments []string) (map[string]struct{}, error) {
		aged := make(map[string]struct{}, len(instruments))
		for _, id := range instruments {
			if row, ok := req.Rows[id]; ok && row.AgeBars >= resolved.MinAgeBars {
				aged[id] = struct{}{}
			}
		}
		return aged, nil
	}
	if err := sets.ApplyAge(ctx, resolved.MinAgeBars, probe); err != nil {
		var skip *input.SkipError
		if errors.As(err, &skip) {
			return skipped(skip.Reason, skip.Detail)
		}
		return ReplayDecision{}, err
	}

	frame := engine.Frame{
		BarEnd: boundary.BarEnd, BarIndex: boundary.BarIndex, Spot: resolved.Spot, Rows: make(map[string]engine.Row, len(req.Rows)),
		Universe: sets.Universe, Expected: sets.Expected, AgedOut: sets.AgedOut, FailedColumns: replayFailedColumns(resolved, program, ready),
	}
	for subject, row := range req.Rows {
		if _, ok := sets.Subjects[subject]; !ok {
			continue
		}
		engineRow := engine.Row{Values: replayValues(resolved, program, row.Source, row.Factors), Params: row.Params}
		if program.UsesPreviousBar {
			if len(row.SourcePrev) > 0 || len(row.FactorsPrev) > 0 {
				engineRow.Previous = replayValues(resolved, program, row.SourcePrev, row.FactorsPrev)
			}
		}
		frame.Rows[subject] = engineRow
	}
	decision, err := engine.Evaluate(program, frame, state)
	if err != nil {
		return skipped(input.SkipConfigError, err.Error())
	}
	out, err := finish(decision, "")
	if err != nil || decision.Status != engine.StatusOK || len(program.Params) == 0 {
		return out, err
	}
	outcome := engine.EvaluateParams(program, frame, resolved.FactorParams)
	if len(outcome.Params) > 0 {
		out.NextParams = make(map[string]map[string]ReplayParamValue, len(outcome.Params))
		for member, params := range outcome.Params {
			out.NextParams[member] = make(map[string]ReplayParamValue, len(params))
			for name, value := range params {
				out.NextParams[member][name] = ReplayParamValue{Default: value.Default, Overrides: value.Overrides}
			}
		}
	}
	if len(outcome.Notes) > 0 {
		out.Detail = strings.Join(outcome.Notes, "；")
	}
	return out, nil
}

// replayValues 把一个标的的源列与因子输出按策略引用的列名装配成引擎的行：数据集列取源列，因子列取因子输出。
func replayValues(resolved input.Resolved, program *dsl.Program, source map[string]float64, factors map[string]map[string]float64) map[string]float64 {
	values := make(map[string]float64)
	for _, name := range append(append([]string(nil), program.Columns...), program.PreviousColumns...) {
		binding, ok := resolved.Columns[name]
		switch {
		case !ok:
		case binding.Source == input.SourceFactor:
			if v, has := factors[binding.Member][name]; has {
				values[name] = v
			}
		default:
			if v, has := source[name]; has {
				values[name] = v
			}
		}
	}
	if v, has := source["close"]; has {
		if _, referenced := values["close"]; !referenced {
			values["close"] = v
		}
	}
	return values
}

// replayFailedColumns 与实时一致：因子失败的标的映射到该因子产出的被引用列。
func replayFailedColumns(resolved input.Resolved, program *dsl.Program, ready readiness.Result) map[string]map[string]string {
	failed := make(map[string]map[string]string)
	for factorID, subjects := range ready.FailedByFactor {
		for _, column := range resolved.ColumnsOfFactor(factorID) {
			for _, subject := range subjects {
				if failed[column] == nil {
					failed[column] = make(map[string]string)
				}
				if _, exists := failed[column][subject]; !exists {
					failed[column][subject] = engine.FailedFactor
				}
			}
		}
	}
	return failed
}

// metaFor 取标的绑定与规则引用的标签成员；回放期间缓存一段时间，不在每一期重复读取。
func (e *ReplayEvaluator) metaFor(ctx context.Context, bound *replayBound) (*replayMeta, error) {
	key := bound.space + "/" + bound.resolved.DatasetID + "/" + bound.dslHash
	e.mu.Lock()
	if e.meta == nil {
		e.meta = map[string]*replayMeta{}
	}
	cached := e.meta[key]
	e.mu.Unlock()
	if cached != nil && e.now().Sub(cached.at) < replayMetaTTL {
		return cached, nil
	}
	subjects, err := e.Client.ListDatasetSubjects(ctx, bound.space, bound.resolved.DatasetID)
	if err != nil {
		return nil, fmt.Errorf("读取数据集标的：%w", err)
	}
	for i := range subjects {
		subjects[i].Active = true
	}
	meta := &replayMeta{subjects: subjects, members: map[string][]string{}, at: e.now()}
	for _, tag := range input.ReferencedTags(bound.program.Strategy) {
		ids, err := e.Client.ListTagMembers(ctx, bound.space, tag)
		if err != nil {
			return nil, fmt.Errorf("读取标签 %s 的成员：%w", tag, err)
		}
		meta.members[tag] = ids
	}
	e.mu.Lock()
	e.meta[key] = meta
	e.mu.Unlock()
	return meta, nil
}

func decodeReplayBody(rw http.ResponseWriter, r *http.Request, out any) bool {
	if r.Method != http.MethodPost {
		writeReplayError(rw, http.StatusMethodNotAllowed, errors.New("POST only"))
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxReplayBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeReplayError(rw, http.StatusBadRequest, fmt.Errorf("解析请求：%w", err))
		return false
	}
	return true
}

func writeReplayJSON(rw http.ResponseWriter, status int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(v)
}

func writeReplayError(rw http.ResponseWriter, status int, err error) {
	writeReplayJSON(rw, status, map[string]string{"error": err.Error()})
}
