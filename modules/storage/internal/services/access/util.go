package access

import (
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
