package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/seriesset"
	"google.golang.org/protobuf/proto"
)

// 回放的“因子现算”来源：策略服务经主机网关调引擎的 EngineControl 服务（replay_rpc.go），按时间顺序逐步取因子帧。
// 回放窗口是会话开始时从本地存储取的快照，运行中不随修正更新；每期走实时计算路径（pipeline.Compute）；
// 回放不写结果、不发周期完成事件、不改连续水位。
//
//	OpenReplay   建立会话：校验数据、按预热区间读快照、预留内存
//	StepReplay   从某一期起算若干期，按（标的, series_tag）返回请求的列、上一期的同名列、生效参数与年龄
//	CloseReplay  结束会话，释放内存

const (
	// replayOpenTimeout 与目录里 OpenReplay 的路由超时（10 分钟）一致。
	replayOpenTimeout = 10 * time.Minute
	replayIdleTimeout = 6 * time.Hour
	replayReserveWait = time.Minute
	// replayStepMaxBytes 是单步响应的上限：服务的包体上限是默认的 4 MiB，留出余量。
	replayStepMaxBytes = 3 << 20
)

// ReplayDeps 是回放会话用到的引擎能力。
type ReplayDeps struct {
	Sets interface {
		SetByID(string) (domain.EngineTask, SetLookup)
	}
	Source SourceDataset
	Store  *seriesstore.Store
	// Synced 返回数据源上正在同步的标的（目录里同源启用任务的范围）：observed 成员的历史截面不纳入其他任务正在同步、
	// 而本任务范围不含的标的（与实时计算同一规则，localdata.HistoryRule）。nil 表示只看本任务的范围。
	Synced func(context.Context, seriesstore.Source) ([]string, error)
	Runner *pipeline.Runner
	Admit  *memadmit.Admitter
	Now    func() time.Time
	// Pin 非空时在会话建立期间固定任务引用的自定义算子模块（返回释放函数）：从读取任务到会话登记之间目录可能切换版本，
	// 没有它，这段时间里的回收会把会话马上要用的版本卸载。
	Pin func(set domain.EngineTask) func()
}

// replayError 带上回放失败对应的返回码：会话不存在（NOT_FOUND）时调用方要重新建立会话。
type replayError struct {
	code commonpb.ErrorCode
	err  error
}

func (e *replayError) Error() string { return e.err.Error() }
func (e *replayError) Unwrap() error { return e.err }

func replayFail(code commonpb.ErrorCode, format string, args ...any) error {
	return &replayError{code: code, err: fmt.Errorf(format, args...)}
}

func replayWrap(code commonpb.ErrorCode, err error) error { return &replayError{code: code, err: err} }

// replayCode 返回错误对应的返回码；没有标明的按内部错误。
func replayCode(err error) commonpb.ErrorCode {
	var typed *replayError
	if errors.As(err, &typed) {
		return typed.code
	}
	return commonpb.ErrorCode_INNER_ERR
}

type replayManager struct {
	deps ReplayDeps
	// maxStepBytes 是单步响应的上限（测试里调小）。
	maxStepBytes int

	mu       sync.Mutex
	sessions map[string]*replaySession
}

func newReplayManager(deps ReplayDeps) *replayManager {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &replayManager{deps: deps, maxStepBytes: replayStepMaxBytes, sessions: map[string]*replaySession{}}
}

// replayColumns 是会话请求的列，按来源分开：成员输出列（列名 → 成员键）、源数据集的数值列、是否要 __params。
type replayColumns struct {
	factors map[string]string
	source  []string
	params  bool
}

type replaySession struct {
	id  string
	set domain.EngineTask
	src seriesstore.Source
	// axis 是任务空间、频率的换算：会话内的周期都是轴上时间，请求与响应是真实时间。
	axis     periodclock.Axis
	step     time.Duration
	from, to time.Time // 决策周期的 data_time 范围 [from, to)（轴上）
	start    time.Time // 预热起点（轴上）
	subjects []string
	// history 是 observed 成员历史截面额外用到的标的：只在快照里提供历史，不出行。
	history  []string
	lookback int
	columns  replayColumns
	view     *serieswindow.View
	// earliest 是标的在本地数据里的第一根（各 series_tag 取最早）：本期的标的全集只含已经上市的标的。
	earliest map[string]time.Time
	// seriesEarliest 是每条序列在本地数据里的第一根：年龄按序列算。
	seriesEarliest map[seriesset.Series]time.Time
	release        func()

	mu       sync.Mutex
	closed   bool // 已关闭（预算已归还）：此后取得会话指针的请求不能再执行
	cache    map[int64]*replayPeriod
	lastUsed time.Time
	// applied 是各期实际生效的、由请求带来（或沿用上一期请求）的成员参数快照，键是期次的 Unix 秒：
	// 某期请求里的成员参数不合法时沿用上一期实际用的，而不是让回放整个失败。只留最近几期。
	applied map[int64]map[string]domain.ParamSnapshot
}

