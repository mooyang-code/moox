package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// ResultSubject resolves result ownership before constructing a storage key.
func ResultSubject(task *FactorTask, row FactorResultRow) (string, error) {
	if task.Factor.FactorType == domain.FactorTypeCrossSection {
		if strings.TrimSpace(row.SubjectID) == "" || !slices.Contains(task.AvailableSubjects, row.SubjectID) {
			return "", fmt.Errorf("cross section result subject %q is outside the available universe", row.SubjectID)
		}
		return row.SubjectID, nil
	}
	if task.SubjectID == "" || (row.SubjectID != "" && row.SubjectID != task.SubjectID) {
		return "", fmt.Errorf("result subject %q does not match task subject %q", row.SubjectID, task.SubjectID)
	}
	return task.SubjectID, nil
}
