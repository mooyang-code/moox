package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// EncodeJSONRequestMeta converts one factor task and frame into the pyworker JSON contract.
func EncodeJSONRequestMeta(task *FactorTask, frame *DataFrame) (map[string]any, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	if frame == nil {
		return nil, fmt.Errorf("data frame is required")
	}
	if len(frame.Rows) != len(frame.DataTimes) || len(frame.Rows) != len(frame.SeriesTags) {
		return nil, fmt.Errorf("data frame row identities must align with rows")
	}
	columns := append([]string{"data_time", "series_tag"}, frame.Columns...)
	rows := make([][]any, 0, len(frame.Rows))
	for i, values := range frame.Rows {
		if len(values) != len(frame.Columns) {
			return nil, fmt.Errorf("data frame row %d has %d values for %d columns", i, len(values), len(frame.Columns))
		}
		if err := validateSeriesTag(frame.SeriesTags[i]); err != nil {
			return nil, fmt.Errorf("data frame row %d: %w", i, err)
		}
		row := make([]any, 0, len(columns))
		row = append(row, frame.DataTimes[i].UTC().Format(time.RFC3339Nano), frame.SeriesTags[i])
		row = append(row, values...)
		rows = append(rows, row)
	}
	factor := task.Factor
	if strings.TrimSpace(factor.FactorID) == "" {
		return nil, fmt.Errorf("factor is required")
	}
	params, err := decodeParamsJSON(factor.FactorID, factor.ParamsJSON)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":                task.TaskID,
		"encoding":          "json",
		"space_id":          task.SpaceID,
		"source_dataset":    task.SourceViewID,
		"target_dataset":    task.ResultDatasetID,
		"subject_id":        task.SubjectID,
		"freq":              task.Freq,
		"target_start_time": task.StartTime.UTC().Format(time.RFC3339Nano),
		"target_end_time":   task.EndTime.UTC().Format(time.RFC3339Nano),
		"factor": map[string]any{
			"factor_id": factor.FactorID, "name": factor.Name,
			"source_hash": factor.SourceHash, "source_path": factor.SourcePath,
			"input_columns": factor.InputColumns, "outputs": factor.Outputs,
			"params": params,
		},
		"df": map[string]any{"columns": columns, "rows": rows},
	}, nil
}

// EncodeJSONBatchRequestMeta encodes one shared frame and multiple factor
// specifications. Each member carries its original task identity so the
// response can be written to independent manifests.
func EncodeJSONBatchRequestMeta(batch *BatchTask, frame *DataFrame) (map[string]any, error) {
	if batch == nil || len(batch.Tasks) == 0 {
		return nil, fmt.Errorf("batch tasks are required")
	}
	if frame == nil {
		return nil, fmt.Errorf("data frame is required")
	}
	base, err := encodeFrameMeta(frame)
	if err != nil {
		return nil, err
	}
	first := batch.Tasks[0]
	factors := make([]any, 0, len(batch.Tasks))
	seen := make(map[string]struct{}, len(batch.Tasks))
	for _, task := range batch.Tasks {
		if task.SpaceID != first.SpaceID || task.SourceViewID != first.SourceViewID ||
			task.SourceDataset != first.SourceDataset || task.SubjectID != first.SubjectID ||
			task.Freq != first.Freq {
			return nil, fmt.Errorf("batch task %q does not share the batch execution window", task.TaskID)
		}
		if strings.TrimSpace(task.TaskID) == "" || strings.TrimSpace(task.BindingID) == "" {
			return nil, fmt.Errorf("batch task and binding IDs are required")
		}
		if _, ok := seen[task.TaskID]; ok {
			return nil, fmt.Errorf("duplicate batch task id %s", task.TaskID)
		}
		seen[task.TaskID] = struct{}{}
		factor, err := encodeFactorMeta(task.Factor)
		if err != nil {
			return nil, err
		}
		factors = append(factors, map[string]any{
			"task_id": task.TaskID, "binding_id": task.BindingID,
			"lookback_periods":  task.LookbackPeriods,
			"target_start_time": task.StartTime.UTC().Format(time.RFC3339Nano),
			"target_end_time":   task.EndTime.UTC().Format(time.RFC3339Nano),
			"factor":            factor,
		})
	}
	return map[string]any{
		"id": batch.BatchID, "mode": "batch", "encoding": "json",
		"space_id": first.SpaceID, "source_dataset": first.SourceViewID,
		"target_dataset": first.ResultDatasetID, "subject_id": first.SubjectID,
		"freq": first.Freq, "target_start_time": first.StartTime.UTC().Format(time.RFC3339Nano),
		"target_end_time": first.EndTime.UTC().Format(time.RFC3339Nano),
		"factors":         factors, "df": base,
	}, nil
}

func encodeFactorMeta(factor FactorSpec) (map[string]any, error) {
	if strings.TrimSpace(factor.FactorID) == "" || strings.TrimSpace(factor.Name) == "" {
		return nil, fmt.Errorf("factor id and name are required")
	}
	params, err := decodeParamsJSON(factor.FactorID, factor.ParamsJSON)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"factor_id": factor.FactorID, "name": factor.Name,
		"source_hash": factor.SourceHash, "source_path": factor.SourcePath,
		"input_columns": factor.InputColumns, "outputs": factor.Outputs, "params": params,
	}, nil
}

