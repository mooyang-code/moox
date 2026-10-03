package pyexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type FactorCall struct {
	FactorID        string          `json:"factor_id"`
	Name            string          `json:"name"`
	SourceHash      string          `json:"source_hash"`
	SourcePath      string          `json:"source_path"`
	FactorType      string          `json:"factor_type"`
	InputColumns    []string        `json:"input_columns"`
	Outputs         []string        `json:"outputs"`
	Params          json.RawMessage `json:"params"`
	LookbackPeriods int             `json:"lookback_periods"`
}

type Request struct {
	Frame   any            `json:"-"`
	Factors []FactorCall   `json:"-"`
	Context map[string]any `json:"-"`
}

type ItemResult struct {
	FactorID string
	Columns  []string
	Rows     [][]any
	Err      error
}

type Executor interface {
	Exec(ctx context.Context, req Request) ([]ItemResult, error)
	Busy() int
	Close() error
}

var errInvalidResponse = errors.New("factor Python worker returned an invalid batch response")

type frameDTO struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type factorDTO struct {
	FactorID        string          `json:"factor_id"`
	Name            string          `json:"name"`
	SourceHash      string          `json:"source_hash"`
	SourcePath      string          `json:"source_path"`
	FactorType      string          `json:"factor_type"`
	InputColumns    []string        `json:"input_columns"`
	Outputs         []string        `json:"outputs"`
	Params          json.RawMessage `json:"params"`
	LookbackPeriods int             `json:"lookback_periods"`
}

type runDTO struct {
	ID       string         `json:"id"`
	Encoding string         `json:"encoding"`
	Frame    frameDTO       `json:"df"`
	Factors  []factorDTO    `json:"factors"`
	Context  map[string]any `json:"context"`
}

func encodeRequest(requestID string, req Request) ([]byte, error) {
	if strings.TrimSpace(requestID) == "" {
		return nil, errors.New("request id is required")
	}
	if req.Context == nil {
		return nil, errors.New("factor execution context is required")
	}
	contextValue, err := normalizeJSON(req.Context)
	if err != nil {
		return nil, fmt.Errorf("encode factor execution context: %w", err)
	}
	if err := rejectLegacyFields(contextValue); err != nil {
		return nil, err
	}
	if len(req.Factors) == 0 {
		return nil, errors.New("factor calls are required")
	}
	frame, err := encodeFrame(req.Frame)
	if err != nil {
		return nil, err
	}
	factors := make([]factorDTO, 0, len(req.Factors))
	seen := make(map[string]struct{}, len(req.Factors))
	for _, factor := range req.Factors {
		if err := validateFactorCall(factor); err != nil {
			return nil, fmt.Errorf("factor %q: %w", factor.FactorID, err)
		}
		if _, ok := seen[factor.FactorID]; ok {
			return nil, fmt.Errorf("duplicate factor_id %s", factor.FactorID)
		}
		seen[factor.FactorID] = struct{}{}
		params := factor.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		factors = append(factors, factorDTO{
			FactorID: factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash,
			SourcePath: factor.SourcePath, FactorType: factor.FactorType,
			InputColumns: append([]string(nil), factor.InputColumns...), Outputs: append([]string(nil), factor.Outputs...),
			Params: append(json.RawMessage(nil), params...), LookbackPeriods: factor.LookbackPeriods,
		})
	}
	payload, err := json.Marshal(runDTO{
		ID: requestID, Encoding: "json", Frame: frame, Factors: factors,
		Context: req.Context,
	})
	if err != nil {
		return nil, fmt.Errorf("encode factor batch request: %w", err)
	}
	return payload, nil
}

func encodeFrame(value any) (frameDTO, error) {
	if value == nil {
		return frameDTO{}, errors.New("factor input frame is required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return frameDTO{}, fmt.Errorf("encode factor input frame: %w", err)
	}
	var frame frameDTO
	if err := json.Unmarshal(raw, &frame); err != nil {
		return frameDTO{}, fmt.Errorf("factor input frame must contain columns and rows: %w", err)
	}
	if len(frame.Columns) < 2 || frame.Columns[0] != "data_time" || frame.Columns[1] != "series_tag" {
		return frameDTO{}, errors.New("factor input frame columns must start with data_time, series_tag")
	}
	if frame.Rows == nil {
		frame.Rows = [][]any{}
	}
	seen := make(map[string]struct{}, len(frame.Columns))
	for _, column := range frame.Columns {
		if column == "" {
			return frameDTO{}, errors.New("factor input frame columns must not be empty")
		}
		if _, ok := seen[column]; ok {
			return frameDTO{}, fmt.Errorf("duplicate factor input frame column %q", column)
		}
		seen[column] = struct{}{}
	}
	for i, row := range frame.Rows {
		if len(row) != len(frame.Columns) {
			return frameDTO{}, fmt.Errorf("factor input frame row %d has %d values for %d columns", i, len(row), len(frame.Columns))
		}
	}
	return frame, nil
}

func validateFactorCall(factor FactorCall) error {
	if strings.TrimSpace(factor.FactorID) == "" || strings.TrimSpace(factor.Name) == "" {
		return errors.New("factor_id and name are required")
	}
	if !sourceHashPattern.MatchString(factor.SourceHash) {
		return errors.New("source_hash must be sha256:<lowercase-hex>")
	}
	if strings.TrimSpace(factor.SourcePath) == "" {
		return errors.New("source_path is required")
	}
	if factor.FactorType != "timeseries" && factor.FactorType != "cross_section" {
		return fmt.Errorf("invalid factor_type %q", factor.FactorType)
	}
	if factor.LookbackPeriods < 1 {
		return errors.New("lookback_periods must be positive")
	}
	if err := validateColumnList("input_columns", factor.InputColumns, false); err != nil {
		return err
	}
	if err := validateColumnList("outputs", factor.Outputs, false); err != nil {
		return err
	}
	if !json.Valid(factor.Params) && len(factor.Params) != 0 {
		return errors.New("params must be a JSON object")
	}
	params := factor.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(params, &object); err != nil || object == nil {
		return errors.New("params must be a JSON object")
	}
	return nil
}

func validateColumnList(name string, values []string, allowEmpty bool) error {
	if len(values) == 0 && !allowEmpty {
		return fmt.Errorf("%s must not be empty", name)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must contain non-empty names", name)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s must be unique", name)
		}
		seen[value] = struct{}{}
		if name == "outputs" && (value == "data_time" || value == "series_tag" || value == "subject_id") {
			return fmt.Errorf("outputs must not contain identity column %q", value)
		}
	}
	return nil
}

func normalizeJSON(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return normalized, nil
}

func rejectLegacyFields(value any) error {
	legacy := map[string]struct{}{
		"task_id": {}, "binding_id": {}, "config_snapshot_id": {}, "missing_subjects": {},
	}
	var visit func(any) error
	visit = func(value any) error {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				if _, ok := legacy[key]; ok {
					return fmt.Errorf("legacy field %s is not supported", key)
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range current {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}
