// Package readiness 判定一条 ViewDataReady 事件对某个实例是否可以求值：
// 比对事件中的因子状态、启用时固化的因子指纹，以及上一根的可信处理记录。
// 它是纯函数，不访问存储；相邻记录由调用方按可信条件查出后传入。
package readiness

import (
	"fmt"
	"sort"
	"strings"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

// PeriodDetailType 是因子周期明细在 ViewDataReady.detail_type 里的取值，与因子模块写入时一致。
const PeriodDetailType = "moox.factor.PeriodDetail.v1"

// 跳过原因。
const (
	ReasonFactorMissing          = "factor_missing"
	ReasonFactorChanged          = "factor_changed"
	ReasonPreviousVersionUnknown = "previous_version_unknown"
	ReasonFactorSkipped          = "factor_skipped"
)

// 事件中的因子状态。
const (
	statusComplete = "complete"
	statusDegraded = "degraded"
	statusSkipped  = "skipped"
)

// Binding 是启用时固化的、与就绪判定有关的绑定信息。
type Binding struct {
	// Factors 是实例引用的因子成员（成员键）及其固化的成员契约（见 Contract）；只引用源列时为空。
	Factors map[string]string
	// PreviousFactors 是经 bars[-1] 读取的因子列所属的因子：上一根的值可能来自旧版本，需要上一根的版本证据；
	// 只经 bars[-1] 读取 K 线列时为空。
	PreviousFactors []string
}

// Contract 是成员契约：定义哈希加成员类型。成员键已经带了固定参数，所以契约一致就意味着这个成员算的是同一件事；
// 定义改了表达式、成员从固定换成可变（键也会变）都会让契约不同。
func Contract(definitionHash, memberKind string) string {
	return strings.TrimSpace(definitionHash) + "|" + strings.TrimSpace(memberKind)
}

// Record 是本实例对恰好相邻上一根的可信处理记录（ok 或 skipped 均可）中保存的因子指纹。
type Record struct {
	Factors map[string]string
}

// Result 是判定结果。Reason 为空表示可以继续求值。
type Result struct {
	Reason string
	Detail string
	// Hashes 是本期事件中各所需因子的指纹，供结果记录保存，下一期据此比对。
	Hashes map[string]string
	// FailedByFactor 是 degraded 因子对应的失败标的，引用该因子的规则把它们视为缺数。
	FailedByFactor map[string][]string
	// GapByFactor 是 FailedByFactor 中因回看窗口 K 线空洞而失败的标的（子集）：持仓的标的缺数且全部原因都是空洞时，策略保持持仓而不是退出。
	GapByFactor map[string][]string
	// FailedSubjects 是没有因子状态的 View（K 线 View）在 degraded 时声明失败的标的，所有列都视为缺数。
	FailedSubjects []string
}

// Check 按设计文档 3.3 的表格顺序判定：因子缺失 → 事件指纹变化 → 上一根版本证据 → 因子跳过 → 降级标的。
func Check(binding Binding, adjacent *Record, event *storagepb.ViewDataReady) Result {
	result := Result{Hashes: make(map[string]string, len(binding.Factors)), FailedByFactor: map[string][]string{}, GapByFactor: map[string][]string{}}
	states, err := indexFactors(event)
	if err != nil && len(binding.Factors) > 0 {
		result.Reason = ReasonFactorMissing
		result.Detail = fmt.Sprintf("事件里的因子周期明细无法解析：%v", err)
		return result
	}
	required := sortedKeys(binding.Factors)
	// 先收齐事件里已有的全部指纹再判缺失：缺失的因子不能让其余因子的指纹没有保存，否则下一期经 bars[-1] 读取这些因子时，
	// 相邻记录里缺少它们的证据，误记 previous_version_unknown。
	for _, factorID := range required {
		state, ok := states[factorID]
		if !ok || strings.TrimSpace(state.GetDefinitionHash()) == "" || strings.TrimSpace(state.GetMemberKind()) == "" {
			if result.Reason == "" {
				result.Reason = ReasonFactorMissing
				result.Detail = fmt.Sprintf("事件中没有成员 %s 的状态或契约", factorID)
			}
			continue
		}
		result.Hashes[factorID] = Contract(state.GetDefinitionHash(), state.GetMemberKind())
	}
	if result.Reason != "" {
		return result
	}
	for _, factorID := range required {
		if expected, actual := binding.Factors[factorID], result.Hashes[factorID]; actual != expected {
			result.Reason = ReasonFactorChanged
			result.Detail = fmt.Sprintf("成员 %s 的契约已从 %s 变为 %s，需要重新启用实例", factorID, expected, actual)
			return result
		}
	}
	if len(binding.PreviousFactors) > 0 {
		if adjacent == nil {
			result.Reason = ReasonPreviousVersionUnknown
			result.Detail = "DSL 经 bars[-1] 读取因子列，但上一根没有可信的处理记录"
			return result
		}
		for _, factorID := range binding.PreviousFactors {
			previous, ok := adjacent.Factors[factorID]
			if !ok || previous == "" {
				result.Reason = ReasonPreviousVersionUnknown
				result.Detail = fmt.Sprintf("上一根的处理记录没有成员 %s 的契约", factorID)
				return result
			}
			if previous != result.Hashes[factorID] {
				result.Reason = ReasonFactorChanged
				result.Detail = fmt.Sprintf("成员 %s 上一根的契约 %s 与本期 %s 不同，上一根数据可能属于旧版本", factorID, previous, result.Hashes[factorID])
				return result
			}
		}
	}
	for _, factorID := range required {
		state := states[factorID]
		switch state.GetStatus() {
		case statusSkipped:
			result.Reason = ReasonFactorSkipped
			result.Detail = fmt.Sprintf("成员 %s 本期被跳过", factorID)
			return result
		case statusDegraded:
			failed := uniqueSorted(state.GetFailedSubjects())
			if len(failed) > 0 {
				result.FailedByFactor[factorID] = failed
			}
			if gaps := uniqueSorted(state.GetGapSubjects()); len(gaps) > 0 {
				result.GapByFactor[factorID] = gaps
			}
		}
	}
	if len(states) == 0 && event.GetStatus() == statusDegraded {
		result.FailedSubjects = parseScopeRef(event.GetFailedScopeRef())
	}
	return result
}

// EncodeDetail 把因子成员状态编码成 ViewDataReady 的 detail_type 与 detail（回放据此构造事件）。
func EncodeDetail(factors []*factorpb.FactorPeriodState) (string, []byte, error) {
	raw, err := proto.Marshal(&factorpb.PeriodDetail{Factors: factors})
	return PeriodDetailType, raw, err
}

// indexFactors 解码事件 detail 里的因子周期明细，按成员键索引因子状态（同一因子可以用不同参数出现多次）。
// 不带因子明细的事件（K 线 View）返回空表。
func indexFactors(event *storagepb.ViewDataReady) (map[string]*factorpb.FactorPeriodState, error) {
	if event.GetDetailType() != PeriodDetailType || len(event.GetDetail()) == 0 {
		return map[string]*factorpb.FactorPeriodState{}, nil
	}
	detail := &factorpb.PeriodDetail{}
	if err := proto.Unmarshal(event.GetDetail(), detail); err != nil {
		return map[string]*factorpb.FactorPeriodState{}, err
	}
	states := make(map[string]*factorpb.FactorPeriodState, len(detail.GetFactors()))
	for _, state := range detail.GetFactors() {
		if state == nil {
			continue
		}
		id := strings.TrimSpace(state.GetMemberKey())
		if id == "" {
			continue
		}
		states[id] = state
	}
	return states, nil
}

// parseScopeRef 解析 View 事件的 failed_scope_ref：逗号分隔的标的列表；字面量 degraded 表示没有具体标的。
func parseScopeRef(ref string) []string {
	subjects := make([]string, 0)
	for _, token := range strings.Split(ref, ",") {
		token = strings.TrimSpace(token)
		if token == "" || token == statusDegraded {
			continue
		}
		subjects = append(subjects, token)
	}
	return uniqueSorted(subjects)
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return sortedKeys(set)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
