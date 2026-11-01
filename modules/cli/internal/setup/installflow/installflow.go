// Package installflow 按依赖并行执行安装步骤：组内并行、组间串行，同一主机串行。
// 执行函数由 command 层注入，这里只管调度和进度。
package installflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
)

// StepTimeout 是单步的总时限：维护锁排队本身没有上限，不能让安装无限等下去。
const StepTimeout = 20 * time.Minute

// Progress 逐行写 JSON 进度，Agent 后台运行后读取。
type Progress struct {
	mu  sync.Mutex
	out io.Writer
	now func() time.Time
}

// NewProgress 创建进度输出。
func NewProgress(out io.Writer) *Progress { return &Progress{out: out, now: time.Now} }

// Event 写一条进度。
func (p *Progress) Event(event string, fields map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	line := map[string]any{"event": event, "at": p.now().UTC().Format(time.RFC3339)}
	for key, value := range fields {
		line[key] = value
	}
	raw, err := json.Marshal(line)
	if err == nil {
		fmt.Fprintln(p.out, string(raw))
	}
}

// Execute 执行一步。
type Execute func(context.Context, deployscan.Step) error

// Run 按组执行步骤。一步失败后，其他主机做完手上这一步就停在安全点，重跑时跳过已完成的步骤。
func Run(ctx context.Context, steps []deployscan.Step, execute Execute, progress *Progress) error {
	for start := 0; start < len(steps); {
		end := start
		for end < len(steps) && steps[end].Group == steps[start].Group {
			end++
		}
		if err := runGroup(ctx, steps[start:end], execute, progress); err != nil {
			return err
		}
		start = end
	}
	return nil
}

func runGroup(ctx context.Context, steps []deployscan.Step, execute Execute, progress *Progress) error {
	lanes := map[string][]deployscan.Step{}
	order := []string{}
	for _, step := range steps {
		if _, seen := lanes[step.Host]; !seen {
			order = append(order, step.Host)
		}
		lanes[step.Host] = append(lanes[step.Host], step)
	}
	var mu sync.Mutex
	var failures []error
	failed := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(failures) != 0
	}
	var workers sync.WaitGroup
	for _, host := range order {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for _, step := range lanes[host] {
				if failed() {
					return
				}
				if err := runStep(ctx, step, execute, progress); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Errorf("%s: %w", step.Command, err))
					mu.Unlock()
					return
				}
			}
		}()
	}
	workers.Wait()
	return errors.Join(failures...)
}

func runStep(ctx context.Context, step deployscan.Step, execute Execute, progress *Progress) error {
	started := time.Now()
	progress.Event("step_started", map[string]any{"step": step.Command})
	stepCtx, cancel := context.WithTimeout(ctx, StepTimeout)
	defer cancel()
	err := execute(stepCtx, step)
	fields := map[string]any{"step": step.Command, "seconds": time.Since(started).Round(time.Second).Seconds()}
	if err != nil {
		fields["error"] = err.Error()
		progress.Event("step_failed", fields)
		return err
	}
	progress.Event("step_done", fields)
	return nil
}
