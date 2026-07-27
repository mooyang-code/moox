package executor

import (
	"context"
	"encoding/json"
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
	SpaceID       string
	DatasetID     string
	TaskID        string
	JobItemID     string
	DeliveryCount uint64
	DataSource    string
	Market        string
	DataType      string
	InstType      string
	SubjectID     string
	Symbol        string
	Interval      string
	Live          bool
}

// executeResult 执行结果
type executeResult struct {
	mu        sync.Mutex
	HasError  bool
	LastError string
	Tasks     []taskExecutionSummary
}

type taskExecutionSummary struct {
	DataType string `json:"data_type"`
	Symbol   string `json:"symbol"`
	Interval string `json:"interval,omitempty"`
	sources.CollectResult
}

type taskStatusReporter func(
	ctx context.Context,
	spaceID string,
	taskID string,
	jobItemID string,
	deliveryCount uint64,
	status int,
	result string,
)

var sendTaskStatus = reporter.ReportTaskStatus

// ErrTaskInstanceReportFailed marks failures at the Collector TaskInstance
// reporting boundary while preserving the underlying transport/service error.
var ErrTaskInstanceReportFailed = errors.New("task instance report failed")

type taskInstanceReportError struct {
	err error
}

func (e *taskInstanceReportError) Error() string {
	return fmt.Sprintf("%s: %v", ErrTaskInstanceReportFailed, e.err)
}

func (e *taskInstanceReportError) Unwrap() error {
	return e.err
}

func (e *taskInstanceReportError) Is(target error) bool {
	return target == ErrTaskInstanceReportFailed
}

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
			DatasetID: task.DatasetID,
			InstType:  task.InstType,
			Symbol:    task.Symbol,
			SubjectID: task.SubjectID,
			Interval:  task.Interval,
			Live:      task.Live,
		}

		log.InfoContextf(ctx, "执行采集: taskID=%s, source=%s, dataType=%s, symbol=%s, interval=%s",
			task.TaskID, task.DataSource, task.DataType, task.Symbol, task.Interval)

		var collectResult sources.CollectResult
		var err error
		if resultCollector, ok := c.(sources.ResultCollector); ok {
			collectResult, err = resultCollector.CollectWithResult(ctx, params)
		} else {
			err = c.Collect(ctx, params)
		}
		if err != nil {
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
				reportStatus(
					ctx, task.SpaceID, task.TaskID, task.JobItemID, task.DeliveryCount,
					reporter.StatusFailed, err.Error(),
				)
				return nil
			}

			// 立即执行场景：返回错误
			return err
		}

		log.InfoContextf(ctx, "采集成功: taskID=%s, interval=%s", task.TaskID, task.Interval)
		if result != nil {
			result.mu.Lock()
			result.Tasks = append(result.Tasks, taskExecutionSummary{
				DataType: task.DataType, Symbol: task.Symbol, Interval: task.Interval,
				CollectResult: collectResult,
			})
			result.mu.Unlock()
		}
		now := time.Now().UTC()
		_ = mooxreport.ObserveModuleRun("collector", "collect", "success", "collector-market-data", now)
		// The Collector interface currently reports only terminal status. Do not
		// use execution time as a business output watermark; a source-closed
		// timestamp must be supplied by the source contract before this is wired.

		// 定时任务场景：上报成功状态
		if reportStatus != nil {
			reportStatus(
				ctx, task.SpaceID, task.TaskID, task.JobItemID, task.DeliveryCount,
				reporter.StatusSuccess, "",
			)
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
				reportStatus(ctx, task.SpaceID, task.TaskID, task.JobItemID, task.DeliveryCount, reporter.StatusFailed,
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

// ExecuteTask keeps downstream collection inside workloadCtx
// while reserving reportCtx for the terminal TaskInstance update.
func ExecuteTask(
	workloadCtx context.Context,
	reportCtx context.Context,
	taskEvent *model.TaskExecuteEvent,
) (string, error) {
	if taskEvent == nil {
		return "", fmt.Errorf("taskEvent is nil")
	}

	log.InfoContextf(workloadCtx, "[ExecuteTask] Starting execution: taskID=%s, symbol=%s",
		taskEvent.TaskID, taskEvent.Symbol)

	// 构建所有需要执行的采集任务
	intervals := taskEvent.Intervals
	if len(intervals) == 0 && strings.EqualFold(taskEvent.DataType, "symbol") {
		intervals = []string{""}
	}
	var collectTasks []*collectTask
	for _, interval := range intervals {
		collectTasks = append(collectTasks, &collectTask{
			SpaceID:       taskEvent.SpaceID,
			DatasetID:     taskEvent.DatasetID,
			TaskID:        taskEvent.TaskID,
			JobItemID:     taskEvent.JobItemID,
			DeliveryCount: taskEvent.DeliveryCount,
			DataSource:    taskEvent.DataSource,
			Market:        normalizeMarket(taskEvent),
			DataType:      taskEvent.DataType,
			InstType:      taskEvent.InstType,
			SubjectID:     taskEvent.SubjectID,
			Symbol:        taskEvent.Symbol,
			Interval:      interval,
			Live:          taskEvent.Live,
		})
	}

	if len(collectTasks) == 0 {
		errMsg := "没有需要执行的interval"
		log.WarnContextf(workloadCtx, "[ExecuteTask] %s", errMsg)
		if err := reportTaskStatus(
			reportCtx, taskEvent.SpaceID, taskEvent.TaskID, taskEvent.JobItemID, taskEvent.DeliveryCount,
			reporter.StatusFailed, errMsg,
		); err != nil {
			return "", err
		}
		return "", errors.New(errMsg)
	}

	// 执行采集任务，统一在最后上报状态。
	result := executeCollectTasks(workloadCtx, collectTasks, nil)

	// 根据执行结果上报状态
	var resultMsg string
	var status int

	if result.HasError {
		status = reporter.StatusFailed
		resultMsg = fmt.Sprintf("部分或全部任务执行失败, lastError=%s", result.LastError)
	} else {
		status = reporter.StatusSuccess
		payload, err := json.Marshal(map[string]any{
			"message": "所有任务执行成功",
			"tasks":   result.Tasks,
		})
		if err != nil {
			return "", fmt.Errorf("encode collection result: %w", err)
		}
		resultMsg = string(payload)
	}

	log.InfoContextf(workloadCtx, "[ExecuteTask] 任务执行完成: taskID=%s, status=%d, result=%s",
		taskEvent.TaskID, status, resultMsg)

	if result.HasError && taskEvent.MaxDeliver > 0 &&
		taskEvent.DeliveryCount < uint64(taskEvent.MaxDeliver) {
		return resultMsg, errors.New(resultMsg)
	}
	if err := reportTaskStatus(
		reportCtx, taskEvent.SpaceID, taskEvent.TaskID, taskEvent.JobItemID, taskEvent.DeliveryCount, status, resultMsg,
	); err != nil {
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

func reportTaskStatus(
	ctx context.Context,
	spaceID string,
	taskID string,
	jobItemID string,
	deliveryCount uint64,
	status int,
	result string,
) error {
	if err := sendTaskStatus(ctx, spaceID, taskID, jobItemID, deliveryCount, status, result); err != nil {
		log.WarnContextf(ctx, "[ExecuteTask] 任务状态上报失败: taskID=%s, status=%d, error=%v",
			taskID, status, err)
		return &taskInstanceReportError{err: err}
	}
	return nil
}