// ReplayParamRejection 是被拒绝的成员参数：本期沿用上一期实际用的参数（没有就按成员自己的参数）。
type ReplayParamRejection struct {
	Member string
	Reason string
}

// replayPeriod 是一期的计算结果（缓存，下一期取上一期的因子值用）；digest 是这一期所用参数的摘要；
// paramValues 是这一期各标的各成员实际用的参数（标的 → 成员键 → 参数名 → 值），策略的 params() 读它。
type replayPeriod struct {
	values      pipeline.FactorValues
	states      map[string]ReplayFactorState
	digest      string
	paramValues map[string]map[string]map[string]float64
}

// ReplayFactorState 是一个成员本期的状态：complete、degraded（部分标的失败）或 skipped（截面因子整体跳过）。
type ReplayFactorState struct {
	Status         string
	FactorID       string
	DefinitionHash string
	// Kind 是成员类型（fixed 或 var）：策略据此和启用时固化的成员契约比对。
	Kind           string
	FailedSubjects []string
}

// ReplayOpenRequest 是建立会话的请求。
type ReplayOpenRequest struct {
	ReplayID string
	TaskID   string
	// From、To 是回放区间（RFC3339，左闭右开）：决策周期是 data_time 落在其中的每一根 K 线。
	From string
	To   string
	// ExtraPeriods 是策略额外需要的回看根数（引用上一根的深度；年龄按本地数据里序列的第一根算，与快照长度无关）；预热起点 = from −（因子最大回看 − 1 + ExtraPeriods）。
	ExtraPeriods int
	// Columns 是每行要返回的列：成员输出列、源数据集的数值列与 __params。
	Columns []string
	// AllowShortWarmup 允许多数标的的本地数据不够预热区间：不加时多数标的预热不足会拒绝建立会话（因子前段会是空值，回放结果没有意义）。
	AllowShortWarmup bool
}

// ReplayFactor 描述回放固定下来的一个成员：Member 是成员键，Kind 是成员类型（fixed 或 var），Outputs 是它在结果数据集里的列名。
type ReplayFactor struct {
	Member         string
	FactorID       string
	Kind           string
	DefinitionHash string
	Outputs        []string
}

// ReplayExcluded 是被排除出回放的标的及原因。
type ReplayExcluded struct {
	Subject string
	Reason  string
}

// ReplayOpenResponse 是建立会话的结果。
type ReplayOpenResponse struct {
	ReplayID string
	Subjects []string
	Excluded []ReplayExcluded
	// PeriodTimes 是各决策周期的 data_time（真实时间的 Unix 秒，升序）：有交易时段的市场跳过休市，调用方按它逐期推进。
	PeriodTimes []int64
	Start       string
	StepSecs    int64
	Bytes       int64
	Factors     []ReplayFactor
	Fields      []string
	// ShortWarmup 是本地数据起点晚于预热起点的标的数（因子前段为空值）；ShortSamples 是其中的前几个。
	ShortWarmup  int
	ShortSamples []string
}

