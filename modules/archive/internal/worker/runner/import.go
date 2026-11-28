package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/worker/source"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// importIdleTimeout 是导入运行多久没收到批次就结束（moox-cli 中途退出）；测试里改小。
var importIdleTimeout = 30 * time.Minute

// ErrInvalidBatch 表示导入批次不合法（列名不在任务列里、键不符、时间或类型不对），整批拒绝、什么都不写。
var ErrInvalidBatch = errors.New("导入批次不合法")

// ImportBatch 是 moox-cli 经数据端口写来的一批行（与写 Storage 的同一种请求）；结果写进 Result（容量至少 1）。
type ImportBatch struct {
	Req    *storagepb.PrimaryUpsertFieldsReq
	Result chan<- ImportResult
}

// ImportResult 是一批行写进暂存区的结果。
type ImportResult struct {
	Added, Changed, Same uint64
	Err                  error
}

// ImportFeed 是导入运行的输入：Batches 逐批送来行，Finished 关闭表示 moox-cli 已写完全部批次。
type ImportFeed struct {
	Batches  <-chan ImportBatch
	Finished <-chan struct{}
}

// runImport 执行 kind=import：运行占住任务（期间 mgr 不给它生成 follow，不会在两批之间导出半截分区），
// 循环接收批次写进暂存区（同键比对替换，落在已定稿分区的先读回）；收到 finish、空闲超时或被取消时结束。
// 不论哪种结束都先导出、上传、定稿全部到期的分区，已写入的行不会白写。
func (s *runState) runImport(ctx context.Context, out *Outcome, feed *ImportFeed) error {
	if feed == nil || feed.Batches == nil || feed.Finished == nil {
		return errors.New("导入运行缺少批次来源")
	}
	columns := make(map[string]int, len(s.task.Columns))
	for i, c := range s.task.Columns {
		columns[c.Name] = i
	}
	idle := time.NewTimer(importIdleTimeout)
	defer idle.Stop()
	var rows uint64
	var stopErr error
loop:
	for {
		select {
		case <-ctx.Done():
			stopErr = ctx.Err()
			break loop
		case <-feed.Finished:
			break loop
		case <-idle.C:
			stopErr = fmt.Errorf("导入运行 %s 没有收到批次，已结束", importIdleTimeout)
			break loop
		case batch := <-feed.Batches:
			result := s.importBatch(ctx, batch.Req, columns)
			batch.Result <- result
			if result.Err == nil {
				rows += result.Added + result.Changed + result.Same
				s.progress.RowsRead = rows
				s.progress.SubjectsDone = uint64(len(s.subjects))
				if s.report != nil && s.report(s.progress) {
					stopErr = errStopped
					break loop
				}
			}
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(importIdleTimeout)
		}
	}
	// 取消时也要导出已写入的行：用不随运行取消的上下文。
	if err := s.settleUntil(context.WithoutCancel(ctx), out, time.Time{}); err != nil {
		return errors.Join(stopErr, err)
	}
	summary := fmt.Sprintf("导入 %d 行（新增 %d、已存在相同 %d、已存在不同已覆盖 %d），涉及 %d 个分区", rows, s.added, s.same, s.changed, len(s.touched))
	if stopErr != nil {
		note := summary + "；已写入的行已照常导出；用同一条 moox-cli import 命令重跑会按断点跳过已写批次"
		if errors.Is(stopErr, errStopped) || errors.Is(stopErr, context.Canceled) {
			s.cancelNote = note
			return stopErr
		}
		return fmt.Errorf("%w；%s", stopErr, note)
	}
	if extra := s.successMessage(); extra != "" {
		summary += "；" + extra
	}
	out.Message = summary
	return nil
}

// importBatch 把一批请求转成暂存行并写入；任何一行不合法时整批拒绝（返回 ErrInvalidBatch）。
func (s *runState) importBatch(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq, columns map[string]int) ImportResult {
	rows, err := s.batchRows(req, columns)
	if err != nil {
		return ImportResult{Err: fmt.Errorf("%w：%v", ErrInvalidBatch, err)}
	}
	result, err := s.stage(ctx, rows)
	if err != nil {
		return ImportResult{Err: err}
	}
	return ImportResult{Added: result.Added, Changed: result.Changed, Same: result.Same}
}

// batchRows 按列名把 TypedValue 转成暂存值（与读 Storage 同一套规则，source.Convert）。
func (s *runState) batchRows(req *storagepb.PrimaryUpsertFieldsReq, columns map[string]int) ([]staging.Row, error) {
	if req.GetSpaceId() != s.task.SpaceID || req.GetDatasetId() != s.task.DatasetID {
		return nil, fmt.Errorf("批次属于 %s/%s，不是本任务 %s/%s", req.GetSpaceId(), req.GetDatasetId(), s.task.SpaceID, s.task.DatasetID)
	}
	out := make([]staging.Row, 0, len(req.GetRows()))
	for i, upsert := range req.GetRows() {
		key := upsert.GetKey()
		ts := key.GetTimeSeries()
		if ts == nil || key.GetSpaceId() != s.task.SpaceID || key.GetDatasetId() != s.task.DatasetID {
			return nil, fmt.Errorf("第 %d 行的键不是本数据集的时序行键", i+1)
		}
		if strings.TrimSpace(ts.GetSubjectId()) == "" {
			return nil, fmt.Errorf("第 %d 行没有标的", i+1)
		}
		if ts.GetFrequency() != "" && ts.GetFrequency() != s.a.GetFrequency() {
			return nil, fmt.Errorf("第 %d 行的频率 %s 与任务频率 %s 不符", i+1, ts.GetFrequency(), s.a.GetFrequency())
		}
		at, err := time.Parse(time.RFC3339Nano, ts.GetDataTime())
		if err != nil {
			return nil, fmt.Errorf("第 %d 行的 data_time %q 不是 RFC3339 时间", i+1, ts.GetDataTime())
		}
		if err := staging.CheckNanosRange(at); err != nil {
			return nil, fmt.Errorf("第 %d 行的 data_time: %w", i+1, err)
		}
		row := staging.Row{SubjectID: ts.GetSubjectId(), DataTime: at.UTC(), SeriesTag: ts.GetSeriesTag(), Values: make([]any, len(s.task.Columns))}
		for _, field := range upsert.GetFields() {
			index, ok := columns[field.GetFieldId()]
			if !ok {
				return nil, fmt.Errorf("第 %d 行的列 %q 不在任务的列里", i+1, field.GetFieldId())
			}
			value, err := source.Convert(s.task.Columns[index].Type, field.GetValue())
			if err != nil {
				return nil, fmt.Errorf("第 %d 行的列 %s: %v", i+1, field.GetFieldId(), err)
			}
			if t, ok := value.(time.Time); ok {
				if err := staging.CheckNanosRange(t); err != nil {
					return nil, fmt.Errorf("第 %d 行的列 %s: %w", i+1, field.GetFieldId(), err)
				}
			}
			row.Values[index] = value
		}
		out = append(out, row)
	}
	return out, nil
}
