package engine

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

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/seriessync"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// 逐期回放的因子侧接口（设计 9.2）：回放服务通过它按时间顺序逐期取因子帧。回放窗口是会话开始时从本地存储取的快照，
// 运行中不随修正更新；每期走实时计算路径（pipeline.Compute）；回放不发周期完成事件，不改连续水位。
// 接口挂在健康检查端口上（只监听本机，沿用健康检查的签名鉴权）。
//
//	POST /v1/replay/open   建立会话：校验数据、按预热区间读快照、预留内存
//	POST /v1/replay/step   计算一期，返回因子帧、源数据列、报价与标的年龄
//	POST /v1/replay/close  结束会话，释放内存

const (
	replayOpenTimeout   = 30 * time.Minute
	replayIdleTimeout   = 6 * time.Hour
	replayReserveWait   = time.Minute
	maxReplayBody       = 1 << 20
	replayWriteBatchMin = 1000
)

// ReplayDeps 是回放会话用到的引擎能力。
type ReplayDeps struct {
	Sets interface {
		SetByID(string) (domain.EngineSet, SetLookup)
	}
	// Params 提供回放区间的冻结参数（复现历史时用；闭环回放由每期请求带参数）。为 nil 时按默认参数。
	Params  paramIntervalSource
	Source  SourceDataset
	Columns interface {
		ListColumns(ctx context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error)
	}
	Store   *seriesstore.Store
	Runner  *pipeline.Runner
	Admit   *memadmit.Admitter
	Now     func() time.Time
	BatchOK func() int
}

type replayManager struct {
	deps ReplayDeps

	mu       sync.Mutex
	sessions map[string]*replaySession
}

func newReplayManager(deps ReplayDeps) *replayManager {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &replayManager{deps: deps, sessions: map[string]*replaySession{}}
}

type replaySession struct {
	id       string
	set      domain.EngineSet
	src      seriesstore.Source
	clock    periodclock.Clock
	step     time.Duration
	from, to time.Time // 决策周期的 data_time 范围 [from, to)
	start    time.Time // 预热起点
	subjects []string
	numeric  []string
	lookback int
	carry    []string
	types    map[string]string
	view     *serieswindow.View
	cols     map[string]*seriesstore.Columns
	earliest map[string]time.Time
	release  func()
	// quote 是成交价取法：next_open（下一根开盘，默认）或 close（本期收盘）。
	quote string
	// intervals 是回放区间（含预热）的冻结参数：请求没有带参数的期次按它计算（复现当时实际用的参数）。
	intervals map[string][]domain.ParamInterval

	mu       sync.Mutex
	cache    map[int64]*replayPeriod
	lastUsed time.Time
}

// replayPeriod 是一期的计算结果（缓存，下一期取上一期的因子值用）；digest 是这一期所用参数的摘要。
type replayPeriod struct {
	values pipeline.FactorValues
	states map[string]ReplayFactorState
	digest string
}

// ReplayFactorState 是一个成员本期的状态：complete、degraded（部分标的失败）或 skipped（截面因子整体跳过）。
type ReplayFactorState struct {
	Status         string   `json:"status"`
	FactorID       string   `json:"factor_id"`
	DefinitionHash string   `json:"definition_hash"`
	FailedSubjects []string `json:"failed_subjects,omitempty"`
	// ParamsSource、ParamsDigest 是这一期所用参数的来源（requested 表示由请求带来）与摘要。
	ParamsSource string `json:"params_source,omitempty"`
	ParamsDigest string `json:"params_digest,omitempty"`
}