// Open 建立回放会话。同一个 replay_id 重复打开时先释放旧会话再重建。
func (m *replayManager) Open(ctx context.Context, req ReplayOpenRequest) (ReplayOpenResponse, error) {
	req.ReplayID, req.TaskID = strings.TrimSpace(req.ReplayID), strings.TrimSpace(req.TaskID)
	if req.ReplayID == "" || req.TaskID == "" {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "replay_id 与 task_id 是必填项")
	}
	if req.ExtraPeriods < 0 {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "extra_periods 不能为负数")
	}
	if len(req.Columns) == 0 {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "columns 是必填项：列出策略引用的列")
	}
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "from：%w", err)
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "to：%w", err)
	}
	set, lookup := m.deps.Sets.SetByID(req.TaskID)
	if lookup != SetFound {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_NOT_FOUND, "因子任务 %s 不在引擎目录里（未启用或结果数据集未就绪）", req.TaskID)
	}
	set.Members = CompileMembers(set.Members)
	if m.deps.Pin != nil {
		release := m.deps.Pin(set)
		defer release() // 会话登记之后由会话表保留它引用的版本（udfHashes）
		// 固定之前目录可能已经切换了版本：以固定之后的目录为准，不一致就让调用方重试。
		if current, lookup := m.deps.Sets.SetByID(req.TaskID); lookup != SetFound || taskUDFKey(current) != taskUDFKey(set) {
			return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_CONFLICT, "目录刚刚发生变化（任务引用的自定义算子版本切换或任务离开目录），请重试")
		}
	}
	step, err := seriesstore.Step(set.Set.Freq)
	if err != nil {
		return ReplayOpenResponse{}, replayWrap(commonpb.ErrorCode_INVALID_PARAM, err)
	}
	// 回放接口是换算边界：请求里的区间是真实时间，会话内全部换到轴上。
	axis, err := periodclock.AxisFor(set.Set.SpaceID, set.Set.Freq)
	if err != nil {
		return ReplayOpenResponse{}, replayWrap(commonpb.ErrorCode_INVALID_PARAM, err)
	}
	realFrom, realTo := from.UTC(), to.UTC()
	from, fromErr := axis.ToAxis(realFrom)
	to, toErr := axis.EndToAxis(realTo)
	if fromErr != nil || toErr != nil || !from.Before(to) {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "回放区间 [%s, %s) 必须按 %s 对齐且 from 早于 to", req.From, req.To, set.Set.Freq)
	}
	lookback := maxLookbackOf(set)
	start := from.Add(-time.Duration(lookback-1+req.ExtraPeriods) * step)
	last := to.Add(-step)
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}

	m.closeSession(req.ReplayID)
	fields, ok, err := m.deps.Store.Schema(src)
	if err != nil {
		return ReplayOpenResponse{}, err
	}
	if !ok {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_CONFLICT, "数据源 %s 在本地没有数据：先导入或启动同步", src)
	}
	numeric := serieswindow.NumericFields(fields)
	columns, err := parseReplayColumns(req.Columns, set, numeric)
	if err != nil {
		return ReplayOpenResponse{}, replayWrap(commonpb.ErrorCode_INVALID_PARAM, err)
	}
	candidates, err := setSubjects(ctx, m.deps.Source, set, nil)
	if err != nil {
		return ReplayOpenResponse{}, err
	}
	// 校验：本地数据覆盖到回放的最后一期。预热区间不够的标的照常参与（因子前段为空）。
	var subjects []string
	var excluded []ReplayExcluded
	earliest := map[string]time.Time{}
	seriesEarliest := map[seriesset.Series]time.Time{}
	for _, subject := range uniqueStrings(candidates) {
		b, has, err := m.deps.Store.Bounds(src, subject)
		if err != nil {
			return ReplayOpenResponse{}, err
		}
		switch {
		case !has:
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: "本地没有数据"})
		case b.Latest.Before(last):
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: fmt.Sprintf("本地数据只到 %s，不包含回放的最后一期（%s）", axis.Format(b.Latest), axis.Format(last))})
		case b.Earliest.After(last):
			excluded = append(excluded, ReplayExcluded{Subject: subject, Reason: fmt.Sprintf("本地数据从 %s 才开始，回放区间内没有数据", axis.Format(b.Earliest))})
		default:
			subjects = append(subjects, subject)
			earliest[subject] = b.Earliest
			tags, err := m.deps.Store.TagBounds(src, subject)
			if err != nil {
				return ReplayOpenResponse{}, err
			}
			for tag, bounds := range tags {
				seriesEarliest[seriesset.Series{SubjectID: subject, SeriesTag: tag}] = bounds.Earliest
			}
		}
	}
	if len(subjects) == 0 {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_CONFLICT, "没有任何标的的本地数据覆盖回放区间：先导入 %s 到 %s 的数据", realFrom.Format(time.RFC3339), realTo.Format(time.RFC3339))
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
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_CONFLICT, "%d/%d 个标的的本地数据晚于预热起点 %s（例如 %s），因子前段会是空值：请先补齐本地历史再发起（moox-factor-engine backfill / moox-cli import）",
			shortCount, len(subjects), axis.Format(start), strings.Join(shortSamples, "、"))
	}
	// observed 成员：历史截面还要用本地存储里在快照区间内有数据、但不在回放标的里的标的（含已下架的），与实时计算同一口径。
	history, err := m.replayHistory(ctx, src, set, candidates, subjects, start, to)
	if err != nil {
		return ReplayOpenResponse{}, err
	}
	periods := int(to.Sub(from) / step)
	rowsPerSubject := int(to.Sub(start) / step)
	need := int64(len(subjects)+len(history)) * int64(rowsPerSubject) * int64(len(numeric)*8+9)
	release := func() {}
	if m.deps.Admit != nil {
		waitCtx, cancel := context.WithTimeout(memadmit.WithPriority(ctx, memadmit.PriorityBackground), replayReserveWait)
		release, err = m.deps.Admit.Reserve(waitCtx, need)
		cancel()
		if err != nil {
			return ReplayOpenResponse{}, fmt.Errorf("回放快照需要约 %d 字节内存，预留不到：%w", need, err)
		}
	}
	cleanup := release
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()
	snapshot, err := m.deps.Store.ReadRange(ctx, src, append(append([]string(nil), subjects...), history...), start, to)
	if err != nil {
		return ReplayOpenResponse{}, fmt.Errorf("读取回放快照：%w", err)
	}
	view, err := serieswindow.FromColumns(src, int64(step/time.Second), numeric, snapshot)
	if err != nil {
		return ReplayOpenResponse{}, replayFail(commonpb.ErrorCode_CONFLICT, "回放快照不连续：%w", err)
	}
	periodTimes := make([]int64, 0, periods)
	for period := from; period.Before(to); period = period.Add(step) {
		at, err := axis.FromAxis(period)
		if err != nil {
			return ReplayOpenResponse{}, err
		}
		periodTimes = append(periodTimes, at.Unix())
	}
	session := &replaySession{
		id: req.ReplayID, set: set, src: src, axis: axis, step: step, from: from, to: to, start: start,
		subjects: subjects, history: history, lookback: lookback, columns: columns, view: view,
		earliest: earliest, seriesEarliest: seriesEarliest, release: release,
		cache: map[int64]*replayPeriod{}, applied: map[int64]map[string]domain.ParamSnapshot{}, lastUsed: m.deps.Now(),
	}
	cleanup = nil
	m.mu.Lock()
	m.sessions[req.ReplayID] = session
	m.mu.Unlock()
	rsp := ReplayOpenResponse{
		ReplayID: req.ReplayID, Subjects: subjects, Excluded: excluded, PeriodTimes: periodTimes, Start: axis.Format(start),
		StepSecs: int64(step / time.Second), Bytes: need, Fields: numeric, ShortWarmup: shortCount, ShortSamples: shortSamples,
	}
	for _, member := range set.Members {
		rsp.Factors = append(rsp.Factors, ReplayFactor{Member: member.Key, FactorID: member.Factor.FactorID,
			DefinitionHash: domain.DefinitionHash(member.Factor), Kind: member.Kind, Outputs: member.OutputColumns()})
	}
	return rsp, nil
}

