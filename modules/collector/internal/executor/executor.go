package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/reporter"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/model"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// collectTask 采集任务定义
type collectTask struct {
	TaskID     string
	DataSource string
	DataType   string
	InstType   string
	Symbol     string
	Interval   string
}

// executeResult 执行结果
type executeResult struct {
	mu        sync.Mutex
	HasError  bool
	LastError string
}

type taskStatusReporter func(ctx context.Context, taskID string, status int, result string)

var reportTaskStatusAsync taskStatusReporter = reporter.ReportTaskStatusAsync
var reportTaskStatusSync taskStatusReporter = func(ctx context.Context, taskID string, status int, result string) {
	if err := reporter.ReportTaskStatus(ctx, taskID, status, result); err != nil {
		log.WarnContextf(ctx, "任务状态同步上报失败: taskID=%s, status=%d, error=%v", taskID, status, err)
	}
}

// ScheduledExecute 定时执行采集任务（由 TRPC 定时器调用）
// 该函数每分钟整点触发，检查所有任务是否该执行
func ScheduledExecute(c context.Context, _ string) error {
	ctx := trpc.CloneContext(c)
	return executeDueTasksAt(ctx, time.Now(), reportTaskStatusAsync)
}

// ExecuteDueTasks 执行当前节点到期任务，并等待任务状态同步上报完成。
// SCF 事件型函数在请求返回后不保证后台 goroutine 继续运行，因此 keepalive
// 触发的任务执行需要使用同步上报路径。
func ExecuteDueTasks(ctx context.Context) error {
	return executeDueTasksAt(ctx, time.Now(), reportTaskStatusSync)
}

func executeDueTasksAt(ctx context.Context, now time.Time, reportStatus taskStatusReporter) error {
	nodeID, version := config.GetNodeInfo()
	log.WithContextFields(ctx, "func", "ScheduledExecute", "version", version, "nodeID", nodeID)

	if nodeID == "" {
		log.DebugContext(ctx, "NodeID 为空，跳过本次执行")
		return nil
	}

	// 获取本节点的任务配置
	taskInstances := config.GetTaskInstancesByNode(nodeID)
	if len(taskInstances) == 0 {
		log.DebugContextf(ctx, "[ScheduledExecute] 没有需要执行的任务 (nodeID=%s)", nodeID)
		return nil
	}

	// #region agent log
	log.InfoContextf(ctx, "[DEBUG_AGENT] client_task_fetch: nodeID=%s, taskCount=%d, tasksMD5=%s, timestamp=%d",
		nodeID, len(taskInstances), config.GetCurrentTasksMD5(), now.Unix())
	// #endregion

	log.InfoContextf(ctx, "[ScheduledExecute] 开始执行采集任务，当前时间: %s, 任务数: %d, nodeID=%s",
		now.Format("15:04:05"), len(taskInstances), nodeID)

	// 打印所有任务信息
	for i, task := range taskInstances {
		// #region agent log
		log.InfoContextf(ctx, "[DEBUG_AGENT] client_task_detail: nodeID=%s, taskIndex=%d, taskID=%s, symbol=%s, intervals=%v, taskParams=%s",
			nodeID, i, task.TaskID, task.Symbol, task.Intervals, task.TaskParams)
		// #endregion

		log.InfoContextf(ctx, "[ScheduledExecute] Task[%d]: TaskID=%s, DataType=%s, DataSource=%s, Symbol=%s, Intervals=%v",
			i, task.TaskID, task.DataType, task.DataSource, task.Symbol, task.Intervals)
	}

	// 收集所有需要执行的采集任务
	var collectTasks []*collectTask
	for _, taskInstance := range taskInstances {
		// 为每个需要执行的 interval 创建一个采集任务
		for _, interval := range taskInstance.Intervals {
			shouldExec := shouldExecute(interval, now)
			log.DebugContextf(ctx, "[ScheduledExecute] Check interval: symbol=%s, interval=%s, shouldExecute=%v",
				taskInstance.Symbol, interval, shouldExec)

			if !shouldExec {
				continue
			}

			log.InfoContextf(ctx, "[ScheduledExecute] Will execute: symbol=%s, interval=%s",
				taskInstance.Symbol, interval)

			collectTasks = append(collectTasks, &collectTask{
				TaskID:     taskInstance.TaskID,
				DataSource: taskInstance.DataSource,
				DataType:   taskInstance.DataType,
				InstType:   taskInstance.InstType,
				Symbol:     taskInstance.Symbol,
				Interval:   interval,
			})
		}
	}

	if len(collectTasks) == 0 {
		log.DebugContextf(ctx, "当前时刻没有需要执行的任务")
		return nil
	}

	// 执行采集任务（定时任务场景：每个任务执行后立即上报状态）
	executeCollectTasks(ctx, collectTasks, reportStatus)

	log.InfoContextf(ctx, "本轮采集任务执行完成")
	return nil
}

