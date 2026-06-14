package storage

import (
	"fmt"
	"sort"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func sortQueryViewRows(rows []*pb.QueryViewRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].GetSubjectId() == rows[j].GetSubjectId() {
			return rows[i].GetDataTime() < rows[j].GetDataTime()
		}
		return rows[i].GetSubjectId() < rows[j].GetSubjectId()
	})
}

func fmtInt(v int64) string {
	return fmt.Sprintf("%d", v)
}

func fmtDouble(v float64) string {
	return fmt.Sprintf("%g", v)
}