// parseReplayColumns 把请求的列分到三类：成员输出列、源数据集的数值列、__params。不认识的列名是调用方的错误。
func parseReplayColumns(requested []string, set domain.EngineTask, numeric []string) (replayColumns, error) {
	outputs := map[string]string{}
	for _, member := range set.Members {
		for _, column := range member.OutputColumns() {
			outputs[column] = member.Key
		}
	}
	source := map[string]bool{}
	for _, name := range numeric {
		source[name] = true
	}
	out := replayColumns{factors: map[string]string{}}
	seen := map[string]bool{}
	for _, column := range requested {
		column = strings.TrimSpace(column)
		if column == "" || seen[column] {
			continue
		}
		seen[column] = true
		switch {
		case column == factorname.ParamsColumn:
			out.params = true
		case outputs[column] != "":
			out.factors[column] = outputs[column]
		case source[column]:
			out.source = append(out.source, column)
		default:
			return replayColumns{}, fmt.Errorf("列 %s 既不是任务成员的输出列，也不是源数据集的数值列", column)
		}
	}
	sort.Strings(out.source)
	return out, nil
}

// ReplayStepRequest 是一步的请求。
type ReplayStepRequest struct {
	ReplayID string
	// Period 是这一步第一期的 data_time（真实时间的 Unix 秒）；Periods 是这一步最多算几期。
	Period  int64
	Periods int
	// Params 是这一步第一期各可变成员的参数快照（成员键 → 快照）：由策略在上一期求值后产出，只存在于回放会话里，
	// 不写成员行。没有给出的成员沿用上一期实际用的参数，再没有就用成员自己的参数（可变成员的默认参数）；这一步后面的各期都沿用。
	Params map[string]domain.ParamSnapshot
}

