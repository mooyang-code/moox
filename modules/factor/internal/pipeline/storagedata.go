package pipeline

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// storageData 是直接读 Storage 的数据层：用于管理端试算和 run-once 诊断（没有本地时序存储、标的又很少），
// 直接从 Storage 读回看窗口，转成管线需要的列式视图。数值字段进视图，其余列只出现在目标区间的行里。
// 引擎的正式计算不用它，走本地时序存储与内存窗口（localdata）。
type storageData struct {
	store storageio.Store
}

// NewStorageData 返回直接读 Storage 的数据层。
func NewStorageData(store storageio.Store) Data { return storageData{store: store} }

func (d storageData) Load(ctx context.Context, plan Plan) (DataResult, error) {
	start, err := ReadStart(clockFor(plan), plan)
	if err != nil {
		return DataResult{}, err
	}
	frames, err := d.store.ReadWindow(ctx, storageio.ReadRequest{
		SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq,
		Subjects: plan.Available, Start: start, End: plan.TargetEnd, Columns: plan.CarryColumns,
	})
	if err != nil {
		return DataResult{}, err
	}
	step, err := seriesstore.Step(plan.Set.Freq)
	if err != nil {
		return DataResult{}, err
	}
	cols := make(map[string]*seriesstore.Columns, len(frames))
	rows := make(map[string]*storageio.Frame, len(frames))
	var numeric []string
	for subject, frame := range frames {
		fields, positions := numericFields(frame)
		numeric = fields.names()
		c := seriesstore.NewColumns(fields.fields, len(frame.Rows))
		sorted := append([][]any(nil), frame.Rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			a, b := sorted[i][0].(time.Time), sorted[j][0].(time.Time)
			if !a.Equal(b) {
				return a.Before(b)
			}
			return sorted[i][1].(string) < sorted[j][1].(string)
		})
		target := &storageio.Frame{SubjectID: subject, Columns: frame.Columns}
		for _, row := range sorted {
			values := make([]seriesstore.Value, len(positions))
			for i, p := range positions {
				switch v := row[p].(type) {
				case float64:
					values[i] = seriesstore.Float(v)
				case int64:
					values[i] = seriesstore.Float(float64(v))
				default:
					values[i] = seriesstore.NullValue
				}
			}
			c.AppendRow(row[0].(time.Time), row[1].(string), false, values...)
			if at := row[0].(time.Time); !at.Before(plan.TargetStart) && at.Before(plan.TargetEnd) {
				target.Rows = append(target.Rows, row)
			}
		}
		if c.Len() > 0 {
			cols[subject] = c
		}
		rows[subject] = target
	}
	view, err := serieswindow.FromColumns(int64(step/time.Second), numeric, cols)
	if err != nil {
		return DataResult{}, fmt.Errorf("试算读到的数据不连续：%w", err)
	}
	return DataResult{View: view, Frames: rows}, nil
}

func (storageData) WindowStatuses() []WarmupStatus { return nil }

type fieldList struct{ fields []seriesstore.Field }

func (f fieldList) names() []string {
	out := make([]string, len(f.fields))
	for i, x := range f.fields {
		out[i] = x.Name
	}
	return out
}

// numericFields 取帧里的数值列（按第一个非空值判断类型）。
func numericFields(frame *storageio.Frame) (fieldList, []int) {
	var out fieldList
	var positions []int
	for i := 2; i < len(frame.Columns); i++ {
		numeric := true
		seen := false
		for _, row := range frame.Rows {
			if row[i] == nil {
				continue
			}
			seen = true
			switch row[i].(type) {
			case float64, int64:
			default:
				numeric = false
			}
			break
		}
		if numeric && (seen || len(frame.Rows) == 0) {
			out.fields = append(out.fields, seriesstore.Field{Name: frame.Columns[i], Type: seriesstore.TypeFloat64})
			positions = append(positions, i)
		}
	}
	return out, positions
}

func clockFor(Plan) periodclock.Clock { return periodclock.Continuous{} }