// shouldExecute 判断当前时刻是否应该执行指定周期的任务
// interval: K线周期，如 "1m", "5m", "1h" 等
// now: 当前时间
func shouldExecute(interval string, now time.Time) bool {
	minute := now.Minute()
	hour := now.Hour()

	switch interval {
	case "1m":
		// 每分钟执行
		return true
	case "3m":
		// 每3分钟执行（0, 3, 6, 9, ...）
		return minute%3 == 0
	case "5m":
		// 每5分钟执行（0, 5, 10, 15, ...）
		return minute%5 == 0
	case "15m":
		// 每15分钟执行（0, 15, 30, 45）
		return minute%15 == 0
	case "30m":
		// 每30分钟执行（0, 30）
		return minute%30 == 0
	case "1h":
		// 每小时整点执行
		return minute == 0
	case "2h":
		// 每2小时整点执行
		return minute == 0 && hour%2 == 0
	case "4h":
		// 每4小时整点执行
		return minute == 0 && hour%4 == 0
	case "6h":
		// 每6小时整点执行
		return minute == 0 && hour%6 == 0
	case "12h":
		// 每12小时整点执行（0点、12点）
		return minute == 0 && hour%12 == 0
	case "1d":
		// 每天0点执行
		return minute == 0 && hour == 0
	case "1w":
		// 每周一0点执行
		return minute == 0 && hour == 0 && now.Weekday() == time.Monday
	case "1M":
		// 每月1号0点执行
		return minute == 0 && hour == 0 && now.Day() == 1
	default:
		log.Warnf("未知的时间周期: %s", interval)
		return false
	}
}

// buildCollectHandler 构建单个采集任务的处理函数
// reportOnError: 是否在错误时上报状态（定时任务场景使用，立即执行场景不在此上报）
func buildCollectHandler(
	ctx context.Context,
	task *collectTask,
	c collector.Collector,
	result *executeResult,
	reportStatus taskStatusReporter,
) func() error {
	return func() error {
		params := &collector.CollectParams{
			InstType: task.InstType,
			Symbol:   task.Symbol,
			Interval: task.Interval,
		}

		log.InfoContextf(ctx, "执行采集: taskID=%s, source=%s, dataType=%s, symbol=%s, interval=%s",
			task.TaskID, task.DataSource, task.DataType, task.Symbol, task.Interval)

		if err := c.Collect(ctx, params); err != nil {
			log.ErrorContextf(ctx, "采集失败: taskID=%s, interval=%s, error=%v",
				task.TaskID, task.Interval, err)

			// 记录错误（使用互斥锁保证并发安全）
			if result != nil {
				result.mu.Lock()
				result.HasError = true
				result.LastError = err.Error()
				result.mu.Unlock()
			}

			// 定时任务场景：上报失败后继续执行其他任务
			if reportStatus != nil {
				reportStatus(ctx, task.TaskID, reporter.StatusFailed, err.Error())
				return nil
			}

			// 立即执行场景：返回错误
			return err
		}

		log.InfoContextf(ctx, "采集成功: taskID=%s, interval=%s", task.TaskID, task.Interval)

		// 定时任务场景：上报成功状态
		if reportStatus != nil {
			reportStatus(ctx, task.TaskID, reporter.StatusSuccess, "")
		}

		return nil
	}
}