// ReplayRow 是一条序列本期的行：只含会话请求的列，没有数据、算不出的列不出现。
type ReplayRow struct {
	Series seriesset.Series
	// Values 是请求的列本期的值，Previous 是上一期的。
	Values   map[string]float64
	Previous map[string]float64
	// Params 是本期各成员（成员键）实际用的参数（参数名 → 值）；只在请求了 __params 时给。
	Params map[string]map[string]float64
	// AgeBars 是本地数据里这条序列截至本期的根数（含本期）：策略的年龄检查用。
	AgeBars int
}

// ReplayPeriodFrame 是一期的因子帧。
type ReplayPeriodFrame struct {
	// Period 是这一期的 data_time（真实时间的 Unix 秒）。
	Period int64
	Rows   []ReplayRow
	States map[string]ReplayFactorState
	// AppliedParams 是本期实际生效的、由请求带来（或沿用之前请求）的成员参数：策略回放以它为基础叠加下一期的新参数，
	// 不合法的请求参数不会留在回放记录里。RejectedParams 是本期被拒绝的成员与原因（只在一步的第一期）。
	AppliedParams  map[string]domain.ParamSnapshot
	RejectedParams []ReplayParamRejection
}

// ReplayStepResponse 是一步的结果：Periods 从请求的期起连续，wire 是它们的线上格式（量大小时已经转换过）。
type ReplayStepResponse struct {
	Periods       []ReplayPeriodFrame
	ComputeMillis int64
	wire          []*factorpb.ReplayPeriod
}

func (m *replayManager) session(id string) *replaySession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// Step 从请求的期起按顺序算最多 Periods 期。单步响应不超过 maxStepBytes：放不下时只返回前面几期（至少 1 期），
// 一期就放不下时拒绝。同一期可重复请求：结果由快照与参数决定，重复计算得到同样的值。
func (m *replayManager) Step(ctx context.Context, req ReplayStepRequest) (ReplayStepResponse, error) {
	s := m.session(strings.TrimSpace(req.ReplayID))
	if s == nil {
		return ReplayStepResponse{}, replayFail(commonpb.ErrorCode_NOT_FOUND, "回放会话 %s 不存在（引擎重启或已结束），请重新建立会话", req.ReplayID)
	}
	if req.Periods < 1 {
		return ReplayStepResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "periods 至少为 1")
	}
	realPeriod := time.Unix(req.Period, 0).UTC()
	period, axisErr := s.axis.ToAxis(realPeriod)
	if axisErr != nil || period.Before(s.from) || !period.Before(s.to) {
		return ReplayStepResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "周期 %s 不在回放区间 [%s, %s) 内或没有对齐",
			realPeriod.Format(time.RFC3339), s.axis.Format(s.from), s.axis.Format(s.to))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ReplayStepResponse{}, replayFail(commonpb.ErrorCode_NOT_FOUND, "回放会话 %s 已关闭，请重新建立会话", req.ReplayID)
	}
	s.lastUsed = m.deps.Now()
	started := time.Now()
	var rsp ReplayStepResponse
	size := 0
	requested := req.Params
	for at := period; at.Before(s.to) && len(rsp.Periods) < req.Periods; at = at.Add(s.step) {
		frame, err := s.stepPeriod(ctx, m.deps.Runner, at, requested)
		if err != nil {
			return ReplayStepResponse{}, err
		}
		requested = nil // 请求的参数只给第一期，之后各期沿用
		wire := replayPeriodToPB(frame)
		// 每期在响应里是一个带长度前缀的字段：多算 10 字节的标签与长度。
		n := proto.Size(wire) + 10
		if size+n > m.maxStepBytes {
			if len(rsp.Periods) == 0 {
				return ReplayStepResponse{}, replayFail(commonpb.ErrorCode_INVALID_PARAM, "一期的因子帧约 %d 字节，超过单步上限 %d 字节：减少引用的列", n, m.maxStepBytes)
			}
			break
		}
		size += n
		rsp.Periods = append(rsp.Periods, frame)
		rsp.wire = append(rsp.wire, wire)
	}
	rsp.ComputeMillis = time.Since(started).Milliseconds()
	return rsp, nil
}

