package pipeline

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

type resultKey struct {
	subject string
	at      time.Time
	tag     string
}

type assembledRow struct {
	key    resultKey
	fields map[string]any
}

func (r *Runner) Assemble(plan Plan, loaded LoadResult, computation Computation) ([]storageio.ResultRow, error) {
	if r == nil || r.clock == nil {
		return nil, fmt.Errorf("factor period clock is required")
	}
	target, err := planTarget(r.clock, plan)
	if err != nil {
		return nil, err
	}
	rows := make(map[resultKey]*assembledRow)
	factorColumns := make([]string, 0)
	columnSet := make(map[string]struct{})
	for _, factor := range plan.Factors {
		for _, output := range factor.Outputs {
			if _, exists := columnSet[output]; exists {
				return nil, fmt.Errorf("duplicate factor output column %q", output)
			}
			columnSet[output] = struct{}{}
			factorColumns = append(factorColumns, output)
		}
	}
	carryColumns := append([]string(nil), plan.CarryColumns...)
	if !plan.WriteCarry {
		carryColumns = nil
	}
	for subject, frame := range loaded.Frames {
		if frame == nil {
			continue
		}
		positions, positionErr := framePositions(frame, carryColumns)
		if positionErr != nil {
			return nil, fmt.Errorf("source frame for %q: %w", subject, positionErr)
		}
		for rowNo, values := range frame.Rows {
			if len(values) != len(frame.Columns) {
				return nil, fmt.Errorf("source frame for %q row %d has %d values for %d columns", subject, rowNo, len(values), len(frame.Columns))
			}
			at, timeErr := parseDataTime(values[positions["data_time"]])
			if timeErr != nil {
				return nil, fmt.Errorf("source frame for %q row %d data_time: %w", subject, rowNo, timeErr)
			}
			if !inTargetRange(plan, target, at) {
				continue
			}
			tagValue, ok := values[positions["series_tag"]].(string)
			if !ok {
				return nil, fmt.Errorf("source frame for %q row %d has invalid series_tag", subject, rowNo)
			}
			key := resultKey{subject: subject, at: at.UTC(), tag: tagValue}
			fields := make(map[string]any, len(carryColumns)+len(factorColumns))
			for _, column := range carryColumns {
				fields[column] = values[positions[column]]
			}
			for _, column := range factorColumns {
				fields[column] = nil
			}
			if _, exists := rows[key]; exists {
				return nil, fmt.Errorf("duplicate source row for subject %q at %s tag %q", subject, at, tagValue)
			}
			rows[key] = &assembledRow{key: key, fields: fields}
		}
	}

	failedFactorSubjects := make(map[string]map[string]struct{})
	for _, factor := range plan.Factors {
		bySubject := computation.Results[factor.FactorID]
		if factor.FactorType == domain.FactorTypeCrossSection {
			if item, ok := bySubject[""]; ok {
				if item.Err != nil {
					for _, subject := range plan.Available {
						markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
					}
					continue
				}
				if err := validateResultColumns(item, factor, true); err != nil {
					for _, subject := range plan.Available {
						markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
					}
					continue
				}
				for rowNo, result := range item.Rows {
					at, tag, subject, outputs, decodeErr := decodeResultRow(result, factor, true)
					if decodeErr != nil {
						return nil, fmt.Errorf("factor %q cross-section result row %d: %w", factor.FactorID, rowNo, decodeErr)
					}
					if !subjectIn(plan.Available, subject) {
						return nil, fmt.Errorf("factor %q returned unexpected subject %q", factor.FactorID, subject)
					}
					if !inTargetRange(plan, target, at) {
						continue
					}
					if !applyFactorValues(rows, resultKey{subject: subject, at: at.UTC(), tag: tag}, factor, outputs, factorColumns) {
						markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
					}
				}
			}
			continue
		}

		for subject, item := range bySubject {
			if item.Err != nil {
				markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
				continue
			}
			if err := validateResultColumns(item, factor, false); err != nil {
				markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
				continue
			}
			for rowNo, result := range item.Rows {
				at, tag, _, outputs, decodeErr := decodeResultRow(result, factor, false)
				if decodeErr != nil {
					return nil, fmt.Errorf("factor %q subject %q result row %d: %w", factor.FactorID, subject, rowNo, decodeErr)
				}
				if !inTargetRange(plan, target, at) {
					continue
				}
				if !applyFactorValues(rows, resultKey{subject: subject, at: at.UTC(), tag: tag}, factor, outputs, factorColumns) {
					markFactorSubjectFailure(computation, failedFactorSubjects, factor, subject)
				}
			}
		}
	}

	for _, factor := range plan.Factors {
		failedSubjects := failedFactorSubjects[factor.FactorID]
		if len(failedSubjects) == 0 {
			continue
		}
		for key, row := range rows {
			if _, failed := failedSubjects[key.subject]; !failed {
				continue
			}
			for _, output := range factor.Outputs {
				row.fields[output] = nil
			}
		}
	}

	out := make([]storageio.ResultRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, storageio.ResultRow{SubjectID: row.key.subject, DataTime: row.key.at, SeriesTag: row.key.tag, Fields: row.fields})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubjectID != out[j].SubjectID {
			return out[i].SubjectID < out[j].SubjectID
		}
		if !out[i].DataTime.Equal(out[j].DataTime) {
			return out[i].DataTime.Before(out[j].DataTime)
		}
		return out[i].SeriesTag < out[j].SeriesTag
	})
	return out, nil
}