func encodeFrameMeta(frame *DataFrame) (map[string]any, error) {
	if len(frame.Rows) != len(frame.DataTimes) || len(frame.Rows) != len(frame.SeriesTags) {
		return nil, fmt.Errorf("data frame row identities must align with rows")
	}
	columns := append([]string{"data_time", "series_tag"}, frame.Columns...)
	rows := make([][]any, 0, len(frame.Rows))
	for i, values := range frame.Rows {
		if len(values) != len(frame.Columns) {
			return nil, fmt.Errorf("data frame row %d has %d values for %d columns", i, len(values), len(frame.Columns))
		}
		if err := validateSeriesTag(frame.SeriesTags[i]); err != nil {
			return nil, fmt.Errorf("data frame row %d: %w", i, err)
		}
		row := make([]any, 0, len(columns))
		row = append(row, frame.DataTimes[i].UTC().Format(time.RFC3339Nano), frame.SeriesTags[i])
		row = append(row, values...)
		rows = append(rows, row)
	}
	return map[string]any{"columns": columns, "rows": rows}, nil
}

// DecodeJSONBatchResponse validates identities and decodes partial results.
func DecodeJSONBatchResponse(meta map[string]any) (*BatchResult, error) {
	rawItems, ok := meta["items"].([]any)
	if !ok {
		return nil, fmt.Errorf("factor batch response items must be an array")
	}
	result := &BatchResult{}
	if id, ok := meta["id"].(string); ok {
		result.BatchID = id
	}
	seen := make(map[string]struct{}, len(rawItems))
	for i, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("factor batch response item %d must be an object", i)
		}
		taskID, _ := item["task_id"].(string)
		bindingID, _ := item["binding_id"].(string)
		if taskID == "" || bindingID == "" {
			return nil, fmt.Errorf("factor batch response item %d identity is required", i)
		}
		if _, exists := seen[taskID]; exists {
			return nil, fmt.Errorf("duplicate factor batch response task %s", taskID)
		}
		seen[taskID] = struct{}{}
		entry := BatchItemResult{TaskID: taskID, BindingID: bindingID}
		if ok, _ := item["ok"].(bool); !ok {
			message, _ := item["message"].(string)
			if message == "" {
				message = "factor batch item failed"
			}
			entry.Err = NonRetryableError{Err: errors.New(message)}
		} else {
			decoded, err := DecodeJSONResponse(item)
			if err != nil {
				return nil, fmt.Errorf("decode factor batch item %s: %w", taskID, err)
			}
			entry.Result = decoded
		}
		result.Items = append(result.Items, entry)
	}
	return result, nil
}

func decodeParamsJSON(factorID, raw string) (map[string]any, error) {
	var params map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&params); err != nil {
		return nil, fmt.Errorf("decode params_json for factor %s: %w", factorID, err)
	}
	if params == nil {
		return nil, fmt.Errorf("decode params_json for factor %s: expected object", factorID)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode params_json for factor %s: trailing JSON value", factorID)
		}
		return nil, fmt.Errorf("decode params_json for factor %s: %w", factorID, err)
	}
	return params, nil
}

func DecodeJSONResponse(meta map[string]any) (*FactorResult, error) {
	rawRows, ok := meta["results"].([]any)
	if !ok {
		return nil, fmt.Errorf("factor response results must be an array")
	}
	out := &FactorResult{Rows: make([]FactorResultRow, 0, len(rawRows))}
	for i, raw := range rawRows {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("factor result row %d must be an object", i)
		}
		rawTime, ok := item["data_time"].(string)
		if !ok {
			return nil, fmt.Errorf("factor result row %d data_time must be a string", i)
		}
		dataTime, err := time.Parse(time.RFC3339Nano, rawTime)
		if err != nil {
			return nil, fmt.Errorf("factor result row %d has invalid data_time: %w", i, err)
		}
		seriesTag, ok := item["series_tag"].(string)
		if !ok {
			return nil, fmt.Errorf("factor result row %d series_tag must be a string", i)
		}
		if err := validateSeriesTag(seriesTag); err != nil {
			return nil, fmt.Errorf("factor result row %d: %w", i, err)
		}
		values, ok := item["values"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("factor result row %d values must be an object", i)
		}
		out.Rows = append(out.Rows, FactorResultRow{
			DataTime: dataTime.UTC(), SeriesTag: seriesTag, Values: values,
		})
	}
	return out, nil
}

func validateSeriesTag(tag string) error {
	if !utf8.ValidString(tag) {
		return fmt.Errorf("series_tag must be valid UTF-8")
	}
	if len(tag) > 128 {
		return fmt.Errorf("series_tag must not exceed 128 bytes")
	}
	if strings.TrimSpace(tag) != tag {
		return fmt.Errorf("series_tag must not have leading or trailing whitespace")
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] < 0x20 || tag[i] == 0x7f {
			return fmt.Errorf("series_tag must not contain ASCII control characters")
		}
	}
	return nil
}
