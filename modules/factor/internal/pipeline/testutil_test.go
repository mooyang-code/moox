package pipeline

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
	"github.com/stretchr/testify/require"
)

// fakeStore 是只用于写入与上报的 Storage 桩（管线不再从 Storage 读窗口）。
type fakeStore struct{}

func (*fakeStore) DatasetColumns(context.Context, string, string) ([]string, error) { return nil, nil }

func (*fakeStore) ReadWindow(context.Context, storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	return nil, fmt.Errorf("pipeline must not read windows from Storage")
}

func (*fakeStore) WriteRows(context.Context, string, string, string, []storageio.ResultRow) error {
	return nil
}
func (*fakeStore) ReportComputed(context.Context, storageio.PeriodMarker) error { return nil }
func (*fakeStore) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

// viewFromFrames 把按行给出的源数据（data_time、series_tag、数值字段）转成列式视图。
// 帧里的数值一律按 float64 放进视图；非数值值会被拒绝。
func viewFromFrames(frames map[string]*storageio.Frame, stepSeconds int64) (*serieswindow.View, error) {
	cols := make(map[string]*seriesstore.Columns, len(frames))
	var fieldNames []string
	for subject, frame := range frames {
		if frame == nil || len(frame.Rows) == 0 {
			continue
		}
		fields := make([]seriesstore.Field, 0, len(frame.Columns)-2)
		for _, name := range frame.Columns[2:] {
			fields = append(fields, seriesstore.Field{Name: name, Type: seriesstore.TypeFloat64})
		}
		c := seriesstore.NewColumns(fields, len(frame.Rows))
		rows := append([][]any(nil), frame.Rows...)
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i][0].(time.Time), rows[j][0].(time.Time)
			if !a.Equal(b) {
				return a.Before(b)
			}
			return rows[i][1].(string) < rows[j][1].(string)
		})
		for _, row := range rows {
			values := make([]seriesstore.Value, 0, len(fields))
			for _, v := range row[2:] {
				switch n := v.(type) {
				case nil:
					values = append(values, seriesstore.NullValue)
				case float64:
					values = append(values, seriesstore.Float(n))
				case int64:
					values = append(values, seriesstore.Float(float64(n)))
				case int:
					values = append(values, seriesstore.Float(float64(n)))
				default:
					return nil, fmt.Errorf("标的 %s 的值 %v（%T）不是数值", subject, v, v)
				}
			}
			c.AppendRow(row[0].(time.Time), row[1].(string), false, values...)
		}
		cols[subject] = c
		fieldNames = serieswindow.NumericFields(fields)
	}
	return serieswindow.FromColumns(stepSeconds, fieldNames, cols)
}

// withView 按 loaded.Frames 当前的内容重新生成列式视图（测试改了帧之后调用）。
func withView(t *testing.T, loaded LoadResult) LoadResult {
	t.Helper()
	view, err := viewFromFrames(loaded.Frames, 60)
	require.NoError(t, err)
	loaded.View = view
	return loaded
}

// frameData 把按帧给出的源数据适配成 Data：用来测试管线本身，不涉及本地存储与同步。
type frameData struct {
	// read 返回计划需要的各标的的帧（回看窗口起点起的全部行）。
	read            func(ctx context.Context, plan Plan, start time.Time) (map[string]*storageio.Frame, error)
	failed, warming []string

	mu    sync.Mutex
	reads int
}

func (d *frameData) Load(ctx context.Context, plan Plan) (DataResult, error) {
	d.mu.Lock()
	d.reads++
	d.mu.Unlock()
	start, err := readWindowStart(periodclock.Continuous{}, plan)
	if err != nil {
		return DataResult{}, err
	}
	frames, err := d.read(ctx, plan, start)
	if err != nil {
		return DataResult{}, err
	}
	view, err := viewFromFrames(frames, 60)
	if err != nil {
		return DataResult{}, err
	}
	rows := make(map[string]*storageio.Frame, len(frames))
	targetStart := plan.TargetStart
	if plan.Mode == ModeLive {
		targetStart = plan.TargetEnd.Add(-time.Minute)
	}
	for subject, frame := range frames {
		out := &storageio.Frame{SubjectID: subject, Columns: frame.Columns}
		for _, row := range frame.Rows {
			if at := row[0].(time.Time); !at.Before(targetStart) && at.Before(plan.TargetEnd) {
				out.Rows = append(out.Rows, row)
			}
		}
		rows[subject] = out
	}
	return DataResult{View: view, Frames: rows, Failed: d.failed, Warming: d.warming}, nil
}

func (*frameData) WindowStatuses() []WarmupStatus { return nil }

var _ = domain.FactorSet{}

func execKey(subject, tag string) exec.SeriesKey { return exec.SeriesKey{Subject: subject, Tag: tag} }