// ReplayOpenRequest 是 POST /v1/replay/open 的请求体。
type ReplayOpenRequest struct {
	ReplayID string `json:"replay_id"`
	SetID    string `json:"set_id"`
	// From、To 是回放区间（RFC3339，左闭右开）：决策周期是 data_time 落在其中的每一根 K 线。
	From string `json:"from"`
	To   string `json:"to"`
	// ExtraPeriods 是策略额外需要的回看根数（引用因子列的上一根深度、年龄检查根数中较大的）；预热起点 = from −（因子最大回看 − 1 + ExtraPeriods）。
	ExtraPeriods int `json:"extra_periods"`
	// Quote 是成交价取法：next_open（默认）或 close。
	Quote string `json:"quote"`
	// WriteFactors 为真时，每期把因子结果写回结果数据集（与区间重算相同的写法，不发周期完成事件）。
	WriteFactors bool `json:"write_factors"`
	// AllowShortWarmup 允许多数标的的本地数据不够预热区间：不加时多数标的预热不足会拒绝建立会话（因子前段会是空值，回放结果没有意义）。
	AllowShortWarmup bool `json:"allow_short_warmup,omitempty"`
}

// ReplayFactor 描述回放固定下来的一个成员：Outputs 是它在结果数据集里的列名，Params 是它的参数列名。
type ReplayFactor struct {
	Member         string   `json:"member"`
	FactorID       string   `json:"factor_id"`
	DefinitionHash string   `json:"definition_hash"`
	Outputs        []string `json:"outputs"`
	Params         []string `json:"params,omitempty"`
}

// ReplayExcluded 是被排除出回放的标的及原因。
type ReplayExcluded struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
}

// ReplayOpenResponse 是建立会话的结果。
type ReplayOpenResponse struct {
	ReplayID string           `json:"replay_id"`
	Subjects []string         `json:"subjects"`
	Excluded []ReplayExcluded `json:"excluded,omitempty"`
	Periods  int              `json:"periods"`
	Start    string           `json:"start"`
	StepSecs int64            `json:"step_seconds"`
	Bytes    int64            `json:"bytes"`
	Factors  []ReplayFactor   `json:"factors"`
	Fields   []string         `json:"fields"`
	// ShortWarmup 是本地数据起点晚于预热起点的标的数（因子前段为空值）；ShortSamples 是其中的前几个。
	ShortWarmup  int      `json:"short_warmup,omitempty"`
	ShortSamples []string `json:"short_samples,omitempty"`
}

func (m *replayManager) routes() []factorhealth.Route {
	return []factorhealth.Route{
		{Path: "/v1/replay/open", Handler: http.HandlerFunc(m.handleOpen)},
		{Path: "/v1/replay/step", Handler: http.HandlerFunc(m.handleStep)},
		{Path: "/v1/replay/close", Handler: http.HandlerFunc(m.handleClose)},
	}
}

func decodeReplay(rw http.ResponseWriter, r *http.Request, out any) bool {
	if r.Method != http.MethodPost {
		writeJSONError(rw, http.StatusMethodNotAllowed, errors.New("POST only"))
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, maxReplayBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Errorf("解析请求：%w", err))
		return false
	}
	return true
}

func (m *replayManager) handleOpen(rw http.ResponseWriter, r *http.Request) {
	var req ReplayOpenRequest
	if !decodeReplay(rw, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), replayOpenTimeout)
	defer cancel()
	rsp, status, err := m.Open(ctx, req)
	if err != nil {
		writeJSONError(rw, status, err)
		return
	}
	writeJSON(rw, http.StatusOK, rsp)
}

