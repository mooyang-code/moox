package engine

import "fmt"

// EncodeJSONRequestMeta converts a task and frame into the pyworker JSON v1 meta.
func EncodeJSONRequestMeta(task *FactorTask, frame *DataFrame) (map[string]any, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	if frame == nil {
		return nil, fmt.Errorf("data frame is required")
	}
	columns := make(map[string][]any, len(frame.Columns))
	for colIdx, name := range frame.Columns {
		values := make([]any, 0, len(frame.Rows))
		for _, row := range frame.Rows {
			if colIdx < len(row) {
				values = append(values, row[colIdx])
			} else {
				values = append(values, nil)
			}
		}
		columns[name] = values
	}
	indexMS := make([]int64, 0, len(frame.DataTimes))
	for _, t := range frame.DataTimes {
		indexMS = append(indexMS, t.UTC().UnixMilli())
	}
	factors := make([]map[string]any, 0, len(task.Factors))
	for _, factor := range task.Factors {
		factors = append(factors, map[string]any{
			"factor_id":      factor.FactorID,
			"name":           factor.Name,
			"params":         factor.Params,
			"writeback_bars": factor.WritebackBars,
		})
	}
	return map[string]any{
		"id":             task.TaskID,
		"kind":           task.Kind,
		"encoding":       "json",
		"space_id":       task.SpaceID,
		"source_dataset": task.SourceDataset,
		"target_dataset": task.TargetDataset,
		"subject_id":     task.SubjectID,
		"freq":           task.Freq,
		"bar_time":       task.BarTime.UTC().Format("2006-01-02T15:04:05Z"),
		"factors":        factors,
		"df": map[string]any{
			"columns":  columns,
			"index_ms": indexMS,
		},
	}, nil
}

func DecodeJSONResponse(meta map[string]any) (*FactorResult, error) {
	resultsRaw, _ := meta["results"].(map[string]any)
	out := &FactorResult{
		Columns:     make(map[string]FactorColumnResult, len(resultsRaw)),
		PerFactorMS: decodeInt64Map(meta["per_factor_ms"]),
		ElapsedMS:   numberToInt64(meta["elapsed_ms"]),
	}
	for name, raw := range resultsRaw {
		resultMap, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid result for column %s", name)
		}
		values, _ := resultMap["values"].([]any)
		out.Columns[name] = FactorColumnResult{
			Tail:   int(numberToInt64(resultMap["tail"])),
			Values: values,
		}
	}
	return out, nil
}

func decodeInt64Map(raw any) map[string]int64 {
	out := map[string]int64{}
	values, _ := raw.(map[string]any)
	for key, value := range values {
		out[key] = numberToInt64(value)
	}
	return out
}

func numberToInt64(raw any) int64 {
	switch v := raw.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	default:
		return 0
	}
}
