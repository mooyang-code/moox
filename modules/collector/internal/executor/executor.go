package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/reporter"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	mooxreport "github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// collectTask 采集任务定义
type collectTask struct {
	SpaceID    string
	TaskID     string
	DataSource string
	Market     string
	DataType   string
	InstType   string
	SubjectID  string
	Symbol     string
	Interval   string
	Live       bool
}

// executeResult 执行结果
type executeResult struct {
	mu        sync.Mutex
	HasError  bool
	LastError string
}

type taskStatusReporter func(ctx context.Context, spaceID string, taskID string, status int, result string)

var reportTaskStatus = reporter.ReportTaskStatus

// buildCollectHandler 构建单个采集任务的处理函数
// reportOnError: 是否在错误时上报状态（定时任务场景使用，立即执行场景不在此上报）
func buildCollectHandler(
	ctx context.Context,
	task *collectTask,
	c sources.Collector,
	result *executeResult,
	reportStatus taskStatusReporter,
) func() error {
	return func() error {
		params := &sources.CollectParams{
			SpaceID:   task.SpaceID,
			InstType:  task.InstType,
			Symbol:    task.Symbol,
			SubjectID: task.SubjectID,
			Interval:  task.Interval,
			Live:      task.Live,
		}

		log.InfoContextf(ctx, "执行采集: taskID=%s, source=%s, dataType=%s, symbol=%s, interval=%s",
			task.TaskID, task.DataSource, task.DataType, task.Symbol, task.Interval)

		if err := c.Collect(ctx, params); err != nil {
			_ = mooxreport.ObserveModuleRun("collector", "collect", "error", "collector-market-data", time.Now())
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
				reportStatus(ctx, task.SpaceID, task.TaskID, reporter.StatusFailed, err.Error())
				return nil
			}

			// 立即执行场景：返回错误
			return err
		}

		log.InfoContextf(ctx, "采集成功: taskID=%s, interval=%s", task.TaskID, task.Interval)
		now := time.Now().UTC()
		_ = mooxreport.ObserveModuleRun("collector", "collect", "success", "collector-market-data", now)
		// The Collector interface currently reports only terminal status. Do not
		// use execution time as a business output watermark; a source-closed
		// timestamp must be supplied by the source contract before this is wired.

		// 定时任务场景：上报成功状态
		if reportStatus != nil {
			reportStatus(ctx, task.SpaceID, task.TaskID, reporter.StatusSuccess, "")
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
		c, err := sources.GetRegistry().Get(task.DataSource, task.Market, task.DataType)
		if err != nil {
			log.WarnContextf(ctx, "未找到采集器: source=%s, market=%s, dataType=%s, taskID=%s",
				task.DataSource, task.Market, task.DataType, task.TaskID)
			if reportStatus != nil {
				reportStatus(ctx, task.SpaceID, task.TaskID, reporter.StatusFailed,
					fmt.Sprintf("采集器未找到: source=%s, market=%s, dataType=%s", task.DataSource, task.Market, task.DataType))
			} else {
				result.mu.Lock()
				result.HasError = true
				result.LastError = fmt.Sprintf("采集器未找到: source=%s, market=%s, dataType=%s", task.DataSource, task.Market, task.DataType)
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
	intervals := taskEvent.Intervals
	if len(intervals) == 0 && strings.EqualFold(taskEvent.DataType, "symbol") {
		intervals = []string{""}
	}
	var collectTasks []*collectTask
	for _, interval := range intervals {
		collectTasks = append(collectTasks, &collectTask{
			SpaceID:    taskEvent.SpaceID,
			TaskID:     taskEvent.TaskID,
			DataSource: taskEvent.DataSource,
			Market:     normalizeMarket(taskEvent),
			DataType:   taskEvent.DataType,
			InstType:   taskEvent.InstType,
			SubjectID:  taskEvent.SubjectID,
			Symbol:     taskEvent.Symbol,
			Interval:   interval,
			Live:       taskEvent.Live,
		})
	}

	if len(collectTasks) == 0 {
		errMsg := "没有需要执行的interval"
		log.WarnContextf(ctx, "[ExecuteTaskImmediately] %s", errMsg)
		if err := reportImmediateTaskStatus(ctx, taskEvent.SpaceID, taskEvent.TaskID, reporter.StatusFailed, errMsg); err != nil {
			return "", err
		}
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

	if err := reportImmediateTaskStatus(ctx, taskEvent.SpaceID, taskEvent.TaskID, status, resultMsg); err != nil {
		return resultMsg, err
	}
	if result.HasError {
		return resultMsg, errors.New(resultMsg)
	}

	return resultMsg, nil
}

func normalizeMarket(taskEvent *model.TaskExecuteEvent) string {
	if taskEvent == nil {
		return "spot"
	}
	if taskEvent.Market != "" {
		return taskEvent.Market
	}
	switch strings.ToUpper(taskEvent.InstType) {
	case "SWAP":
		return "swap"
	default:
		return "spot"
	}
}

func reportImmediateTaskStatus(ctx context.Context, spaceID string, taskID string, status int, result string) error {
	if err := reportTaskStatus(ctx, spaceID, taskID, status, result); err != nil {
		log.WarnContextf(ctx, "[ExecuteTaskImmediately] 任务状态上报失败: taskID=%s, status=%d, error=%v",
			taskID, status, err)
		return err
	}
	return nil
}