// stepPeriod 算一期并组装这一期的帧（调用方持有会话锁）。
func (s *replaySession) stepPeriod(ctx context.Context, runner *pipeline.Runner, period time.Time, requested map[string]domain.ParamSnapshot) (ReplayPeriodFrame, error) {
	params, applied, rejected, err := s.params(period, requested)
	if err != nil {
		return ReplayPeriodFrame{}, replayWrap(commonpb.ErrorCode_INVALID_PARAM, err)
	}
	current, err := s.compute(ctx, runner, period, params)
	if err != nil {
		return ReplayPeriodFrame{}, err
	}
	prevAt := period.Add(-s.step)
	previous := s.cache[prevAt.Unix()]
	if previous == nil && !prevAt.Before(s.start) {
		prevParams, _, _, err := s.params(prevAt, nil)
		if err != nil {
			return ReplayPeriodFrame{}, err
		}
		if previous, err = s.compute(ctx, runner, prevAt, prevParams); err != nil {
			return ReplayPeriodFrame{}, err
		}
	}
	s.applied[period.Unix()] = applied
	for at := range s.applied {
		if at < period.Unix()-int64(4*s.step/time.Second) {
			delete(s.applied, at)
		}
	}
	realPeriod, err := s.axis.FromAxis(period)
	if err != nil {
		return ReplayPeriodFrame{}, err
	}
	frame := ReplayPeriodFrame{Period: realPeriod.Unix(), States: current.states, AppliedParams: applied, RejectedParams: rejected}
	now, prev := period.Unix(), prevAt.Unix()
	stepSecs := int64(s.step / time.Second)
	for _, subject := range s.subjects {
		for _, sv := range s.view.Series(subject) {
			// 这条序列本期在快照里没有行（还没上市、已经结束）时不出行；出不出行与请求了哪些列无关。
			if !seriesHasRow(s.view, sv, now) {
				continue
			}
			key := seriesset.Series{SubjectID: subject, SeriesTag: sv.Tag}
			row := ReplayRow{Series: key, Values: map[string]float64{}}
			for _, name := range s.columns.source {
				if v, ok := seriesValue(s.view, sv, name, now); ok {
					row.Values[name] = v
				}
				if v, ok := seriesValue(s.view, sv, name, prev); ok {
					row.setPrevious(name, v)
				}
			}
			for column, member := range s.columns.factors {
				if v, ok := current.values[key][member][column]; ok {
					row.Values[column] = v
				}
				if previous != nil {
					if v, ok := previous.values[key][member][column]; ok {
						row.setPrevious(column, v)
					}
				}
			}
			if s.columns.params {
				row.Params = current.paramValues[subject]
			}
			if first, ok := s.seriesEarliest[key]; ok {
				row.AgeBars = int((now-first.Unix())/stepSecs) + 1
			}
			frame.Rows = append(frame.Rows, row)
		}
	}
	// 只保留当期与上一期的结果，缓存不随回放长度增长。
	for key := range s.cache {
		if key < prev {
			delete(s.cache, key)
		}
	}
	return frame, nil
}

func (r *ReplayRow) setPrevious(name string, v float64) {
	if r.Previous == nil {
		r.Previous = map[string]float64{}
	}
	r.Previous[name] = v
}

// seriesValue 返回一条序列在 atSec 那一根某个数值字段的值；没有这一根、值为 NaN 时 ok 为 false。
// 不用 View.ValueAt：它在标的的各序列里取第一条覆盖这一时刻的，统一数据集上两条序列会拿到同一个值。
func seriesValue(view *serieswindow.View, sv serieswindow.SeriesView, field string, atSec int64) (float64, bool) {
	idx, ok := view.FieldIndex(field)
	if !ok || atSec < sv.Start || atSec > sv.End(view.Step) {
		return 0, false
	}
	value := sv.Cols[idx][(atSec-sv.Start)/view.Step]
	if math.IsNaN(value) {
		return 0, false
	}
	return value, true
}

// seriesHasRow 报告这条序列在 atSec 那一根有没有真实的源行：有任何一个数值字段的值、且不是填充行。填充行是同步补上的缺失周期
// （OHLC 取上一根收盘、成交量为 0），与实时一样不算本期有行（实时把目标那一根是填充行的序列判为本期不可用，见 localdata.missingTagsAt）。
func seriesHasRow(view *serieswindow.View, sv serieswindow.SeriesView, atSec int64) bool {
	if atSec < sv.Start || atSec > sv.End(view.Step) {
		return false
	}
	i := (atSec - sv.Start) / view.Step
	if int(i) < len(sv.Filled) && sv.Filled[i] {
		return false
	}
	for _, col := range sv.Cols {
		if !math.IsNaN(col[i]) {
			return true
		}
	}
	return false
}