func framePositions(frame *storageio.Frame, carryColumns []string) (map[string]int, error) {
	if len(frame.Columns) < 2 || frame.Columns[0] != "data_time" || frame.Columns[1] != "series_tag" {
		return nil, fmt.Errorf("columns must start with data_time, series_tag")
	}
	positions := make(map[string]int, len(frame.Columns))
	for index, name := range frame.Columns {
		if _, duplicate := positions[name]; duplicate {
			return nil, fmt.Errorf("duplicate column %q", name)
		}
		positions[name] = index
	}
	for _, name := range carryColumns {
		if _, ok := positions[name]; !ok {
			return nil, fmt.Errorf("carry column %q is missing", name)
		}
	}
	return positions, nil
}

func inTargetRange(plan Plan, liveTarget, at time.Time) bool {
	if plan.Mode == ModeLive {
		return at.Equal(liveTarget)
	}
	return !at.Before(plan.TargetStart) && at.Before(plan.TargetEnd)
}

func parseDataTime(value any) (time.Time, error) {
	switch item := value.(type) {
	case time.Time:
		if item.IsZero() {
			return time.Time{}, fmt.Errorf("time is zero")
		}
		return item.UTC(), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, item)
		if err != nil {
			return time.Time{}, err
		}
		return parsed.UTC(), nil
	case int64:
		return time.Unix(item, 0).UTC(), nil
	case int:
		return time.Unix(int64(item), 0).UTC(), nil
	case float64:
		if math.IsNaN(item) || math.IsInf(item, 0) {
			return time.Time{}, fmt.Errorf("invalid epoch time")
		}
		seconds, fraction := math.Modf(item)
		return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("unexpected time type %T", value)
	}
}

func validateResultColumns(item pyexec.ItemResult, factor domain.FactorDef, cross bool) error {
	want := []string{"data_time", "series_tag"}
	if cross {
		want = append(want, "subject_id")
	}
	want = append(want, factor.Outputs...)
	if len(item.Columns) != len(want) {
		return fmt.Errorf("got %d columns, want %d", len(item.Columns), len(want))
	}
	for i, name := range want {
		if item.Columns[i] != name {
			return fmt.Errorf("column %d is %q, want %q", i, item.Columns[i], name)
		}
	}
	for rowNo, values := range item.Rows {
		if len(values) != len(want) {
			return fmt.Errorf("row %d has %d values, want %d", rowNo, len(values), len(want))
		}
	}
	return nil
}

func decodeResultRow(values []any, factor domain.FactorDef, cross bool) (time.Time, string, string, []any, error) {
	at, err := parseDataTime(values[0])
	if err != nil {
		return time.Time{}, "", "", nil, err
	}
	tag, ok := values[1].(string)
	if !ok {
		return time.Time{}, "", "", nil, fmt.Errorf("series_tag has type %T", values[1])
	}
	index := 2
	subject := ""
	if cross {
		var valid bool
		subject, valid = values[index].(string)
		if !valid || subject == "" {
			return time.Time{}, "", "", nil, fmt.Errorf("subject_id is invalid")
		}
		index++
	}
	return at, tag, subject, values[index:], nil
}

func applyFactorValues(rows map[resultKey]*assembledRow, key resultKey, factor domain.FactorDef, values []any, allFactorColumns []string) bool {
	valid := true
	for _, value := range values {
		if _, ok := numericOrNull(value); !ok {
			valid = false
			break
		}
	}
	row, exists := rows[key]
	if !exists {
		row = &assembledRow{key: key, fields: make(map[string]any)}
		for _, column := range allFactorColumns {
			row.fields[column] = nil
		}
		rows[key] = row
	}
	if !valid {
		for _, column := range factor.Outputs {
			row.fields[column] = nil
		}
		return false
	}
	for i, column := range factor.Outputs {
		row.fields[column] = normalizeNumeric(values[i])
	}
	return true
}

func numericOrNull(value any) (float64, bool) {
	switch item := value.(type) {
	case nil:
		return 0, true
	case int:
		return float64(item), true
	case int8:
		return float64(item), true
	case int16:
		return float64(item), true
	case int32:
		return float64(item), true
	case int64:
		return float64(item), true
	case uint:
		return float64(item), true
	case uint8:
		return float64(item), true
	case uint16:
		return float64(item), true
	case uint32:
		return float64(item), true
	case uint64:
		return float64(item), true
	case float32:
		return float64(item), true
	case float64:
		return item, true
	case json.Number:
		parsed, err := strconv.ParseFloat(string(item), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func normalizeNumeric(value any) any {
	parsed, ok := numericOrNull(value)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return nil
	}
	if _, isFloat := value.(float32); isFloat {
		return parsed
	}
	if _, isFloat := value.(float64); isFloat {
		return parsed
	}
	if _, isNumber := value.(json.Number); isNumber {
		return parsed
	}
	return value
}

func markFactorSubjectFailure(computation Computation, failed map[string]map[string]struct{}, factor domain.FactorDef, subject string) {
	if failed[factor.FactorID] == nil {
		failed[factor.FactorID] = make(map[string]struct{})
	}
	failed[factor.FactorID][subject] = struct{}{}
	state := computation.FactorStates[factor.FactorID]
	state.FactorID = factor.FactorID
	state.Status = "degraded"
	state.SourceHash = factor.SourceHash
	state.FailedSubjects = uniqueSorted(append(state.FailedSubjects, subject))
	computation.FactorStates[factor.FactorID] = state
}

func subjectIn(subjects []string, wanted string) bool {
	for _, subject := range subjects {
		if subject == wanted {
			return true
		}
	}
	return false
}
