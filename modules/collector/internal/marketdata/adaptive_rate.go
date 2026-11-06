package marketdata

import (
	"errors"
	"math"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// 自适应速率只用在补采、追赶、缺口修复这类持续几分钟到几小时的请求上：配置里的每秒上限是已知安全的最大值，
// 实际速率从它开始，行情源给出压力信号就降，连续几个统计周期都健康、并且流量确实用到了当前速率再慢慢升回去。
// 实时采集每 5 分钟只有 1～2 秒的突发，来不及学习，不使用。
//
// 控制方式参考了 github.com/Yiming1997/agilelimiter（MIT）：按统计周期而不是按单次请求调节，
// 失败占比用统计误差判断，只在流量用到速率时才升，连续几个周期健康才升。它按延迟调速，我们的行情源限流时
// 不会变慢（直接返回 429、断连），所以信号换成了明确的限流、繁忙，以及超时、断连、5xx。
const (
	// adaptiveTick 是统计周期。
	adaptiveTick = time.Second
	// adaptiveDownStrong 是明确限流（429、繁忙）后的降速比例，立即生效；adaptiveDownMild 是弱信号（超时、断连、5xx）占比超标时的。
	adaptiveDownStrong = 0.5
	adaptiveDownMild   = 0.8
	// adaptiveUp 是升速比例；需要连续 adaptiveRecoverTicks 个健康周期，且降速后 adaptiveHold 内不升。
	adaptiveUp           = 1.1
	adaptiveRecoverTicks = 2
	adaptiveHold         = 3 * time.Second
	// 弱信号占比超过 adaptiveTolerance + adaptiveSigma × 标准误差才降速；一个周期少于 adaptiveMinSamples 个请求时不判断。
	adaptiveTolerance  = 0.05
	adaptiveSigma      = 3.0
	adaptiveMinSamples = 5
	// adaptiveHeadroom：只有这个周期实际用掉的速率不少于当前速率的 1/adaptiveHeadroom 才升，
	// 流量被别的因素（并发数、任务量）限制住、根本没用满速率时，升速没有意义，还会在流量变大时冲过安全线。
	adaptiveHeadroom = 2.0
	// adaptiveMinWindow 是计算实际用掉的速率时的最短窗口，避免几个请求挤在几毫秒里被算成很高的速率。
	adaptiveMinWindow = 500 * time.Millisecond
	// adaptiveFloorRatio 是速率下限相对配置上限的比例，adaptiveFloorMin 是绝对下限（每秒请求数）。
	adaptiveFloorRatio = 0.1
	adaptiveFloorMin   = 1.0
)

// AdaptiveRate 是一个行情源在本进程内共享的自适应速率（AIMD，按统计周期调节）。
type AdaptiveRate struct {
	provider string
	clock    Clock

	mu    sync.Mutex
	max   float64
	floor float64
	rate  float64

	// 当前统计周期：第一个与最后一个请求的时刻，成功、弱信号、强信号的个数。
	tickStart, lastObs time.Time
	ok, mild, strong   int

	healthy  int
	lastDown time.Time
	reduced  bool
}

func newAdaptiveRate(provider string, max float64, clock Clock) *AdaptiveRate {
	if clock == nil {
		clock = realClock{}
	}
	floor := math.Min(math.Max(max*adaptiveFloorRatio, adaptiveFloorMin), max)
	return &AdaptiveRate{provider: provider, clock: clock, max: max, floor: floor, rate: max}
}

// Rate 返回当前允许的每秒请求数。上一个统计周期已经结束时，先按它的结果调整。
func (a *AdaptiveRate) Rate() float64 {
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settle(now)
	return a.rate
}

type pressure int

const (
	pressureNone pressure = iota
	pressureMild
	pressureStrong
)

// pressureOf 把一次请求的结果归为压力信号：限流、繁忙是强信号；超时、断连、HTTP 状态异常是弱信号；
// 其余错误（还没出这根 K 线、标的不支持、解析失败）与速率无关，不参与调节。
func pressureOf(err error) (signal pressure, counted bool) {
	switch {
	case err == nil:
		return pressureNone, true
	case errors.Is(err, ErrRateLimited), errors.Is(err, ErrRemoteBusy):
		return pressureStrong, true
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrTCP), errors.Is(err, ErrHTTPStatus):
		return pressureMild, true
	}
	return pressureNone, false
}

