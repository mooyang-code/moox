// Package verify 判断一次安装是否真的有数据：K 线入库、因子前进、策略在运行。只做判断，数据由调用方读取。
package verify

import (
	"fmt"
	"time"
)

// 检查结果。
const (
	Pass = "pass"
	Wait = "wait"
	Fail = "fail"
	Skip = "skip"
)

// 失败时的错误码，install.md 的处理表要逐项对应。
const (
	CodeCollectionNotStarted = "collection_not_started"
	CodeNoCollectionTasks    = "no_collection_tasks"
	CodeReadFailed           = "verify_read_failed"
	CodeKlineNotArrived      = "kline_not_arrived"
	CodeFactorNotAdvancing   = "factor_not_advancing"
	CodeStrategyNotRunning   = "strategy_not_running"
)

// maxMissingRatio 是允许的 K 线缺失比例：到截止时间就带缺口结算，缺口靠之后补采，要求零缺失可能永远走不完。
const maxMissingRatio = 0.05

// firstBarWait 是生效周期之后等第一根 K 线的时间：超过它 expected 仍是 0，说明这段时间本来就没有 K 线（休市）。
const firstBarWait = 3 * time.Minute

// Dataset 是一个启用任务的结果数据集。Registered 表示已进入采集计划，Frequency、EffectiveFrom 只在这时有值。
type Dataset struct {
	ID            string
	Registered    bool
	Frequency     string
	EffectiveFrom time.Time
	Expected      int
	Missing       int
}

// Factor 是一个启用的因子任务。Baseline 是本次验收开始时的最后一期，SourceEnabled 表示源数据集在采集。
type Factor struct {
	TaskID        string
	Frequency     string
	LastPeriod    int64
	Baseline      int64
	LastStatus    string
	SourceEnabled bool
}

// Instance 是一个策略实例。
type Instance struct {
	ID        string
	Enabled   bool
	SessionID string
}

// Input 是一轮验收读到的数据。
type Input struct {
	Now      time.Time
	Datasets []Dataset
	// TasksDisabled 表示空间有采集任务但全部停用：还没有运行 setup start。
	TasksDisabled  bool
	HasPlan        bool
	NeedsStream    bool
	StreamOnline   bool
	PlanIssue      string
	FactorDeployed bool
	Factors        []Factor
	Instances      []Instance
}

// Check 是一项检查的结果。
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Evaluate 判断这一轮的结果。这里只给 pass、wait、skip：等到超时才算失败，由 Finish 转换。
func Evaluate(in Input) []Check {
	kline := klines(in)
	return []Check{plan(in), kline, factors(in, kline.Status == Skip), strategies(in)}
}

func plan(in Input) Check {
	check := Check{Name: "plan", Code: CodeCollectionNotStarted}
	switch {
	case len(in.Datasets) == 0 && in.TasksDisabled:
		// 任务默认停用，全停用就是还没启动，不能算通过。
		check.Status, check.Detail = Fail, "这个空间的采集任务都停用了，先运行 setup start --space-id <空间>"
	case len(in.Datasets) == 0:
		// 没有任务就没有数据，空间写错或种子没写入时不能算通过。
		check.Status, check.Code, check.Detail = Fail, CodeNoCollectionTasks, "这个空间没有启用的 K 线任务"
	case !in.HasPlan:
		check.Status, check.Detail = Wait, "还没有采集计划"
		if in.PlanIssue != "" {
			check.Detail += "：" + in.PlanIssue
		}
	case in.NeedsStream && !in.StreamOnline:
		check.Status, check.Detail = Wait, "collector-stream 不在线"
	default:
		check.Status, check.Code = Pass, ""
	}
	return check
}

func klines(in Input) Check {
	check := Check{Name: "kline", Code: CodeKlineNotArrived}
	if len(in.Datasets) == 0 {
		check.Status, check.Code, check.Detail = Skip, "", "这个空间没有启用的 K 线任务"
		return check
	}
	expected, missing, settled := 0, 0, 0
	for _, dataset := range in.Datasets {
		if !dataset.Registered {
			check.Status, check.Detail = Wait, "数据集 "+dataset.ID+" 还没进入采集计划"
			return check
		}
		if dataset.Expected == 0 {
			if dataset.Frequency == "1m" && in.Now.Before(dataset.EffectiveFrom.Add(firstBarWait)) {
				check.Status, check.Detail = Wait, "数据集 "+dataset.ID+" 的第一根 K 线还没收盘"
				return check
			}
			continue // 休市或频率大于 1m：最近几期本来就没有 K 线。
		}
		// 逐个数据集判断，小数据集全缺不能被大数据集稀释。
		if float64(dataset.Missing) > float64(dataset.Expected)*maxMissingRatio {
			check.Status, check.Detail = Wait, fmt.Sprintf("数据集 %s 缺失 %d/%d 根", dataset.ID, dataset.Missing, dataset.Expected)
			return check
		}
		expected += dataset.Expected
		missing += dataset.Missing
		settled++
	}
	if settled == 0 {
		check.Status, check.Code, check.Detail = Skip, "", "最近几期没有应有的 K 线，按休市处理"
		return check
	}
	check.Status, check.Code, check.Detail = Pass, "", fmt.Sprintf("缺失 %d/%d 根", missing, expected)
	return check
}

// factors 判断因子任务。marketClosed 时 1m 任务本来就不前进，只看最近一期没有失败。
func factors(in Input, marketClosed bool) Check {
	check := Check{Name: "factor", Code: CodeFactorNotAdvancing}
	if !in.FactorDeployed {
		check.Status, check.Code, check.Detail = Skip, "", "没有部署因子引擎"
		return check
	}
	checked := 0
	for _, factor := range in.Factors {
		if !factor.SourceEnabled {
			continue // 源数据集没在采集，这个任务本来就不会前进。
		}
		checked++
		// degraded 算通过：预热期状态就是它。
		if factor.LastStatus == "failed" {
			check.Status, check.Detail = Wait, "因子任务 "+factor.TaskID+" 最近一期失败"
			return check
		}
		if factor.Frequency == "1m" && !marketClosed && factor.LastPeriod <= factor.Baseline {
			check.Status, check.Detail = Wait, "因子任务 "+factor.TaskID+" 还没有前进"
			return check
		}
	}
	if checked == 0 {
		check.Status, check.Code, check.Detail = Skip, "", "没有读在采集数据的因子任务"
		return check
	}
	check.Status, check.Code = Pass, ""
	return check
}

func strategies(in Input) Check {
	check := Check{Name: "strategy", Code: CodeStrategyNotRunning}
	enabled := 0
	for _, instance := range in.Instances {
		if !instance.Enabled {
			continue
		}
		enabled++
		if instance.SessionID == "" {
			check.Status, check.Detail = Wait, "策略实例 "+instance.ID+" 还没有运行会话"
			return check
		}
	}
	if enabled == 0 {
		check.Status, check.Code, check.Detail = Skip, "", "没有启用的策略实例"
		return check
	}
	check.Status, check.Code = Pass, ""
	return check
}

// Settled 报告这一轮是否不用再等：已有失败项，或没有还在等的项。
func Settled(checks []Check) bool {
	waiting := false
	for _, check := range checks {
		if check.Status == Fail {
			return true
		}
		waiting = waiting || check.Status == Wait
	}
	return !waiting
}

// Finish 在超时后把还在等的项改成失败。
func Finish(checks []Check) []Check {
	out := make([]Check, len(checks))
	copy(out, checks)
	for i := range out {
		if out[i].Status == Wait {
			out[i].Status = Fail
		}
	}
	return out
}