// executeCollectTasks 执行采集任务列表
// reportStatus: 非 nil 时在每个任务执行后上报状态；nil 用于立即执行场景统一汇总上报。
func executeCollectTasks(
	ctx context.Context,
	tasks []*collectTask,
	reportStatus taskStatusReporter,
) *executeResult {
	if len(tasks) == 0 {
		return &executeResult{}
	}

	result := &executeResult{}
	handlers := make([]func() error, 0, len(tasks))

	for _, task := range tasks {
		// 获取采集器
		c, err := collector.GetRegistry().Get(task.DataSource, task.DataType)
		if err != nil {
			log.WarnContextf(ctx, "未找到采集器: source=%s, dataType=%s, taskID=%s",
				task.DataSource, task.DataType, task.TaskID)
			if reportStatus != nil {
				reportStatus(ctx, task.TaskID, reporter.StatusFailed,
					fmt.Sprintf("采集器未找到: source=%s, dataType=%s", task.DataSource, task.DataType))
			} else {
				result.mu.Lock()
				result.HasError = true
				result.LastError = fmt.Sprintf("采集器未找到: source=%s, dataType=%s", task.DataSource, task.DataType)
				result.mu.Unlock()
			}
			continue
		}

		// 构建处理函数
		handler := buildCollectHandler(ctx, task, c, result, reportStatus)
		handlers = append(handlers, handler)
	}

	if len(handlers) == 0 {
		return result
	}

	log.InfoContextf(ctx, "并发执行 %d 个采集任务", len(handlers))

	// 并发执行所有采集任务
	_ = trpc.GoAndWait(handlers...)

	return result
}

// ExecuteTaskImmediately 立即执行任务（服务端触发的任务转移）
// 用于任务失败后，服务端将任务转移到其他节点立即执行
// 注意：客户端在上报失败前已经进行了多次重试，这里直接执行即可
func ExecuteTaskImmediately(ctx context.Context, taskEvent *model.TaskExecuteEvent) (string, error) {
	if taskEvent == nil {
		return "", fmt.Errorf("taskEvent is nil")
	}

	log.InfoContextf(ctx, "[ExecuteTaskImmediately] Starting immediate execution: taskID=%s, symbol=%s",
		taskEvent.TaskID, taskEvent.Symbol)

	// 构建所有需要执行的采集任务
	var collectTasks []*collectTask
	for _, interval := range taskEvent.Intervals {
		collectTasks = append(collectTasks, &collectTask{
			TaskID:     taskEvent.TaskID,
			DataSource: taskEvent.DataSource,
			DataType:   taskEvent.DataType,
			InstType:   taskEvent.InstType,
			Symbol:     taskEvent.Symbol,
			Interval:   interval,
		})
	}

	if len(collectTasks) == 0 {
		errMsg := "没有需要执行的interval"
		log.WarnContextf(ctx, "[ExecuteTaskImmediately] %s", errMsg)
		reportImmediateTaskStatus(ctx, taskEvent.TaskID, reporter.StatusFailed, errMsg)
		return "", errors.New(errMsg)
	}

	// 执行采集任务（立即执行场景：统一在最后上报状态）
	result := executeCollectTasks(ctx, collectTasks, nil)

	// 根据执行结果上报状态
	var resultMsg string
	var status int

	if result.HasError {
		status = reporter.StatusFailed
		resultMsg = fmt.Sprintf("部分或全部任务执行失败, lastError=%s", result.LastError)
	} else {
		status = reporter.StatusSuccess
		resultMsg = "所有任务执行成功"
	}

	log.InfoContextf(ctx, "[ExecuteTaskImmediately] 任务执行完成: taskID=%s, status=%d, result=%s",
		taskEvent.TaskID, status, resultMsg)

	reportImmediateTaskStatus(ctx, taskEvent.TaskID, status, resultMsg)

	return resultMsg, nil
}

func reportImmediateTaskStatus(ctx context.Context, taskID string, status int, result string) {
	if err := reporter.ReportTaskStatus(ctx, taskID, status, result); err != nil {
		log.WarnContextf(ctx, "[ExecuteTaskImmediately] 任务状态上报失败: taskID=%s, status=%d, error=%v",
			taskID, status, err)
	}
}
