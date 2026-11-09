package storageio

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
)

// Backend 是 Client 提供的全部 Storage 能力。
type Backend interface {
	Store
	Metadata
	ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (Universe, bool, error)
	EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error)
	ReadDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *ChangeCursor, fromHead bool, limit int) (Changes, error)
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

// Axis 是 Storage 客户端层的换算边界：调用方（引擎、试算的管线）只用轴上时间，Storage 里是真实时间。
// 读进来的 data_time、变更区间、最早一期 ToAxis；写出去的 data_time、周期完成标记 FromAxis；读取区间的排他终点
// 换成最后一根的真实收盘时刻。7×24 市场的轴就是真实时间，原样透传。
type Axis struct {
	Backend
	// freqs 缓存数据集的频率（空间/数据集 → 频率）：写结果行与查完成标记的接口不带频率，数据集的频率不会变。
	freqs sync.Map
}

// AllSubjectsLister 是 Storage 客户端的可选能力：列出数据集登记过的全部标的（含已下架的）。
type AllSubjectsLister interface {
	ListAllDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

// ListAllDatasetSubjects 透传给被包装的客户端；它不支持时退回只列在册标的。
func (s *Axis) ListAllDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	if lister, ok := s.Backend.(AllSubjectsLister); ok {
		return lister.ListAllDatasetSubjects(ctx, spaceID, datasetID)
	}
	return s.Backend.ListDatasetSubjects(ctx, spaceID, datasetID)
}

// NewAxis 用换算边界包装 Storage 客户端。
func NewAxis(inner Backend) *Axis { return &Axis{Backend: inner} }

// axisOf 返回空间、频率的换算；7×24 市场（或未登记的空间，交给 Storage 自己报错）返回 ok 为假，表示原样透传。
func axisOf(spaceID, freq string) (periodclock.Axis, bool, error) {
	if !sessionSpace(spaceID) {
		return periodclock.Axis{}, false, nil
	}
	axis, err := periodclock.AxisFor(spaceID, freq)
	if err != nil {
		return periodclock.Axis{}, false, err
	}
	return axis, true, nil
}

// datasetAxis 是不带频率的接口用的换算：频率取数据集自己的。
func (s *Axis) datasetAxis(ctx context.Context, spaceID, datasetID string) (periodclock.Axis, bool, error) {
	if !sessionSpace(spaceID) {
		return periodclock.Axis{}, false, nil
	}
	key := spaceID + "/" + datasetID
	freq, cached := s.freqs.Load(key)
	if !cached {
		info, err := s.Backend.GetDataset(ctx, spaceID, datasetID)
		if err != nil {
			return periodclock.Axis{}, false, err
		}
		if info.Freq == "" {
			return periodclock.Axis{}, false, fmt.Errorf("数据集 %s/%s 没有频率", spaceID, datasetID)
		}
		freq = info.Freq
		s.freqs.Store(key, freq)
	}
	return axisOf(spaceID, freq.(string))
}

// sessionSpace 报告空间是否有交易时段（轴与真实时间不同）。
func sessionSpace(spaceID string) bool {
	_, err := periodclock.ForSpace(spaceID)
	return err == nil && !periodclock.IsContinuous(spaceID)
}

func (s *Axis) ReadWindow(ctx context.Context, req ReadRequest) (map[string]*Frame, error) {
	axis, ok, err := axisOf(req.SpaceID, req.Freq)
	if err != nil || !ok {
		if err != nil {
			return nil, err
		}
		return s.Backend.ReadWindow(ctx, req)
	}
	inner := req
	// 起点早于轴的纪元（交易日历的第一个交易日）时没有任何交易桶：原值就早于所有真实数据。
	if !req.Start.Before(time.Unix(0, 0)) {
		if inner.Start, err = axis.FromAxis(req.Start); err != nil {
			return nil, fmt.Errorf("换算读取起点：%w", err)
		}
	}
	if inner.End, err = axis.FromAxisEnd(req.End); err != nil {
		return nil, fmt.Errorf("换算读取终点：%w", err)
	}
	frames, err := s.Backend.ReadWindow(ctx, inner)
	if err != nil {
		return nil, err
	}
	for subject, frame := range frames {
		for _, row := range frame.Rows {
			at, _ := row[0].(time.Time)
			if row[0], err = axis.ToAxis(at); err != nil {
				return nil, fmt.Errorf("标的 %s 的行 data_time：%w", subject, err)
			}
		}
	}
	return frames, nil
}

