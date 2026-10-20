package marketfetch

import (
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

// 函数进程自身的运行状态：整点取数变慢时要区分慢在行情源还是函数进程（冷启动、CPU 被占满导致调度迟滞）。
var (
	processStart  = time.Now()
	invocationSeq atomic.Int64
)

// schedProbe 在取数期间每 10 毫秒让出一次调度并记录实际唤醒比预期晚多少：函数 CPU 吃紧（128MB 规格份额很小）时，
// 握手、读响应都会被拖慢，最大迟滞会明显变大；正常时只有几毫秒。
type schedProbe struct {
	stop       chan struct{}
	done       chan struct{}
	maxLag     time.Duration
	goroutines int
}

func startSchedProbe() *schedProbe {
	probe := &schedProbe{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(probe.done)
		const step = 10 * time.Millisecond
		next := time.Now().Add(step)
		timer := time.NewTimer(step)
		defer timer.Stop()
		for {
			select {
			case <-probe.stop:
				return
			case now := <-timer.C:
				if lag := now.Sub(next); lag > probe.maxLag {
					probe.maxLag = lag
				}
				if n := runtime.NumGoroutine(); n > probe.goroutines {
					probe.goroutines = n
				}
				next = now.Add(step)
				timer.Reset(step)
			}
		}
	}()
	return probe
}

// finish 停止探测并返回批次摘要的附加字段。
func (p *schedProbe) finish() map[string]string {
	close(p.stop)
	<-p.done
	return map[string]string{
		"sched_lag_max_ms": strconv.FormatInt(p.maxLag.Milliseconds(), 10),
		"goroutines_max":   strconv.Itoa(p.goroutines),
	}
}

// runtimeFields 是函数进程与触发的上下文字段：进程已存活多久（冷启动时很小）、第几次调用、CPU 数，
// 以及定时事件迟到、函数内错峰等待和从入口到开始取数的时间。
func runtimeFields(req Request, started time.Time) map[string]string {
	fields := map[string]string{
		"proc_age_ms":    strconv.FormatInt(started.Sub(processStart).Milliseconds(), 10),
		"invocation_seq": strconv.FormatInt(invocationSeq.Load(), 10),
		"num_cpu":        strconv.Itoa(runtime.NumCPU()),
	}
	if req.TimerLateMS != 0 {
		fields["timer_late_ms"] = strconv.Itoa(req.TimerLateMS)
	}
	if req.StaggerMS != 0 {
		fields["stagger_ms"] = strconv.Itoa(req.StaggerMS)
	}
	if !req.EntryAt.IsZero() {
		fields["entry_to_start_ms"] = strconv.FormatInt(started.Sub(req.EntryAt).Milliseconds(), 10)
	}
	return fields
}
