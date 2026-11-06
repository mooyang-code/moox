package marketdata

import (
	"errors"
	"math"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

// 自适应速率只用在补采、追赶、缺口修复这类持续几分钟到几小时的请求上：配置里的每秒上限是已知安全的最大值，
// 实际速率从它开始，行情源给出压力信号（限流、繁忙、超时、断连）就降，连续成功再慢慢涨回去。
// 实时采集每 5 分钟只有 1～2 秒的突发，来不及学习，不使用。
const (
	// adaptiveDownStrong 是明确的限流（429、繁忙）后的降速比例，adaptiveDownMild 是超时、断连、5xx 后的。
	adaptiveDownStrong = 0.5
	adaptiveDownMild   = 0.8
	// adaptiveUp 是连续 adaptiveStreak 次成功后的升速比例；降速之后 adaptiveHold 内不升。
	adaptiveUp     = 1.1
	adaptiveStreak = 20
	adaptiveHold   = 3 * time.Second
	// adaptiveDownGap 是两次降速的最小间隔：一批并发请求同时失败只算一次压力信号，不会一下子降到底。
	adaptiveDownGap = time.Second
	// adaptiveFloorRatio 是速率下限相对配置上限的比例，adaptiveFloorMin 是绝对下限（每秒请求数）。
	adaptiveFloorRatio = 0.1
	adaptiveFloorMin   = 1.0
)

// AdaptiveRate 是一个行情源在本进程内共享的自适应速率（AIMD）。
type AdaptiveRate struct {
	provider string
	clock    Clock

	mu       sync.Mutex
	max      float64
	floor    float64
	rate     float64
	streak   int
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

// Rate 返回当前允许的每秒请求数。
func (a *AdaptiveRate) Rate() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
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

// Observe 记录一次请求的结果并调整速率。
func (a *AdaptiveRate) Observe(err error) {
	signal, counted := pressureOf(err)
	if !counted {
		return
	}
	now := a.clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if signal == pressureNone {
		a.streak++
		if a.streak < adaptiveStreak || a.rate >= a.max || now.Sub(a.lastDown) < adaptiveHold {
			return
		}
		a.streak = 0
		previous := a.rate
		a.rate = math.Min(a.max, a.rate*adaptiveUp)
		if a.reduced && a.rate >= a.max {
			a.reduced = false
			log.Infof("adaptive rate recovered provider=%s rate=%.1f (from %.1f)", a.provider, a.rate, previous)
		}
		return
	}
	a.streak = 0
	if now.Sub(a.lastDown) < adaptiveDownGap {
		return
	}
	factor := adaptiveDownMild
	if signal == pressureStrong {
		factor = adaptiveDownStrong
	}
	previous := a.rate
	a.rate = math.Max(a.floor, a.rate*factor)
	a.lastDown = now
	a.reduced = true
	log.Warnf("adaptive rate down provider=%s rate=%.1f (from %.1f) cause=%v", a.provider, a.rate, previous, err)
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
