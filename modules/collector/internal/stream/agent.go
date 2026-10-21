package stream

import (
	"context"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	heartbeatInterval = 10 * time.Second
	reportInterval    = 5 * time.Second
	maxBufferedStats  = 2000
)

// LocalSink 接收分给本地执行端（定时拉取）的计划项。
type LocalSink interface {
	Apply(items []PlanItem)
}

// Agent 把运行时与 Collector 连起来：每 10 秒心跳，计划版本变化时拉取并应用，再在心跳里回报已应用的版本；
// 周期统计与疑似下架攒批上报。
type Agent struct {
	Client  *PlanClient
	Runtime *Runtime
	Local   LocalSink

	mu       sync.Mutex
	applied  PlanVersion
	latest   PlanVersion
	stats    []PeriodStat
	suspects []Suspect
	lastBeat time.Time
	wake     chan struct{}
}

// NewAgent 构造代理；运行时的统计与疑似下架回调应指向 Agent.Collect 与 Agent.Flag。
func NewAgent(client *PlanClient, runtime *Runtime, local LocalSink) *Agent {
	return &Agent{Client: client, Runtime: runtime, Local: local, wake: make(chan struct{}, 1)}
}

// Collect 收集一期统计等待上报。
func (a *Agent) Collect(stat PeriodStat) {
	a.mu.Lock()
	if len(a.stats) < maxBufferedStats {
		a.stats = append(a.stats, stat)
	}
	a.mu.Unlock()
}

// Flag 收集一个疑似下架的标的并尽快上报（Collector 收到后立即同步标签）。
func (a *Agent) Flag(suspect Suspect) {
	a.mu.Lock()
	a.suspects = append(a.suspects, suspect)
	a.mu.Unlock()
	notify(a.wake)
}

// Synced 判断已应用 Collector 要求的最新计划版本（就绪检查用）。
func (a *Agent) Synced() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.lastBeat.IsZero() && a.applied == a.latest
}

// Applied 返回已应用的计划版本。
func (a *Agent) Applied() PlanVersion {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.applied
}

// Run 运行到 ctx 结束。
func (a *Agent) Run(ctx context.Context) {
	heartbeat := time.NewTicker(heartbeatInterval)
	report := time.NewTicker(reportInterval)
	defer heartbeat.Stop()
	defer report.Stop()
	a.beat(ctx)
	for {
		select {
		case <-ctx.Done():
			a.flush(context.WithoutCancel(ctx))
			return
		case <-heartbeat.C:
			a.beat(ctx)
		case <-report.C:
			a.flush(ctx)
		case <-a.wake:
			a.flush(ctx)
		}
	}
}

func (a *Agent) beat(ctx context.Context) {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	latest, err := a.Client.Heartbeat(callCtx, a.Applied(), a.Runtime.Status())
	if err != nil {
		log.Warnf("collector-stream heartbeat failed: %v", err)
		return
	}
	// 提交预留随心跳下发：配置变化不改计划版本，也要让已有闸门用上新值。
	a.Runtime.SetCommitReserve(a.Client.CommitReserve())
	a.mu.Lock()
	a.latest, a.lastBeat = latest, time.Now()
	changed := latest != a.applied
	a.mu.Unlock()
	if !changed {
		return
	}
	pullCtx, cancelPull := context.WithTimeout(ctx, 10*time.Second)
	version, items, reserve, err := a.Client.Pull(pullCtx)
	cancelPull()
	if err != nil {
		log.Warnf("collector-stream pull plan failed: %v", err)
		return
	}
	a.Runtime.SetCommitReserve(reserve)
	if err := a.Runtime.Apply(items); err != nil {
		// 有市场起不来：不确认这个版本，下一次心跳重试。
		log.Errorf("collector-stream apply plan %d/%s failed: %v", version.ID, version.Stage, err)
		return
	}
	if a.Local != nil {
		a.Local.Apply(items)
	}
	a.mu.Lock()
	a.applied = version
	a.mu.Unlock()
	log.Infof("collector-stream applied plan %d/%s items=%d", version.ID, version.Stage, len(items))
	// 立即回报，Collector 看到确认才算下发完成。
	confirmCtx, cancelConfirm := context.WithTimeout(ctx, 5*time.Second)
	defer cancelConfirm()
	if latest, err := a.Client.Heartbeat(confirmCtx, version, a.Runtime.Status()); err == nil {
		a.mu.Lock()
		a.latest = latest
		a.mu.Unlock()
	}
}

func (a *Agent) flush(ctx context.Context) {
	a.mu.Lock()
	stats, suspects := a.stats, a.suspects
	a.stats, a.suspects = nil, nil
	a.mu.Unlock()
	if len(stats) == 0 && len(suspects) == 0 {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.Client.Report(callCtx, stats, suspects); err != nil {
		log.Warnf("collector-stream report failed: %v", err)
		a.mu.Lock()
		// 失败时放回，下次再报；统计有上限，疑似下架不丢。
		a.stats = append(stats[max(0, len(stats)+len(a.stats)-maxBufferedStats):], a.stats...)
		a.suspects = append(suspects, a.suspects...)
		a.mu.Unlock()
	}
}