// udfHashes 返回现有回放会话引用的自定义算子版本：目录切换后这些版本仍不能卸载（会话的计算还要用）。
func (m *replayManager) udfHashes() map[string]bool {
	out := map[string]bool{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		for _, ref := range refsOfTasks([]domain.EngineTask{s.set}) {
			out[ref.hash] = true
		}
	}
	return out
}

// params 返回一期各成员用的参数：请求带来的快照（校验合法）优先，其次是沿用上一期实际用的请求参数，都没有时按成员自己的参数。
// 请求里某个成员的参数不合法（范围、类型、联合约束，或成员是固定成员）时，和实时提交被拒绝一样沿用上一期实际用的参数，
// 并作为拒绝返回；成员键不存在是调用方的错误，仍然整期失败。
// 第二个返回值是本期实际生效的请求参数（来自请求，或沿用上一期请求）。
func (s *replaySession) params(period time.Time, requested map[string]domain.ParamSnapshot) (map[string]domain.ParamSnapshot, map[string]domain.ParamSnapshot, []ReplayParamRejection, error) {
	for key := range requested {
		found := false
		for _, member := range s.set.Members {
			found = found || member.Key == key
		}
		if !found {
			return nil, nil, nil, fmt.Errorf("任务里没有成员 %s", key)
		}
	}
	out := make(map[string]domain.ParamSnapshot, len(s.set.Members))
	var applied map[string]domain.ParamSnapshot
	var rejected []ReplayParamRejection
	apply := func(key string, snapshot domain.ParamSnapshot) {
		out[key] = snapshot
		if applied == nil {
			applied = map[string]domain.ParamSnapshot{}
		}
		applied[key] = snapshot
	}
	for _, member := range s.set.Members {
		if !member.IsVar() {
			if _, ok := requested[member.Key]; ok {
				rejected = append(rejected, ReplayParamRejection{Member: member.Key, Reason: fmt.Sprintf("成员 %s 是固定成员，不接受逐期提交的参数", member.Key)})
			}
			continue
		}
		if snapshot, ok := requested[member.Key]; ok {
			err := domain.CheckSnapshotNumbers(snapshot)
			if err == nil {
				err = domain.ValidateSnapshot(member, snapshot, nil)
			}
			if err == nil {
				apply(member.Key, snapshot)
				continue
			}
			rejected = append(rejected, ReplayParamRejection{Member: member.Key, Reason: err.Error()})
		}
		// 沿用上一期实际生效的参数；没有就留空，由成员自己的参数计算。
		if previous, ok := s.applied[period.Add(-s.step).Unix()][member.Key]; ok {
			apply(member.Key, previous)
		}
	}
	return out, applied, rejected, nil
}

func paramsDigest(params map[string]domain.ParamSnapshot) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		b.WriteString(key + "=" + params[key].Hash() + ";")
	}
	return b.String()
}