func (s *Axis) ReadDatasetChanges(ctx context.Context, spaceID, datasetID string, cursor *ChangeCursor, fromHead bool, limit int) (Changes, error) {
	changes, err := s.Backend.ReadDatasetChanges(ctx, spaceID, datasetID, cursor, fromHead, limit)
	if err != nil {
		return Changes{}, err
	}
	for i := range changes.Changes {
		for j := range changes.Changes[i].Ranges {
			r := &changes.Changes[i].Ranges[j]
			axis, ok, err := axisOf(spaceID, r.Freq)
			if err != nil {
				return Changes{}, err
			}
			if !ok {
				continue
			}
			// 变更区间两端都是闭区间（受影响的第一根与最后一根）。
			if r.Start, err = axis.ToAxis(r.Start); err != nil {
				return Changes{}, fmt.Errorf("变更区间起点：%w", err)
			}
			if r.End, err = axis.ToAxis(r.End); err != nil {
				return Changes{}, fmt.Errorf("变更区间终点：%w", err)
			}
		}
	}
	return changes, nil
}

func (s *Axis) EarliestPeriod(ctx context.Context, spaceID, datasetID, freq string, subjects, columns []string) (time.Time, bool, error) {
	earliest, found, err := s.Backend.EarliestPeriod(ctx, spaceID, datasetID, freq, subjects, columns)
	if err != nil || !found {
		return earliest, found, err
	}
	axis, ok, err := axisOf(spaceID, freq)
	if err != nil || !ok {
		return earliest, found, err
	}
	at, err := axis.ToAxis(earliest)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("最早一期：%w", err)
	}
	return at, true, nil
}

func (s *Axis) WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []ResultRow) error {
	axis, ok, err := s.datasetAxis(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	if !ok {
		return s.Backend.WriteRows(ctx, spaceID, datasetID, commitID, rows)
	}
	out := make([]ResultRow, len(rows))
	for i, row := range rows {
		out[i] = row
		if out[i].DataTime, err = axis.FromAxis(row.DataTime); err != nil {
			return fmt.Errorf("结果行 %d 的 data_time：%w", i, err)
		}
	}
	return s.Backend.WriteRows(ctx, spaceID, datasetID, commitID, out)
}

func (s *Axis) ReportComputed(ctx context.Context, marker PeriodMarker) error {
	axis, ok, err := axisOf(marker.SpaceID, marker.Frequency)
	if err != nil {
		return err
	}
	if ok {
		at, err := axis.FromAxis(time.Unix(marker.PeriodTime, 0))
		if err != nil {
			return fmt.Errorf("周期完成标记的周期：%w", err)
		}
		marker.PeriodTime = at.Unix()
	}
	return s.Backend.ReportComputed(ctx, marker)
}

// periodToReal 把不带频率的接口里的轴上周期（Unix 秒）换成真实周期。
func (s *Axis) periodToReal(ctx context.Context, spaceID, datasetID string, periodTime int64) (int64, error) {
	axis, ok, err := s.datasetAxis(ctx, spaceID, datasetID)
	if err != nil || !ok {
		return periodTime, err
	}
	at, err := axis.FromAxis(time.Unix(periodTime, 0))
	if err != nil {
		return 0, fmt.Errorf("周期完成标记的周期：%w", err)
	}
	return at.Unix(), nil
}

func (s *Axis) ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error) {
	periodTime, err := s.periodToReal(ctx, spaceID, datasetID, periodTime)
	if err != nil {
		return false, err
	}
	return s.Backend.ComputedExists(ctx, spaceID, datasetID, triggerEventID, periodTime)
}

func (s *Axis) ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (Universe, bool, error) {
	periodTime, err := s.periodToReal(ctx, spaceID, datasetID, periodTime)
	if err != nil {
		return Universe{}, false, err
	}
	return s.Backend.ComputedUniverse(ctx, spaceID, datasetID, triggerEventID, periodTime)
}

var _ Backend = (*Client)(nil)
var _ Backend = (*Axis)(nil)
