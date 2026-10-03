package storageio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

type canonicalField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type canonicalRow struct {
	SubjectID string           `json:"subject_id"`
	DataTime  string           `json:"data_time"`
	SeriesTag string           `json:"series_tag"`
	Fields    []canonicalField `json:"fields"`
}

type canonicalCommit struct {
	SetID      string         `json:"set_id"`
	PeriodTime int64          `json:"period_time"`
	Rows       []canonicalRow `json:"rows"`
}

func CommitID(setID string, periodTime int64, rows []ResultRow) string {
	canonicalRows := make([]canonicalRow, 0, len(rows))
	for _, row := range rows {
		fields := make([]canonicalField, 0, len(row.Fields))
		for name, value := range row.Fields {
			fields = append(fields, canonicalField{Name: name, Value: canonicalValue(value)})
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		at := row.DataTime.UTC().Format(time.RFC3339Nano)
		canonicalRows = append(canonicalRows, canonicalRow{
			SubjectID: row.SubjectID, DataTime: at, SeriesTag: row.SeriesTag, Fields: fields,
		})
	}
	sort.Slice(canonicalRows, func(i, j int) bool {
		left, right := canonicalRows[i], canonicalRows[j]
		if left.SubjectID != right.SubjectID {
			return left.SubjectID < right.SubjectID
		}
		leftTime, _ := time.Parse(time.RFC3339Nano, left.DataTime)
		rightTime, _ := time.Parse(time.RFC3339Nano, right.DataTime)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		if left.SeriesTag != right.SeriesTag {
			return left.SeriesTag < right.SeriesTag
		}
		leftBytes, _ := json.Marshal(left.Fields)
		rightBytes, _ := json.Marshal(right.Fields)
		return string(leftBytes) < string(rightBytes)
	})
	encoded, _ := json.Marshal(canonicalCommit{SetID: setID, PeriodTime: periodTime, Rows: canonicalRows})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