// Open 建立回放会话。同一个 replay_id 重复打开时（回放服务重启后续跑）先释放旧会话再重建。
func (m *replayManager) Open(ctx context.Context, req ReplayOpenRequest) (ReplayOpenResponse, int, error) {
	req.ReplayID, req.SetID = strings.TrimSpace(req.ReplayID), strings.TrimSpace(req.SetID)
	if req.ReplayID == "" || req.SetID == "" {
		return ReplayOpenResponse{}, http.StatusBadRequest, errors.New("replay_id 与 set_id 是必填项")
	}
	if req.ExtraPeriods < 0 {
		return ReplayOpenResponse{}, http.StatusBadRequest, errors.New("extra_periods 不能为负数")
	}
	quote := req.Quote
	if quote == "" {
		quote = "next_open"
	}
	if quote != "next_open" && quote != "close" {
		return ReplayOpenResponse{}, http.StatusBadRequest, errors.New("quote 只能是 next_open 或 close")
	}
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusBadRequest, fmt.Errorf("from：%w", err)
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusBadRequest, fmt.Errorf("to：%w", err)
	}
	set, lookup := m.deps.Sets.SetByID(req.SetID)
	if lookup != SetFound {
		return ReplayOpenResponse{}, http.StatusNotFound, fmt.Errorf("因子集合 %s 不在引擎目录里（未启用或结果数据集未就绪）", req.SetID)
	}
	set.Members = CompileMembers(set.Members)
	step, err := seriesstore.Step(set.Set.Freq)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusBadRequest, err
	}
	from, to = from.UTC(), to.UTC()
	if !from.Before(to) || from.Unix()%int64(step/time.Second) != 0 || to.Unix()%int64(step/time.Second) != 0 {
		return ReplayOpenResponse{}, http.StatusBadRequest, fmt.Errorf("回放区间 [%s, %s) 必须按 %s 对齐且 from 早于 to", req.From, req.To, set.Set.Freq)
	}
	clock, err := periodclock.ForSpace(set.Set.SpaceID)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusBadRequest, err
	}
	lookback := maxLookbackOf(set)
	start := from.Add(-time.Duration(lookback-1+req.ExtraPeriods) * step)
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}

	m.closeSession(req.ReplayID)
	fields, ok, err := m.deps.Store.Schema(src)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusInternalServerError, err
	}
	if !ok {
		return ReplayOpenResponse{}, http.StatusConflict, fmt.Errorf("数据源 %s 在本地没有数据：先导入或启动同步", src)
	}
	numeric := serieswindow.NumericFields(fields)
	candidates, err := setSubjects(ctx, m.deps.Source, set, nil)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusBadGateway, err
	}
	// 校验：本地数据覆盖回放区间，并且包含 to 之后的一根（用于下一根开盘成交）。预热区间不够的标的照常参与（因子前段为空）。
	var subjects []string
	var excluded []ReplayExcluded
	earliest := map[string]time.Time{}
	for _, subject := range uniqueStrings(candidates) {
		b, has, err := m.deps.Store.Bounds(src, subject)
		if err != nil {
			return ReplayOpenResponse{}, http.StatusInternalServerError, err
		}
		switch {
		case !has:
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: "本地没有数据"})
		case b.Latest.Before(to):
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: fmt.Sprintf("本地数据只到 %s，不包含回放终点（%s）这一根", b.Latest.Format(time.RFC3339), to.Format(time.RFC3339))})
		case b.Earliest.After(to.Add(-step)):
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: fmt.Sprintf("本地数据从 %s 才开始，回放区间内没有数据", b.Earliest.Format(time.RFC3339))})
		default:
			subjects = append(subjects, subject)
			earliest[subject] = b.Earliest
		}
	}
	if len(subjects) == 0 {
		return ReplayOpenResponse{}, http.StatusConflict, fmt.Errorf("没有任何标的的本地数据覆盖回放区间：先导入 %s 到 %s 的数据（需要包含终点之后的一根）", from.Format(time.RFC3339), to.Format(time.RFC3339))
	}
	var shortSamples []string
	shortCount := 0
	for _, subject := range subjects {
		if earliest[subject].After(start) {
			shortCount++
			if len(shortSamples) < 5 {
				shortSamples = append(shortSamples, subject)
			}
		}
	}
	if shortCount*2 > len(subjects) && !req.AllowShortWarmup {
		return ReplayOpenResponse{}, http.StatusConflict, fmt.Errorf("%d/%d 个标的的本地数据晚于预热起点 %s（例如 %s），因子前段会是空值：先回补或导入更早的历史（moox-factor-engine backfill / moox-cli import），或确认接受后加 --allow-short-warmup",
			shortCount, len(subjects), start.Format(time.RFC3339), strings.Join(shortSamples, "、"))
	}
	periods := int(to.Sub(from) / step)
	rowsPerSubject := int(to.Add(step).Sub(start) / step)
	need := int64(len(subjects)) * int64(rowsPerSubject) * int64(len(numeric)*8+9)
	release := func() {}
	if m.deps.Admit != nil {
		waitCtx, cancel := context.WithTimeout(memadmit.WithPriority(ctx, memadmit.PriorityBackground), replayReserveWait)
		release, err = m.deps.Admit.Reserve(waitCtx, need)
		cancel()
		if err != nil {
			return ReplayOpenResponse{}, http.StatusServiceUnavailable, fmt.Errorf("回放快照需要约 %d 字节内存，预留不到：%w", need, err)
		}
	}
	cleanup := release
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	snapshot, err := m.deps.Store.ReadRange(ctx, src, subjects, start, to.Add(step))
	if err != nil {
		return ReplayOpenResponse{}, http.StatusInternalServerError, fmt.Errorf("读取回放快照：%w", err)
	}
	view, err := serieswindow.FromColumns(int64(step/time.Second), numeric, snapshot)
	if err != nil {
		return ReplayOpenResponse{}, http.StatusConflict, fmt.Errorf("回放快照不连续：%w", err)
	}
	var carry []string
	var types map[string]string
	if req.WriteFactors {
		if carry, err = m.deps.Source.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID); err != nil {
			return ReplayOpenResponse{}, http.StatusBadGateway, fmt.Errorf("读取源数据集字段：%w", err)
		}
		columns, err := m.deps.Columns.ListColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return ReplayOpenResponse{}, http.StatusBadGateway, fmt.Errorf("读取源数据集字段类型：%w", err)
		}
		types = seriessync.ValueTypes(columns)
	}
	var intervals map[string][]domain.ParamInterval
	if m.deps.Params != nil {
		if intervals, err = m.deps.Params.Intervals(ctx, set.Set.SetID, start, to); err != nil {
			return ReplayOpenResponse{}, http.StatusBadGateway, fmt.Errorf("读取冻结参数：%w", err)
		}
	}
	session := &replaySession{
		id: req.ReplayID, set: set, src: src, clock: clock, step: step, from: from, to: to, start: start,
		subjects: subjects, numeric: numeric, lookback: lookback, carry: carry, types: types, view: view, cols: snapshot,
		earliest: earliest, release: release, quote: quote, intervals: intervals, cache: map[int64]*replayPeriod{}, lastUsed: m.deps.Now(),
	}
	cleanup = nil
	m.mu.Lock()
	m.sessions[req.ReplayID] = session
	m.mu.Unlock()
	rsp := ReplayOpenResponse{
		ReplayID: req.ReplayID, Subjects: subjects, Excluded: excluded, Periods: periods, Start: start.Format(time.RFC3339),
		StepSecs: int64(step / time.Second), Bytes: need, Fields: numeric, ShortWarmup: shortCount, ShortSamples: shortSamples,
	}
	for _, member := range set.Members {
		rsp.Factors = append(rsp.Factors, ReplayFactor{Member: member.Alias, FactorID: member.Factor.FactorID,
			DefinitionHash: domain.DefinitionHash(member.Factor), Outputs: member.OutputColumns(), Params: member.ParamColumns()})
	}
	return rsp, http.StatusOK, nil
}

