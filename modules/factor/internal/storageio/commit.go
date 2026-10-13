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
	Salt       string         `json:"salt,omitempty"`
	PeriodTime int64          `json:"period_time"`
	Rows       []canonicalRow `json:"rows"`
}

func CommitID(setID string, periodTime int64, rows []ResultRow) string {
	return CommitIDWithSalt(setID, periodTime, "", rows)
}

// CommitIDWithSalt 在内容摘要之外混入每次执行唯一的盐。Storage 对「同提交 ID、同内容」直接返回成功而不再写字段，
// 补算与空洞失效写入要保证「期望重写就一定重写」（例如修复后被写 NULL、再恢复算出同样的值），所以这类写入用盐；
// 盐为空时与 CommitID 完全一致，普通实时写入保持内容寻址去重。
func CommitIDWithSalt(setID string, periodTime int64, salt string, rows []ResultRow) string {
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
	encoded, _ := json.Marshal(canonicalCommit{SetID: setID, Salt: salt, PeriodTime: periodTime, Rows: canonicalRows})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
