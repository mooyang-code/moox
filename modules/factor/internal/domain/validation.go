package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// NormalizeFactorDefinition validates and canonicalizes a factor definition.
func NormalizeFactorDefinition(factor FactorDef) (FactorDef, error) {
	factor.FactorID = strings.TrimSpace(factor.FactorID)
	factor.Name = strings.TrimSpace(factor.Name)
	factor.SourceCode = strings.TrimSpace(factor.SourceCode)
	factor.SourceHash = strings.TrimSpace(factor.SourceHash)
	factor.SourcePath = strings.TrimSpace(factor.SourcePath)
	factor.Status = strings.TrimSpace(factor.Status)
	if factor.FactorID == "" || factor.Name == "" || factor.SourceCode == "" {
		return FactorDef{}, fmt.Errorf("factor_id, name and source_code are required")
	}
	if factor.Status == "" {
		factor.Status = FactorStatusDisabled
	}
	if factor.Status != FactorStatusEnabled && factor.Status != FactorStatusDisabled {
		return FactorDef{}, fmt.Errorf("invalid factor status %q", factor.Status)
	}
	var err error
	factor.InputColumns, err = normalizeColumns("input_columns", factor.InputColumns)
	if err != nil {
		return FactorDef{}, err
	}
	factor.Outputs, err = normalizeColumns("outputs", factor.Outputs)
	if err != nil {
		return FactorDef{}, err
	}
	factor.ParamsJSON, err = normalizeParamsJSON(factor.ParamsJSON)
	if err != nil {
		return FactorDef{}, err
	}
	if factor.LookbackPeriods < 1 {
		return FactorDef{}, fmt.Errorf("lookback_periods must be at least 1")
	}
	return factor, nil
}

func normalizeColumns(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%s must contain at least one column", field)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s contains an empty column", field)
		}
		if value == "data_time" || value == "series_tag" {
			return nil, fmt.Errorf("%s contains reserved column %s", field, value)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeParamsJSON(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "{}", nil
	}
	var params map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&params); err != nil {
		return "", fmt.Errorf("params_json must be a JSON object: %w", err)
	}
	if params == nil {
		return "", errors.New("params_json must be a JSON object")
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("params_json contains trailing JSON values")
		}
		return "", fmt.Errorf("params_json contains trailing JSON values: %w", err)
	}
	normalized, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal params_json: %w", err)
	}
	return string(normalized), nil
}