// ReplayStepRequest 是 POST /v1/replay/step 的请求体。
type ReplayStepRequest struct {
	ReplayID string `json:"replay_id"`
	// Period 是这根 K 线的 data_time（RFC3339）；决策时刻是 Period + 周期。
	Period string `json:"period"`
	// SourceColumns 是要返回的源数据列；为空返回全部数值列。close 与 open 总是返回（估值、成交价要用）。
	SourceColumns []string `json:"source_columns,omitempty"`
	// WriteFactors 为真时把本期因子结果写回结果数据集（会话打开时也要声明 write_factors）。
	WriteFactors bool `json:"write_factors,omitempty"`
	// Params 是闭环回放这一期各成员的参数快照（别名 → 快照，因子动态调参执行计划 2.9）：由策略回放在上一期求值后产出。
	// 没有给出的成员按会话打开时读取的冻结记录计算（复现当时实际用的参数），再没有就用默认值。
	Params map[string]domain.ParamSnapshot `json:"params,omitempty"`
}

// ReplaySubjectFrame 是一个标的本期的数据。
type ReplaySubjectFrame struct {
	// Source 是本期源数据列的值，SourcePrev 是上一根的；缺失的列不出现。
	Source     map[string]float64 `json:"source"`
	SourcePrev map[string]float64 `json:"source_prev,omitempty"`
	// Factors 是各因子各输出列本期的值，FactorsPrev 是上一期的。
	Factors     map[string]map[string]float64 `json:"factors,omitempty"`
	FactorsPrev map[string]map[string]float64 `json:"factors_prev,omitempty"`
	// Quote 是回放报价：Close 是本期收盘价，Exec 是成交价（next_open 时为下一根开盘价，close 时为本期收盘价）；没有价格时为 0。
	Close float64 `json:"close"`
	Exec  float64 `json:"exec"`
	// AgeBars 是本地数据里这个标的截至本期的根数（含本期）：策略的年龄检查用。
	AgeBars int `json:"age_bars"`
}

