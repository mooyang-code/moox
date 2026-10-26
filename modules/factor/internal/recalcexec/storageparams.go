package recalcexec

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
)

// StorageParamReader 从任务的结果数据集读取已有行的 __params 列。
type StorageParamReader struct {
	store storageio.Store
}

func NewStorageParamReader(store storageio.Store) *StorageParamReader {
	return &StorageParamReader{store: store}
}

// ReadRowParams 实现 ParamReader：__params 为空或不是合法 JSON 的行按"没有这一行的记录"处理，
// 补算时会按默认参数新补并记入作业结果，而不是让整个作业失败。
func (r *StorageParamReader) ReadRowParams(ctx context.Context, set domain.FactorTask, subjects []string, start, end time.Time) (map[pipeline.RowKey]map[string]map[string]float64, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("storage store is required")
	}
	frames, err := r.store.ReadWindow(ctx, storageio.ReadRequest{
		SpaceID: set.SpaceID, DatasetID: set.ResultDatasetID, Freq: set.Freq, Subjects: subjects,
		Start: start, End: end, Columns: []string{factorname.ParamsColumn},
	})
	if err != nil {
		return nil, err
	}
	out := make(map[pipeline.RowKey]map[string]map[string]float64)
	for subject, frame := range frames {
		if frame == nil {
			continue
		}
		for _, row := range frame.Rows {
			if len(row) < 3 {
				continue
			}
			at, ok := row[0].(time.Time)
			if !ok {
				continue
			}
			tag, _ := row[1].(string)
			text := ""
			switch v := row[2].(type) {
			case storageio.JSONText:
				text = string(v)
			case string:
				text = v
			}
			if text == "" {
				continue
			}
			members, parseErr := factorname.ParseParams(text)
			if parseErr != nil {
				continue
			}
			out[pipeline.RowKey{Subject: subject, At: at.UTC(), Tag: tag}] = members
		}
	}
	return out, nil
}
