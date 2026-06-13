package v2

import (
	"fmt"
	"sort"

	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
)

func sortFrameRows(rows []*pb.QueryFrameRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].GetInstrumentId() == rows[j].GetInstrumentId() {
			return rows[i].GetTime() < rows[j].GetTime()
		}
		return rows[i].GetInstrumentId() < rows[j].GetInstrumentId()
	})
}

func fmtInt(v int64) string {
	return fmt.Sprintf("%d", v)
}

func fmtDouble(v float64) string {
	return fmt.Sprintf("%g", v)
}