// ReplayStepResponse 是一期的因子帧。
type ReplayStepResponse struct {
	ReplayID string `json:"replay_id"`
	Period   string `json:"period"`
	// DecisionAt 是决策时刻（Period + 周期）。
	DecisionAt string                        `json:"decision_at"`
	Subjects   map[string]ReplaySubjectFrame `json:"subjects"`
	States     map[string]ReplayFactorState  `json:"factor_states"`
	// Rows 是写回结果数据集的行数（write_factors 时）。
	Rows int `json:"rows,omitempty"`
	// ComputeMillis 是本期因子计算耗时。
	ComputeMillis int64 `json:"compute_ms"`
}

func (m *replayManager) handleStep(rw http.ResponseWriter, r *http.Request) {
	var req ReplayStepRequest
	if !decodeReplay(rw, r, &req) {
		return
	}
	rsp, status, err := m.Step(r.Context(), req)
	if err != nil {
		writeJSONError(rw, status, err)
		return
	}
	writeJSON(rw, http.StatusOK, rsp)
}

func (m *replayManager) session(id string) *replaySession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// Step 计算一期并返回因子帧。同一期可重复请求（回放服务重启后补齐）：结果由快照决定，重复计算得到同样的值。
func (m *replayManager) Step(ctx context.Context, req ReplayStepRequest) (ReplayStepResponse, int, error) {
	s := m.session(strings.TrimSpace(req.ReplayID))
	if s == nil {
		return ReplayStepResponse{}, http.StatusNotFound, fmt.Errorf("回放会话 %s 不存在（引擎重启或已结束），请重新建立会话", req.ReplayID)
	}
	period, err := time.Parse(time.RFC3339, req.Period)
	if err != nil {
		return ReplayStepResponse{}, http.StatusBadRequest, fmt.Errorf("period：%w", err)
	}
	period = period.UTC()
	if period.Before(s.from) || !period.Before(s.to) || (period.Unix()-s.from.Unix())%int64(s.step/time.Second) != 0 {
		return ReplayStepResponse{}, http.StatusBadRequest, fmt.Errorf("周期 %s 不在回放区间 [%s, %s) 内或没有对齐", req.Period, s.from.Format(time.RFC3339), s.to.Format(time.RFC3339))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = m.deps.Now()
	started := time.Now()
	params, err := s.params(period, req.Params)
	if err != nil {
		return ReplayStepResponse{}, http.StatusBadRequest, err
	}
	current, plan, comp, err := s.compute(ctx, m.deps.Runner, period, params)
	if err != nil {
		return ReplayStepResponse{}, http.StatusInternalServerError, err
	}
	previous := s.cache[period.Add(-s.step).Unix()]
	if previous == nil && !period.Add(-s.step).Before(s.start) {
		prevParams, err := s.params(period.Add(-s.step), nil)
		if err != nil {
			return ReplayStepResponse{}, http.StatusInternalServerError, err
		}
		if previous, _, _, err = s.compute(ctx, m.deps.Runner, period.Add(-s.step), prevParams); err != nil {
			return ReplayStepResponse{}, http.StatusInternalServerError, err
		}
	}
	rsp := ReplayStepResponse{
		ReplayID: s.id, Period: period.Format(time.RFC3339), DecisionAt: period.Add(s.step).Format(time.RFC3339),
		Subjects: make(map[string]ReplaySubjectFrame, len(s.subjects)), States: current.states,
	}
	wanted := s.sourceColumns(req.SourceColumns)
	now, prev := period.Unix(), period.Add(-s.step).Unix()
	next := period.Add(s.step).Unix()
	for _, subject := range s.subjects {
		closePrice, hasClose := s.view.ValueAt(subject, "close", now)
		if _, present := s.view.Latest(subject); !present {
			continue
		}
		frame := ReplaySubjectFrame{Source: map[string]float64{}}
		present := false
		for _, name := range wanted {
			if v, ok := s.view.ValueAt(subject, name, now); ok {
				frame.Source[name] = v
				present = true
			}
			if v, ok := s.view.ValueAt(subject, name, prev); ok {
				if frame.SourcePrev == nil {
					frame.SourcePrev = map[string]float64{}
				}
				frame.SourcePrev[name] = v
			}
		}
		if !present && !hasClose {
			continue
		}
		if hasClose {
			frame.Close, frame.Exec = closePrice, closePrice
		}
		if s.quote == "next_open" {
			if v, ok := s.view.ValueAt(subject, "open", next); ok {
				frame.Exec = v
			} else {
				frame.Exec = 0
			}
		}
		frame.Factors = current.values[subject]
		if previous != nil {
			frame.FactorsPrev = previous.values[subject]
		}
		if first, ok := s.earliest[subject]; ok {
			frame.AgeBars = int((period.Unix()-first.Unix())/int64(s.step/time.Second)) + 1
		}
		rsp.Subjects[subject] = frame
	}
	if req.WriteFactors {
		rows, err := s.write(ctx, m.deps.Runner, plan, comp, period, m.batch())
		if err != nil {
			return ReplayStepResponse{}, http.StatusBadGateway, err
		}
		rsp.Rows = rows
	}
	rsp.ComputeMillis = time.Since(started).Milliseconds()
	// 只保留当期与上一期的结果，缓存不随回放长度增长。
	for key := range s.cache {
		if key < period.Add(-s.step).Unix() {
			delete(s.cache, key)
		}
	}
	return rsp, http.StatusOK, nil
}

func (m *replayManager) batch() int {
	if m.deps.BatchOK != nil {
		if n := m.deps.BatchOK(); n > 0 {
			return n
		}
	}
	return replayWriteBatchMin
}

// sourceColumns 返回要带回的源数据列：请求的列（只保留窗口里有的）加上 close、open。
func (s *replaySession) sourceColumns(requested []string) []string {
	has := map[string]bool{}
	for _, name := range s.numeric {
		has[name] = true
	}
	set := map[string]struct{}{}
	if len(requested) == 0 {
		for _, name := range s.numeric {
			set[name] = struct{}{}
		}
	}
	for _, name := range append([]string{"close", "open"}, requested...) {
		if has[name] {
			set[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// params 返回一期各成员用的参数：请求带来的快照（校验合法）优先，其次是冻结记录（沿用最近的区间），都没有时为默认值。
func (s *replaySession) params(period time.Time, requested map[string]domain.ParamSnapshot) (map[string]domain.ParamFreeze, error) {
	for alias := range requested {
		found := false
		for _, member := range s.set.Members {
			found = found || member.Alias == alias
		}
		if !found {
			return nil, fmt.Errorf("集合里没有成员 %s", alias)
		}
	}
	out := make(map[string]domain.ParamFreeze, len(s.set.Members))
	for _, member := range s.set.Members {
		if snapshot, ok := requested[member.Alias]; ok {
			if err := domain.CheckSnapshotNumbers(snapshot); err != nil {
				return nil, err
			}
			if err := domain.ValidateSnapshot(member, snapshot, s.set.Set.InScope); err != nil {
				return nil, err
			}
			out[member.Alias] = domain.ParamFreeze{SetID: s.set.Set.SetID, MemberAlias: member.Alias, PeriodTime: period, Snapshot: snapshot,
				SnapshotHash: snapshot.Hash(), Source: domain.ParamsRequested}
			continue
		}
		var previous *domain.ParamInterval
		for i := range s.intervals[member.Alias] {
			interval := s.intervals[member.Alias][i]
			if interval.Covers(period) {
				out[member.Alias] = domain.ParamFreeze{SetID: s.set.Set.SetID, MemberAlias: member.Alias, PeriodTime: period,
					Snapshot: interval.Snapshot, SnapshotHash: interval.SnapshotHash, Source: interval.Source}
				previous = nil
				break
			}
			if interval.End.Before(period) {
				previous = &s.intervals[member.Alias][i]
			}
		}
		if _, ok := out[member.Alias]; !ok && previous != nil {
			source := domain.ParamsCarried
			if previous.Source == domain.ParamsDefault {
				source = domain.ParamsDefault
			}
			out[member.Alias] = domain.ParamFreeze{SetID: s.set.Set.SetID, MemberAlias: member.Alias, PeriodTime: period,
				Snapshot: previous.Snapshot, SnapshotHash: previous.SnapshotHash, Source: source}
		}
	}
	return out, nil
}

func paramsDigest(params map[string]domain.ParamFreeze) string {
	aliases := make([]string, 0, len(params))
	for alias := range params {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	var b strings.Builder
	for _, alias := range aliases {
		b.WriteString(alias + "=" + params[alias].Snapshot.Hash() + ";")
	}
	return b.String()
}

// compute 用快照算一期（实时计算路径）；同一期、同一组参数已经算过时直接取缓存（调用方持有会话锁）。
func (s *replaySession) compute(ctx context.Context, runner *pipeline.Runner, period time.Time, params map[string]domain.ParamFreeze) (*replayPeriod, pipeline.Plan, pipeline.Computation, error) {
	digest := paramsDigest(params)
	cached := s.cache[period.Unix()]
	if cached != nil && cached.digest != digest {
		cached = nil
	}
	// 本期的标的全集只含这一期已经上市（本地最早一根不晚于本期）的标的：未来才出现的标的不能改变早期周期的截面与「缺数」判定。
	var universe []string
	for _, subject := range s.subjects {
		if first, ok := s.earliest[subject]; ok && !first.After(period) {
			universe = append(universe, subject)
		}
	}
	plan, err := pipeline.BuildLivePlan(s.clock, pipeline.LiveInput{
		Set: s.set.Set, Members: s.set.Members, Params: params, PeriodTime: period, Universe: universe, CarryColumns: s.carry,
	})
	if err != nil {
		return nil, pipeline.Plan{}, pipeline.Computation{}, err
	}
	var available []string
	for _, subject := range universe {
		if _, ok := s.view.ValueAt(subject, "close", period.Unix()); ok || s.hasRow(subject, period) {
			available = append(available, subject)
		}
	}
	plan.Available = available
	if cached != nil {
		return cached, plan, pipeline.Computation{}, nil
	}
	loaded := pipeline.LoadResult{View: s.view.Truncate(period.Unix(), s.lookback+1), Available: available, Frames: map[string]*storageio.Frame{}}
	computation, err := runner.Compute(ctx, plan, loaded)
	if err != nil {
		return nil, plan, computation, fmt.Errorf("计算 %s：%w", period.Format(time.RFC3339), err)
	}
	values, err := pipeline.PeriodValues(plan, computation, period)
	if err != nil {
		return nil, plan, computation, err
	}
	states := make(map[string]ReplayFactorState, len(computation.FactorStates))
	for alias, state := range computation.FactorStates {
		states[alias] = ReplayFactorState{Status: state.Status, FactorID: state.FactorID, DefinitionHash: state.DefinitionHash,
			FailedSubjects: state.FailedSubjects, ParamsSource: state.ParamsSource, ParamsDigest: state.ParamsDigest}
	}
	result := &replayPeriod{values: values, states: states, digest: digest}
	s.cache[period.Unix()] = result
	return result, plan, computation, nil
}

func (s *replaySession) hasRow(subject string, at time.Time) bool {
	cols := s.cols[subject]
	if cols == nil {
		return false
	}
	i := sort.Search(cols.Len(), func(i int) bool { return cols.Times[i] >= at.Unix() })
	return i < cols.Len() && cols.Times[i] == at.Unix()
}

// write 把本期因子结果写回结果数据集：用快照里这一期的源行组装（带携带列），与区间重算的写法相同，不发周期完成事件。
func (s *replaySession) write(ctx context.Context, runner *pipeline.Runner, plan pipeline.Plan, comp pipeline.Computation, period time.Time, batch int) (int, error) {
	if len(s.carry) == 0 {
		return 0, errors.New("会话没有按 write_factors 打开：打开会话时要声明 write_factors")
	}
	if comp.Results == nil {
		// 命中缓存时没有保留计算结果：重算这一期。
		loaded := pipeline.LoadResult{View: s.view.Truncate(period.Unix(), s.lookback+1), Available: plan.Available}
		var err error
		if comp, err = runner.Compute(ctx, plan, loaded); err != nil {
			return 0, err
		}
	}
	frames := make(map[string]*storageio.Frame, len(s.subjects))
	for _, subject := range s.subjects {
		cols := s.cols[subject]
		if cols == nil {
			continue
		}
		target := seriesstore.NewColumns(cols.Fields, 1)
		for i := 0; i < cols.Len(); i++ {
			if cols.Times[i] == period.Unix() {
				target.AppendRowFrom(cols, i)
			}
		}
		if target.Len() > 0 {
			frames[subject] = seriessync.ColumnsToFrame(subject, target, s.carry, s.types)
		}
	}
	loaded := pipeline.LoadResult{View: s.view.Truncate(period.Unix(), s.lookback+1), Available: plan.Available, Frames: frames}
	rows, err := runner.Assemble(plan, loaded, comp)
	if err != nil {
		return 0, fmt.Errorf("组装 %s 的结果行：%w", period.Format(time.RFC3339), err)
	}
	plan.CommitSalt = fmt.Sprintf("replay:%s", s.id)
	if err := runner.Write(ctx, plan, period, rows, batch); err != nil {
		return 0, err
	}
	return len(rows), nil
}

type replayCloseRequest struct {
	ReplayID string `json:"replay_id"`
}

func (m *replayManager) handleClose(rw http.ResponseWriter, r *http.Request) {
	var req replayCloseRequest
	if !decodeReplay(rw, r, &req) {
		return
	}
	closed := m.closeSession(strings.TrimSpace(req.ReplayID))
	writeJSON(rw, http.StatusOK, map[string]bool{"closed": closed})
}

func (m *replayManager) closeSession(id string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.release()
	return true
}

// Sweep 关闭长时间没有请求的会话（回放服务异常退出后不会来关）。
func (m *replayManager) Sweep() int {
	m.mu.Lock()
	var stale []string
	for id, s := range m.sessions {
		s.mu.Lock()
		idle := m.deps.Now().Sub(s.lastUsed)
		s.mu.Unlock()
		if idle > replayIdleTimeout {
			stale = append(stale, id)
		}
	}
	m.mu.Unlock()
	for _, id := range stale {
		m.closeSession(id)
	}
	return len(stale)
}

// Run 周期清理空闲会话，直到 ctx 结束；退出时释放全部会话。
func (m *replayManager) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			ids := make([]string, 0, len(m.sessions))
			for id := range m.sessions {
				ids = append(ids, id)
			}
			m.mu.Unlock()
			for _, id := range ids {
				m.closeSession(id)
			}
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}

func uniqueStrings(in []string) []string {
	set := make(map[string]struct{}, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			set[v] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
