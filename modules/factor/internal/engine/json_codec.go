package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

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
	dataTimes := make([]string, 0, len(frame.DataTimes))
	for _, t := range frame.DataTimes {
		dataTimes = append(dataTimes, t.UTC().Format(time.RFC3339Nano))
	}
	factors := make([]map[string]any, 0, len(task.Factors))
	for _, factor := range task.Factors {
		var params map[string]any
		if err := json.Unmarshal([]byte(factor.ParamsJSON), &params); err != nil {
			return nil, fmt.Errorf("decode params_json for factor %s: %w", factor.FactorID, err)
		}
		if params == nil {
			return nil, fmt.Errorf("decode params_json for factor %s: expected object", factor.FactorID)
		}
		factors = append(factors, map[string]any{
			"factor_id": factor.FactorID, "name": factor.Name,
			"source_hash": factor.SourceHash, "source_path": factor.SourcePath,
			"input_columns": factor.InputColumns, "outputs": factor.Outputs,
			"params": params,
		})
	}
	return map[string]any{
		"id":                task.TaskID,
		"encoding":          "json",
		"space_id":          task.SpaceID,
		"source_dataset":    task.SourceDataset,
		"target_dataset":    task.TargetDataset,
		"subject_id":        task.SubjectID,
		"freq":              task.Freq,
		"target_start_time": task.StartTime.UTC().Format(time.RFC3339Nano),
		"target_end_time":   task.EndTime.UTC().Format(time.RFC3339Nano),
		"factors":           factors,
		"df": map[string]any{
			"columns": columns, "data_times": dataTimes,
		},
	}, nil
}

func DecodeJSONResponse(meta map[string]any) (*FactorResult, error) {
	resultsRaw, ok := meta["results"].(map[string]any)
	if !ok || len(resultsRaw) == 0 {
		return nil, fmt.Errorf("factor response contains no results")
	}
	out := &FactorResult{Columns: make(map[string][]any, len(resultsRaw))}
	for name, raw := range resultsRaw {
		values, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("factor column %s must be an array", name)
		}
		out.Columns[name] = values
	}
	return out, nil
}