// Observe 记录一次请求的结果。强信号立即降速；其余在统计周期结束时汇总判断。
func (a *AdaptiveRate) Observe(err error) {
	signal, counted := pressureOf(err)
	if !counted {
		return
	}
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.settle(now)
	if a.tickStart.IsZero() {
		a.tickStart = now
	}
	a.lastObs = now
	switch signal {
	case pressureNone:
		a.ok++
	case pressureMild:
		a.mild++
	case pressureStrong:
		a.strong++
		a.evaluate(now)
		return
	}
	if now.Sub(a.tickStart) >= adaptiveTick {
		a.evaluate(now)
	}
}

// settle 在上一个统计周期已经结束时先按它的结果调整（长时间没有请求之后的第一次调用）。
func (a *AdaptiveRate) settle(now time.Time) {
	if !a.tickStart.IsZero() && now.Sub(a.tickStart) >= adaptiveTick {
		a.evaluate(now)
	}
}

// evaluate 按当前统计周期的结果调整速率，然后开始新的周期。调用方持有 mu。
func (a *AdaptiveRate) evaluate(now time.Time) {
	n := a.ok + a.mild + a.strong
	strong, mild := a.strong, a.mild
	window := max(a.lastObs.Sub(a.tickStart), adaptiveMinWindow)
	a.ok, a.mild, a.strong = 0, 0, 0
	a.tickStart, a.lastObs = time.Time{}, time.Time{}
	if n == 0 {
		return
	}
	used := float64(n) / window.Seconds()

	share := float64(mild) / float64(n)
	sigma := math.Sqrt(adaptiveTolerance * (1 - adaptiveTolerance) / float64(n))
	mildExcess := n >= adaptiveMinSamples && share > adaptiveTolerance+adaptiveSigma*sigma
	switch {
	case strong > 0 || mildExcess:
		a.healthy = 0
		if now.Sub(a.lastDown) < adaptiveTick && !a.lastDown.IsZero() {
			return // 刚降过，等这次降速的效果出来
		}
		factor, cause := adaptiveDownMild, "弱信号占比超标"
		if strong > 0 {
			factor, cause = adaptiveDownStrong, "限流或繁忙"
		}
		previous := a.rate
		a.rate = math.Max(a.floor, a.rate*factor)
		a.lastDown = now
		a.reduced = true
		log.Warnf("adaptive rate down provider=%s rate=%.1f (from %.1f) cause=%s strong=%d mild=%d of %d", a.provider, a.rate, previous, cause, strong, mild, n)
	case mild == 0:
		a.healthy++
		if a.healthy < adaptiveRecoverTicks || a.rate >= a.max || now.Sub(a.lastDown) < adaptiveHold || used*adaptiveHeadroom < a.rate {
			return
		}
		a.healthy = 0
		previous := a.rate
		a.rate = math.Min(a.max, a.rate*adaptiveUp)
		if a.reduced && a.rate >= a.max {
			a.reduced = false
			log.Infof("adaptive rate recovered provider=%s rate=%.1f (from %.1f)", a.provider, a.rate, previous)
		}
	default:
		a.healthy = 0 // 有弱信号但没超标：保持
	}
}

var adaptiveRates sync.Map // 行情源 ID → *AdaptiveRate

// sharedAdaptiveRate 返回本进程内某个行情源共享的自适应速率；第一次取用时以配置的每秒上限作为最大值。
func sharedAdaptiveRate(provider string, max float64, clock Clock) *AdaptiveRate {
	if existing, ok := adaptiveRates.Load(provider); ok {
		return existing.(*AdaptiveRate)
	}
	created, _ := adaptiveRates.LoadOrStore(provider, newAdaptiveRate(provider, max, clock))
	return created.(*AdaptiveRate)
}