// compute 用快照算一期（实时计算路径）；同一期、同一组参数已经算过时直接取缓存（调用方持有会话锁）。
func (s *replaySession) compute(ctx context.Context, runner *pipeline.Runner, period time.Time, params map[string]domain.ParamSnapshot) (*replayPeriod, error) {
	digest := paramsDigest(params)
	if cached := s.cache[period.Unix()]; cached != nil && cached.digest == digest {
		return cached, nil
	}
	// 本期的标的全集只含这一期已经上市（本地最早一根不晚于本期）的标的：未来才出现的标的不能改变早期周期的截面与「缺数」判定。
	var universe []string
	for _, subject := range s.subjects {
		if first, ok := s.earliest[subject]; ok && !first.After(period) {
			universe = append(universe, subject)
		}
	}
	// 回放按标的选范围：序列全集取“标的 × 快照里它的 series_tag”，本期可用的标的它的各条序列都算可用，
	// 标的缺数就是它的序列缺数。
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
		Set: s.set.Set, Members: s.set.Members, Params: params, PeriodTime: period,
		UniverseSeries: pipeline.SubjectSeries(s.view, universe),
	})
	if err != nil {
		return nil, err
	}
	// 本期可用按序列判断，与实时一致：这条序列本期有真实的源行（填充行不算）；至少有一条可用序列的标的才是可用标的。
	var available []string
	var availableSeries []seriesset.Series
	for _, subject := range universe {
		has := false
		for _, sv := range s.view.Series(subject) {
			if seriesHasRow(s.view, sv, period.Unix()) {
				has = true
				availableSeries = append(availableSeries, seriesset.Series{SubjectID: subject, SeriesTag: sv.Tag})
			}
		}
		if has {
			available = append(available, subject)
		}
	}
	plan.Available = available
	if plan.Observed() {
		// 与实时计算相同：本期不可用的回放标的与历史标的都放进历史截面（数据截至本期，未来的行已被截掉）。
		inAvailable := make(map[string]struct{}, len(available))
		for _, subject := range available {
			inAvailable[subject] = struct{}{}
		}
		plan.HistorySubjects = []string{}
		for _, subject := range append(append([]string(nil), s.subjects...), s.history...) {
			if _, ok := inAvailable[subject]; !ok {
				plan.HistorySubjects = append(plan.HistorySubjects, subject)
			}
		}
	}
	loaded := pipeline.LoadResult{View: s.view.Truncate(period.Unix(), s.lookback+1), Available: available,
		AvailableSeries: availableSeries, Frames: map[string]*storageio.Frame{}}
	computation, err := runner.Compute(ctx, plan, loaded)
	if err != nil {
		return nil, fmt.Errorf("计算 %s：%w", s.axis.Format(period), err)
	}
	values, err := pipeline.PeriodValues(plan, computation, period)
	if err != nil {
		return nil, err
	}
	states := make(map[string]ReplayFactorState, len(computation.FactorStates))
	for key, state := range computation.FactorStates {
		states[key] = ReplayFactorState{Status: state.Status, FactorID: state.FactorID, DefinitionHash: state.DefinitionHash,
			Kind: state.MemberKind, FailedSubjects: state.FailedSubjects}
	}
	result := &replayPeriod{values: values, states: states, digest: digest, paramValues: paramValues(plan, universe)}
	s.cache[period.Unix()] = result
	return result, nil
}

// replayHistory 返回有 observed 成员时快照区间 [from, to) 的历史截面标的：按 localdata.HistoryRule，本任务范围是回放的候选标的，
// 回放标的本身不算（它们每期按是否可用决定）。
func (m *replayManager) replayHistory(ctx context.Context, src seriesstore.Source, set domain.EngineTask, candidates, subjects []string, from, to time.Time) ([]string, error) {
	observed := false
	for _, member := range set.Members {
		observed = observed || member.Factor.ObservedHistory()
	}
	if !observed {
		return nil, nil
	}
	var synced []string
	if m.deps.Synced != nil {
		var err error
		if synced, err = m.deps.Synced(ctx, src); err != nil {
			return nil, fmt.Errorf("列出数据源上正在同步的标的：%w", err)
		}
	}
	return localdata.HistoryCandidates(m.deps.Store, src, from, to,
		localdata.HistoryRule{Exclude: subjects, Own: candidates, Synced: append(synced, candidates...)})
}

// closeSession 等正在执行的 Step 结束之后才归还内存预算并移出会话表（Step 持有会话锁整个过程）：
// 旧请求还握着整个快照时预算不能先还出去，否则新会话可以再次占用这部分预算；移出会话表也在这之后，
// 等待期间会话引用的自定义算子版本仍被保留。
func (m *replayManager) closeSession(id string) bool {
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	s.release()
	s.mu.Unlock()
	m.mu.Lock()
	if m.sessions[id] == s {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	return true
}

// Sweep 关闭长时间没有请求的会话（调用方异常退出后不会来关）。
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

// paramValues 返回这一期各标的各成员实际用的参数（标的 → 成员键 → 参数名 → 值）。
func paramValues(plan pipeline.Plan, universe []string) map[string]map[string]map[string]float64 {
	out := make(map[string]map[string]map[string]float64, len(universe))
	for _, subject := range universe {
		byMember := make(map[string]map[string]float64, len(plan.Members))
		for _, member := range plan.Members {
			byMember[member.Key] = pipeline.MemberParamValues(plan, member, subject)
		}
		out[subject] = byMember
	}
	return out
}

// taskUDFKey 是任务成员引用的自定义算子绑定的指纹（成员键 → 名称@哈希）：会话建立时据此判断期间是否切换过版本。
func taskUDFKey(set domain.EngineTask) string {
	var parts []string
	for _, member := range set.Members {
		if key := domain.UDFKey(member.Factor.UDFs); key != "" {
			parts = append(parts, member.Key+"="+key)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
