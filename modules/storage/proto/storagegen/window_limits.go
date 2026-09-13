package storagepb

const (
	SeriesWindowMaxSubjects       = 512
	SeriesWindowMaxRows           = 50000
	SeriesWindowMaxRowsPerSubject = 10000
	SeriesWindowMaxCells          = 500000
	SeriesWindowKeyColumns        = 4
)

// SeriesWindowSubjectLimit includes key columns in the cell budget. A zero
// result means even one subject cannot fit without weakening its snapshot.
func SeriesWindowSubjectLimit(rowsPerSubject, businessColumns int) int {
	if rowsPerSubject < 1 || rowsPerSubject > SeriesWindowMaxRowsPerSubject || businessColumns < 0 || businessColumns > SeriesWindowMaxCells-SeriesWindowKeyColumns {
		return 0
	}
	return min(SeriesWindowMaxSubjects, SeriesWindowMaxRows/rowsPerSubject, SeriesWindowMaxCells/rowsPerSubject/(businessColumns+SeriesWindowKeyColumns))
}
